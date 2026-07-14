# 构建 Agent（服务端）

本页讲如何把一个 Go agent 暴露为 A2A server：启动 HTTP 服务、实现 `MessageProcessor`、选择存储后端，并按需打开鉴权、推送通知、多租户、子路径部署、legacy 兼容和遥测。调用 agent 见 [客户端](client.md)；协议对象与交互模型见 [协议](protocol.md)。

```bash
go get trpc.group/trpc-go/trpc-a2a-go/v2
```

## 最小组成

一个 A2A server 由三件事组成：

| 部分 | 你提供什么 | 框架做什么 |
| --- | --- | --- |
| agent card | agent 的身份、URL、能力、技能、鉴权声明 | 暴露 `/.well-known/agent-card.json`，并归一化 v1/v0 字段。 |
| `TaskManager` | 选择 memory、Redis 或自定义实现 | 管理任务、事件、会话历史、取消、订阅和留存。 |
| `MessageProcessor` | 你的 agent 逻辑 | 接收 `ExecContext`，消费你返回的事件流，派生一元和流式响应。 |

最小启动代码如下：

```go
import (
    "trpc.group/trpc-go/trpc-a2a-go/v2/server"
    "trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

tm, _ := memory.NewTaskManager(&myProcessor{})
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(agentCard))
srv.Start(":8080")   // 在 "/" 服务 JSON-RPC，在 /.well-known/agent-card.json 服务 card
```

`NewA2AServer` 接收函数式 option:

| Option | 用途 |
| --- | --- |
| `WithAgentCard(card)` | 公开默认 agent card。单 agent server 必填；多租户 server 可作为默认目录 card。 |
| `WithTenantCard(tenant, card)` / `WithTenantCardProvider(fn)` | 注册按租户解析的 card，多租户时使用。 |
| `WithAuthenticatedExtendedCardHandler(fn)` | 开启鉴权后的 extended agent card。 |
| `WithAuthProvider(p)` | 要求 JSON-RPC 请求鉴权。 |
| `WithPushNotificationJWKSHandler(handler)` | 发布 push sender 的验证公钥。`SignedSender` 传 `sender.JWKSHandler()`，也可传自定义 `http.Handler`。 |
| `WithJWKSEndpoint(false, "")` | 公钥由别处发布时关闭内置 JWKS route；传非空 path 可修改该 route。 |
| `WithBasePath(prefix)` | 挂载到子路径。 |
| `WithJSONRPCEndpoint(path)` | 自定义 JSON-RPC endpoint。通常优先用 `WithBasePath`。 |
| `WithCompatHandler(h)` | 同时服务 legacy v0.2.x wire。 |
| `WithMiddleware(mw...)` | 包裹 HTTP handler 链。 |
| `WithCORSEnabled(true)` | 输出 CORS 头。 |
| `WithReadTimeout` / `WithWriteTimeout` / `WithIdleTimeout` | HTTP server 超时。 |
| `WithTelemetryMeterProvider(mp)` / `WithTelemetryMeterProviderOptions(...)` | 注入已有 meter provider，或让 server 创建 OTLP provider。 |
| `WithFirstTokenPolicy(p)` | 自定义 TTFT 首 token 识别策略。 |

用 `srv.Stop(ctx)` 停机，它会先排空在途轮次再关闭。

## 定义 MessageProcessor

你的 agent 就是一个方法:

```go
type MessageProcessor interface {
    ProcessMessage(ctx context.Context, ec *ExecContext) (<-chan protocol.StreamEvent, error)
}
```

你从一份只读快照读入请求,返回一个事件 channel——任务的事件日志。框架用这一个方法服务 `SendMessage`(一元)与 `SendStreamingMessage`(流式);你的代码里没有流式/非流式分支。

`ExecContext` 携带你应答所需的一切:`Message`(收到的消息)、`TaskID`(预分配)、`Task`(续跑轮的当前快照,首轮为 `nil`)、`ContextID`、`Tenant`、`History`(会话快照)、`AcceptedOutputModes`、`PushConfig`(客户端内联的 webhook 配置,若有)。

推荐包一个 **`TaskHandle`**——一个小的辅助函数,承载熟悉的动词(`UpdateTaskState`、`AddArtifact`、`AppendArtifact`、`Reply`)并把要返回的 channel 交给你。同步函数体原样可用;`Events()` 之前的 emit 永不阻塞,不需要 goroutine。→ [examples/basic](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/basic)

```go
func (p *proc) ProcessMessage(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
    h := taskmanager.NewTaskHandle(ctx, ec)
    defer h.Close()
    h.UpdateTaskState(protocol.TaskStateWorking, nil)
    result := doWork(ec.Message)
    h.AddArtifact(result.Artifact, true)                              // lastChunk=true
    h.UpdateTaskState(protocol.TaskStateCompleted, taskmanager.ReplyText("done"))
    return h.Events(), nil
}
```

动词:`UpdateTaskState(state, message)`、`AddArtifact(artifact, lastChunk)`、`AppendArtifact(artifact, lastChunk)`、`Reply(message)`,以及读取 `TaskID()`、`GetContextID()`、`GetTask()`、`GetMessageHistory()`。`AddArtifact` 新增 artifact(或替换同 ID 的 artifact),`AppendArtifact` 追加续块,并且必须复用同一个 `ArtifactID`。`taskmanager.ReplyText(text)` 构造一条 agent 消息。

**`TaskHandle` 底层就是 channel 操作。** 真正的契约是你返回的 `<-chan protocol.StreamEvent`:`UpdateTaskState` 发一个 `*protocol.TaskStatusUpdateEvent`,`AddArtifact` 和 `AppendArtifact` 发一个 `*protocol.TaskArtifactUpdateEvent`,`Reply` 发一个 `*protocol.Message`,`Close` 关闭 channel。你很少需要,但可以自己构造并发送这些事件——这也是够到 `TaskHandle` 不暴露的字段的唯一办法:

```go
out := make(chan protocol.StreamEvent, 4)
go func() {
    defer close(out)
    out <- &protocol.TaskStatusUpdateEvent{Status: protocol.TaskStatus{State: protocol.TaskStateWorking}}
    out <- &protocol.TaskArtifactUpdateEvent{Artifact: art, LastChunk: &done, Metadata: map[string]any{"seq": 1}}
    out <- &protocol.TaskStatusUpdateEvent{Status: protocol.TaskStatus{State: protocol.TaskStateCompleted}}
}()
return out, nil
```

（[examples/simple](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/simple)是一个完全用裸 channel 写的 processor。）

### 常见形态

- **纯回复**(不产生任务):`h.Reply(taskmanager.ReplyText("..."))`。
- **实时流式**——把函数体放进 goroutine,事件就会实时到达 `SendStreamingMessage` 的消费者;长循环里检查 `ctx.Err()`,被取消时直接关闭(框架落 `CANCELED`)。→ [examples/streaming](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/streaming)
- **多轮**——用 `h.UpdateTaskState(protocol.TaskStateInputRequired, taskmanager.ReplyText("need more"))` 挂起并关闭;后续消息(回传 `taskId`)作为新一轮到来,此时 `ec.Task` 已就位。

### 写 Processor 时记住这几条

这几条是避免任务卡住或历史丢失的核心规则：

- **谁发事件，谁负责关闭。** 如果你在 goroutine 里发事件，就在那个 goroutine 里关闭 channel 或 `TaskHandle`。channel 不关闭，轮次就不会结束，任务也会一直占着执行槽。
- **每轮都要给出结论。** 正常结束用 `completed` / `failed` / `canceled` / `rejected`；需要用户继续输入时用 `input-required` / `auth-required`。如果还停在 `submitted` 或 `working` 就关闭，框架会把任务标成 `FAILED`。
- **一轮只属于一个任务。** 事件默认属于 `ec.TaskID`。不要发其他 `taskId` 的事件，也不要自己发 `*protocol.Task` 快照；任务快照只由框架生成。
- **要让后续对话记住终答，就发 `Message`。** 当前 `status.message` 只留在 `Task.Status`；后续状态或 follow-up 用户消息取代它时，上一条 status message 才会移入历史。终态 status message 不会再被取代，只留在 `status.Message`；artifact 也永不进历史。需要保留的回答，尤其是 LLM 最终回复，请作为 `Message` 事件发出。

## 轮次生命周期

一个**轮次**就是一次 `ProcessMessage` 调用，以及框架把它返回的 channel 读完的过程。可以把它理解为“agent 推进一次任务”的最小执行单位。

轮次大致按这个顺序发生：

1. 框架收到 `SendMessage` 或 `SendStreamingMessage`，准备好 `ExecContext`，然后调用你的 `ProcessMessage`。
2. 你的 processor 返回事件 channel。
3. 框架读取事件：每个事件都会先持久化，再返回给调用方或订阅者。
4. channel 关闭时，本轮结束；框架根据最后的任务状态收尾。

关键语义如下：

- **任务是懒创建的。** 只有发出 status 或 artifact 这类任务事件后，任务才真正落库。只发 `Message` 的轮次不会留下任务；对该轮预分配 ID 调 `GetTask` 会得到 not-found。
- **同一任务同一时间只能跑一轮。** 上一轮还没结束时，针对同一个 `taskId` 的后续消息会被拒绝：`-32602`，`"already has an active execution"`。
- **channel 关闭才算结束。** 关闭时框架应用下面的规则：

  | 关闭时的情况 | 结果 |
  | --- | --- |
  | 已经发过终态 | 正常结束，保留你发出的终态 |
  | 停在 `input-required` / `auth-required` | 任务挂起，等待下一条带相同 `taskId` 的消息 |
  | 停在 `submitted` / `working` | 标记为 `FAILED`，错误信息是 `"processor finished without terminal state"` |
  | 收到取消且没有发出终态 | 标记为 `CANCELED` |

- **挂起会立刻让出任务。** 发出 `input-required` / `auth-required` 后，框架允许续跑轮次开始。旧轮次之后再发出的事件会被丢弃；完成结果应该由续跑轮次交付。
- **违反事件契约会失败。** 例如给别的 `taskId` 发事件、发 `*protocol.Task` 快照、发没有状态的 status，都会让本轮任务失败，并丢弃后续事件。

## 执行与取消

client 断开连接不会自动取消 agent 的工作。框架会让轮次在一个与请求连接分离的 context 上继续跑，结果仍然可以通过 `GetTask` 或 `SubscribeToTask` 取回。

真正会取消 processor `ctx` 的只有两类动作：client 调 `CancelTask`，或者 manager / server 停机。收到取消后，推荐做法是停止继续发送普通进度，尽快关闭 channel；如果你需要收尾，也可以发出自己的终态事件，框架会尊重它。

`CancelTask` 返回的是“发起取消那一刻”的任务快照，所以它可能仍然是 `working`。最终是否落成 `CANCELED`，要等 processor 停下并关闭 channel。已经终态的任务不能取消，会返回 `-32002`。

## 响应生成

你的 processor 只写一条事件流，框架会按不同调用方式生成不同响应：

| 调用方式 | 框架如何响应 |
| --- | --- |
| `SendMessage`（默认阻塞） | 等本轮结束。只要本轮创建过任务，就返回最终任务快照；如果只是纯回复，则返回最后一条 `Message`。没有任何事件是 processor bug，返回 `-32603`。 |
| `SendMessage` + `returnImmediately=true` | 返回最早可用结果：第一个已落库的任务快照，或第一条 `Message`。如果 client 需要后续跟踪任务，processor 应先发任务事件。 |
| `SendStreamingMessage` | 按事件顺序实时转发；每个事件都会先持久化再投递。流在终态或挂起帧结束。 |
| `SubscribeToTask` | 先发送当前任务快照，再发送实时增量。终态任务不能订阅。 |

## 会话、历史，以及什么会被记住

会话历史只记录“对话”，不记录所有运行细节。框架按 `messageId` 保存消息本体，再按 `contextId` 维护会话索引。会进入会话历史的内容：

- 每一轮的请求消息；
- processor 主动发出的 `Message` 事件；
- 被后续状态或 follow-up 用户消息取代的 **上一条 `status.message`**；例如用户继续 `input-required` 任务时，提问会在新用户消息之前移入历史。

不会进入会话历史的内容也很重要：

- **当前** `status.message` 只留在 `Task.Status`，不会同时出现在 history；终态消息不会再被取代，因此始终只留在 `status.Message`；
- artifact 是任务交付物，只挂在任务上，永不进历史。

所以，如果你希望下一轮还能看到某段内容，例如 LLM 的最终回答、用户确认后的摘要、工具调用后的结论，就把它作为 `Message` 事件发出。否则下一轮的 `ec.History` 可能只有用户输入，看不到 agent 上一轮真正说了什么。

`Task.history` 是响应时按 `historyLength` 从会话里临时组出来的；`ec.History` 是本轮开始前拍下的快照，并按 `MaxHistoryLength`（默认 100）截断。

请求里的 `configuration` 也按职责分开：`returnImmediately` 和 `historyLength` 由框架消费；`acceptedOutputModes` 会进入 `ec.AcceptedOutputModes`，`taskPushNotificationConfig` 会进入 `ec.PushConfig`，交给你的 processor 自行决定怎么用。

## 存储后端

`TaskManager` 拥有任务与会话状态。内置两个后端。

**内存**——零依赖、单进程:

```go
tm, _ := memory.NewTaskManager(proc,
    memory.WithMaxHistoryLength(100),
    memory.WithConversationTTL(time.Hour, 30*time.Second),
    memory.WithTaskTTL(time.Hour),   // 0 = 终态任务永久保留(默认)
)
```

**Redis**——跨进程共享、重启不丢:

```go
import redistm "trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/redis"

tm, _ := redistm.NewTaskManager(proc, redisClient,   // 注意参数序:(processor, client)
    redistm.WithExpireTime(time.Hour),                // key TTL
    redistm.WithMaxHistoryLength(100),
)
```

→ [examples/redis](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/redis)。实现 `taskmanager.TaskManager` 接口即可自带后端。

留存:

| | memory 后端 | redis 后端 |
| --- | --- | --- |
| 会话 | 闲置超 `ConversationTTL`(默认 1h)即清理;单会话上限 `MaxHistoryLength` | key TTL(默认 1h),写时续期 |
| 终态任务 | **默认永久保留**(`TaskTTL`=0)——生产环境请设置 `memory.WithTaskTTL` | key TTL(默认 1h,`WithExpireTime`) |
| 挂起任务 | 永不回收(清理器只收终态)——请让 client 恢复或取消它们 | 随 key TTL 过期 |

没有按任务的删除 API;A2A 未定义此类接口。Redis 后端上,实时事件扇出是进程内的(快照共享)。

## 鉴权

在服务端要求鉴权；三种方案可链式组合。客户端一侧见 [客户端](client.md)。

```go
provider := auth.NewChainAuthProvider(
    auth.NewJWTAuthProvider(secret, audience, issuer, time.Hour),
    auth.NewAPIKeyAuthProvider(keyMap, "X-API-Key"),
    auth.NewOAuth2AuthProviderWithConfig(oauth2Config, userInfoURL, userIDField),
)
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card), server.WithAuthProvider(provider))
```

card 的 `securitySchemes` 公示服务端接受什么。→ [examples/auth](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/auth)。

## 扩展 Agent Card

公开 agent card 适合放基础身份、公开技能和鉴权声明。如果某些技能、内部路由或配额信息只想在鉴权后暴露，可以开启 extended agent card：

```go
enabled := true
card.Capabilities.ExtendedAgentCard = &enabled

srv, _ := server.NewA2AServer(tm,
    server.WithAgentCard(card),
    server.WithAuthProvider(provider),
    server.WithAuthenticatedExtendedCardHandler(func(ctx context.Context, base server.AgentCard) (server.AgentCard, error) {
        base.Skills = append(base.Skills, privateSkill)
        return base, nil
    }),
)
```

客户端用 `GetAuthenticatedExtendedCard` 获取；底层 wire 方法是 `GetExtendedAgentCard`。→ [examples/auth](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/auth)。

## 推送通知

用于离线运行:客户端注册 webhook,框架随任务进展**自动**回调它。投递是 opt-in 的——给 TaskManager 一个 `push.Sender`,框架会把每个任务事件的 `StreamResponse` POST 到该任务注册的每个 webhook；需要过滤或攒批时，用自定义 `push.Sender` 显式实现策略。

```go
// SignedSender 默认生成临时签名 key；生产多副本用
// pushauth.WithJWTKey(key, kid) 共享同一把私钥。
sender, _ := pushauth.NewSignedSender()

// TaskManager:Sender 开启自动投递;server 从它发现 push 能力并在 card 上声明。
tm, _ := memory.NewTaskManager(processor,
    memory.WithPushNotifications(sender),
)

// Server:在 JWKS endpoint 发布 sender 的验证公钥。
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
    server.WithPushNotificationJWKSHandler(sender.JWKSHandler()))
```

客户端经 `CreateTaskPushNotificationConfig` 注册配置(用 `Get` / `List` / `DeleteTaskPushNotificationConfig` 管理；Get/Delete 同时使用 task ID 和 config ID 定位)。**不注入 sender = 不支持 push**:config RPC 返回 `-32003 PushNotificationNotSupported`(与官方 SDK 一致)。想保留注册、由 agent 自己掌控投递,用 `WithPushConfig(push.Config{Sender: sender, ManualDelivery: true})`——自动投递关闭,注册/JWKS 发现/能力声明照常;要过滤/攒批,自定义 `push.Sender` 持有并委托给 `SignedSender` 即可，无需嵌入。自动投递使用有界的进程内队列：同一配置保持顺序，队列满时对任务事件施加背压，但进程崩溃后不提供 durable outbox 保证；需要该保证时使用手动投递并接入持久化队列。请求内联的 `configuration.taskPushNotificationConfig` 同样视为注册:未启用时拒绝；启用后，仅在本轮真正创建 task 时落库并自动投递，纯 message 结果不会留下孤儿配置；它也照旧作为 `ec.PushConfig` 传给 processor。自定义 header / tracing:`push.WithRequestDecorator`。→ [examples/jwks](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/jwks)。

`SignedSender` 会原样遵循客户端声明的静态凭据（例如 Basic 或 Bearer），仅当客户端完全省略 `Authentication` 时才使用 JWT 身份。若客户端声明了 scheme 但凭据需要动态获取，应使用 `push.NewHTTPSender(push.WithAuthorizationHeader(...))` 解析，框架不会静默用 JWT 覆盖。`HTTPSender` 默认拒绝私网/特殊用途地址和重定向；确实需要可信私网回调时，必须显式传 `push.WithUnsafeAllowPrivateNetworks()`。如果回调无需签名，使用 `push.NewHTTPSender()` 并省略 `WithPushNotificationJWKSHandler`。

## 多租户托管

一个进程可承载多个 agent。A2A v1.0 的多 agent 路由不靠 URL path，而靠请求体里的 `tenant` 字段；该值会进入 `ec.Tenant`。每个租户的 agent card 通过 `WithTenantCard` 或 `WithTenantCardProvider` 提供，客户端用 `/.well-known/agent-card.json?tenant=<tenant>` 获取。

```go
srv, _ := server.NewA2AServer(tm,
    server.WithTenantCard("weather", weatherCard),
    server.WithTenantCard("billing", billingCard),
)
// 在 ProcessMessage 里:switch ec.Tenant { … }
```

纯多租户 server 可以不提供默认 `WithAgentCard`；此时不带 `?tenant=` 获取 card 会返回 404。若需要一个目录型默认 card，再额外传 `WithAgentCard(card)`。

→ [examples/tenant](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/tenant)。

## 子路径部署

把整个 server 挂到路径前缀下，比如放在网关后面。推荐显式使用 `WithBasePath`，它会同时调整 agent card、JSON-RPC 和 JWKS endpoint：

```go
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
    server.WithBasePath("/api/v1/agent"))   // card + JSON-RPC 位于 /api/v1/agent/…
```

如果没有设置 `WithBasePath`，server 会尝试从 `agentCard.URL` 的 path 自动推导 base path；显式 `WithBasePath` 的优先级更高，适合外部 URL 和内部路由不一致的网关场景。

→ [examples/subpath](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/subpath)。

## 服务 legacy v0.2.x 客户端

让未修改的 v0.2.x 客户端在迁移期间继续工作:把 `compat/v0` 挂到同一端点。legacy 斜杠方法名与 v1.0 的 PascalCase 名不相交,所以一个端点同时分发两代——在同一鉴权链内、对同一个 `TaskManager`,并保留老默认(尤其非阻塞的 `message/send`)。

```go
import v0 "trpc.group/trpc-go/trpc-a2a-go/v2/compat/v0"

srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
    server.WithCompatHandler(v0.NewJSONRPCHandler(tm)))
```

→ [examples/compat](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/compat)。

## 遥测

server 通过 OpenTelemetry 记录请求数、请求耗时和流式首 token 时延（TTFT）。你可以注入已有 meter provider，也可以让 server 根据 OTLP option 创建 provider：

```go
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
    server.WithTelemetryMeterProvider(meterProvider),
)

srv, _ = server.NewA2AServer(tm, server.WithAgentCard(card),
    server.WithTelemetryMeterProviderOptions(
        metrics.WithEndpoint("localhost:4317"),
        metrics.WithServiceName("my-a2a-server"),
    ),
)
```

当“首 token”对你的 agent 不是第一帧时，自定义策略：

```go
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
    server.WithFirstTokenPolicy(myFirstTokenPolicy),
)
```

## 能力状态

生产部署时可以按下面选择：

| 需求 | 推荐配置 | 注意事项 |
| --- | --- | --- |
| 本地开发、单进程 demo | `taskmanager/memory` | 默认终态任务永久保留；生产请设置 `memory.WithTaskTTL`。 |
| 重启后保留任务、多个进程共享快照 | `taskmanager/redis` | 实时 SSE 事件扇出仍是进程内；断线后用快照恢复。 |
| 用户在线等结果 | `SendMessage` 或 `SendStreamingMessage` | v1.0 `SendMessage` 默认阻塞。 |
| 用户离线等待回调 | push notification + JWKS | 需要 agent card 声明 push 能力，服务端负责发送 webhook。 |
| 一个 agent 一个进程 | `WithAgentCard` | 最简单，card 直接代表该 agent。 |
| 一个进程多个 agent | `WithTenantCard` / `WithTenantCardProvider` | 请求体带 `tenant`，card 用 `?tenant=` 获取。 |
| 网关或统一前缀部署 | `WithBasePath` | 显式配置优先于从 `agentCard.URL` 推导。 |
| 兼容老客户端 | `WithCompatHandler(v0.NewJSONRPCHandler(tm))` | 必须挂在 server 内部，才能共享鉴权链。 |
| 指标与 TTFT | `WithTelemetryMeterProvider` 或 `WithTelemetryMeterProviderOptions` | `WithFirstTokenPolicy` 可调整首 token 判定。 |

当前框架服务 **JSON-RPC** 传输绑定。A2A v1.0 spec 还定义了 **gRPC** 与 **HTTP+JSON（REST）** 绑定，当前尚未实现。见 [框架概览](overview.md)。
