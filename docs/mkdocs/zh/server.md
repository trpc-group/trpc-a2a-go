# 构建 Agent（服务端）

服务端:如何起一个 A2A server、把你的 agent 定义为 `MessageProcessor`、选存储后
端、打开框架的各项服务端能力。调用 agent 见 [客户端](client.md);这些 API 依赖
的运行时规则见下文的[轮次生命周期](#轮次生命周期)等几节。

```bash
go get trpc.group/trpc-go/trpc-a2a-go/v2
```

## server 的三个部分

一个 server 绑定 **agent card**(身份 + 能力)、**TaskManager**(状态)、你的
**MessageProcessor**(逻辑):

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
| `WithAgentCard(card)` | 公开 agent card(单 agent server)。 |
| `WithTenantCard(tenant, card)` / `WithTenantCardProvider(fn)` | 按租户 card(多租户,见下)。 |
| `WithAuthProvider(p)` | 要求鉴权(见下)。 |
| `WithJWKSEndpoint(enabled, path)` + `WithPushNotificationAuthenticator(a)` | 签名推送通知、发布公钥。 |
| `WithBasePath(prefix)` | 挂载到子路径。 |
| `WithCompatHandler(h)` | 同时服务 legacy v0.2.x wire。 |
| `WithMiddleware(mw...)` | 包裹 HTTP handler 链。 |
| `WithCORSEnabled(true)` | 输出 CORS 头。 |
| `WithReadTimeout` / `WithWriteTimeout` / `WithIdleTimeout` | HTTP server 超时。 |
| `WithTelemetryMeterProvider(mp)` / `WithFirstTokenPolicy(p)` | 指标 + TTFT。 |

用 `srv.Stop(ctx)` 停机,它会先排空在途轮次再关闭。

## 定义 MessageProcessor

你的 agent 就是一个方法:

```go
type MessageProcessor interface {
    ProcessMessage(ctx context.Context, ec *ExecContext) (<-chan protocol.StreamEvent, error)
}
```

你从一份只读快照读入请求,返回一个事件 channel——任务的事件日志。框架用这一个
方法服务 `SendMessage`(一元)与 `SendStreamingMessage`(流式);你的代码里没有
流式/非流式分支。

`ExecContext` 携带你应答所需的一切:`Message`(收到的消息)、`TaskID`(预分
配)、`Task`(续跑轮的当前快照,首轮为 `nil`)、`ContextID`、`Tenant`、`History`
(会话快照)、`AcceptedOutputModes`、`PushConfig`(客户端内联的 webhook 配置,
若有)。

推荐包一个 **`TaskHandle`**——一个小的辅助函数,承载熟悉的动词
(`UpdateTaskState`、`AddArtifact`、`Reply`)并把要返回的 channel 交给你。同步
函数体原样可用;`Events()` 之前的 emit 永不阻塞,不需要 goroutine。
→ [examples/basic](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/basic)

```go
func (p *proc) ProcessMessage(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
    h := taskmanager.NewTaskHandle(ctx, ec)
    defer h.Close()
    h.UpdateTaskState(protocol.TaskStateWorking, nil)
    result := doWork(ec.Message)
    h.AddArtifact(result.Artifact, true)                              // lastChunk = true
    h.UpdateTaskState(protocol.TaskStateCompleted, taskmanager.ReplyText("done"))
    return h.Events(), nil
}
```

动词:`UpdateTaskState(state, message)`、`AddArtifact(artifact, lastChunk)`、
`Reply(message)`,以及读取 `TaskID()`、`GetContextID()`、`GetTask()`、
`GetMessageHistory()`。`taskmanager.ReplyText(text)` 构造一条 agent 消息。

**`TaskHandle` 底层就是 channel 操作。** 真正的契约是你返回的
`<-chan protocol.StreamEvent`:`UpdateTaskState` 发一个
`*protocol.TaskStatusUpdateEvent`,`AddArtifact` 发一个
`*protocol.TaskArtifactUpdateEvent`,`Reply` 发一个 `*protocol.Message`,
`Close` 关闭 channel。你很少需要,但可以自己构造并发送这些事件——这也是够到
`TaskHandle` 不暴露的字段的唯一办法,比如分块流式的 artifact `Append` 标志:

```go
out := make(chan protocol.StreamEvent, 4)
go func() {
    defer close(out)
    out <- &protocol.TaskStatusUpdateEvent{Status: protocol.TaskStatus{State: protocol.TaskStateWorking}}
    out <- &protocol.TaskArtifactUpdateEvent{Artifact: art, Append: &appendFlag, LastChunk: &done}
    out <- &protocol.TaskStatusUpdateEvent{Status: protocol.TaskStatus{State: protocol.TaskStateCompleted}}
}()
return out, nil
```

（[examples/simple](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/simple)
是一个完全用裸 channel 写的 processor。）

### 常见形态

- **纯回复**(不产生任务):`h.Reply(taskmanager.ReplyText("..."))`。
- **实时流式**——把函数体放进 goroutine,事件就会实时到达 `SendStreamingMessage`
  的消费者;长循环里检查 `ctx.Err()`,被取消时直接关闭(框架落 `CANCELED`)。
  → [examples/streaming](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/streaming)
- **多轮**——用
  `h.UpdateTaskState(protocol.TaskStateInputRequired, taskmanager.ReplyText("need more"))`
  挂起并关闭;后续消息(回传 `taskId`)作为新一轮到来,此时 `ec.Task` 已就位。

### 使用约束

- channel/handle 由发事件的 goroutine 负责关闭——轮次只在关闭时结束,永不关闭的
  轮次会钉死任务并阻塞停机。
- 每轮以终态或挂起态收尾;在 `working` 关闭会把任务打成 `FAILED`。
- 一轮只驱动一个任务;永不发 `*protocol.Task`。
- 需要跨轮记住的内容必须作为 `Message` 事件发出(status message 是易失的;
  artifact 永不进入历史)。

## 轮次生命周期

一个**轮次**是一次 `ProcessMessage` 调用及其 channel 的排空过程。以下是你的
agent 代码所遵循、客户端所观察到的确切语义。

- **懒创建**——首个*任务事件*落库时任务才成立。只发 `Message` 的轮次不留任务
  (对该轮预分配 ID 的 `GetTask` 返回 not-found)。
- **单活跃 run**——上一轮还在跑时,同一任务的第二条消息被拒
  (`-32602`,"already has an active execution")。
- **关 channel 才是轮次结束**——且只有这一种结束方式。关闭时应用 close 规则:

  | 关闭时的任务状态 | 结果 |
  | --- | --- |
  | 终态(你发过了) | 正常结束 |
  | `input-required` / `auth-required` | 任务**挂起**,等待后续 |
  | `submitted` / `working` | **`FAILED`**——"processor finished without terminal state"(bug 信号) |
  | 期间收到过取消请求 | **`CANCELED`** |

- **挂起即让出**——发出 `input-required`/`auth-required` 后任务立刻被释放以便续
  跑开始;老轮之后再发的事件一律丢弃。完成要由续跑轮交付。
- **违规立即失败**——给别的 `taskId` 发事件、发 `*protocol.Task` 快照
  (v1.0 中仅框架可产生)、发无状态的 status,都会把该轮任务打成 `FAILED` 并丢弃
  其余事件。

## 执行与取消

- **分离的 context**——轮次跑在与请求分离的 context 上:client 断连**不会**取消
  工作;结果始终可经 `GetTask`/`SubscribeToTask` 取回。
- **谁能取消**——只有 `CancelTask`(和 manager 停机)会取消 processor 的 `ctx`。
  得体的反应是停止发送并关闭——框架落 `CANCELED`。取消*之后*发出的终态事件依然
  生效(允许收尾的轮次就让它收尾)。
- **取消的返回值**——`CancelTask` 返回取消请求时刻的快照(可能仍是 `working`);
  终态 `CANCELED` 要等该轮收尾时才落库。取消已终态的任务返回 `-32002`。

## 响应生成

四种调用方式各自这样得到答案:

- **`SendMessage`(阻塞,默认)**等该轮结束:碰过任务就答**任务快照**,否则答
  **最后一条 Message**;一个事件都没发的轮次是 processor 的 bug(`-32603`)。
- **`SendMessage` + `returnImmediately=true`** 以**最早可用结果**应答:首个已落
  库的任务快照或首条 Message(它不带 `taskId`——client 若需追踪任务,先发一个任
  务事件)。
- **`SendStreamingMessage`** 按序转发每个事件,且**先持久化再投递**。流在终态或
  挂起帧结束。
- **`SubscribeToTask`** 先发当前任务快照、再发实时增量;终态任务被拒。

## 会话、历史,以及什么会被记住

存储是两级的:**按 `messageId` 存消息本体**,按 `contextId` 存会话索引。进入会
话的只有:每轮的请求消息,以及 processor 发出的每个 **`Message` 事件**——没有别
的。

> **status message 是易失的**(被下一个 status 覆盖、从不入库),**artifact 永
> 不进入历史**。任何需要跨轮记住的内容——尤其是 LLM 的最终回答——必须作为
> `Message` 事件发出,否则下一轮的 `ec.History` 只有用户的发言。(这一点与官方
> a2a SDK 不同,后者会把每条 `status.message` 滚入 `task.history`。)

`Task.history` 是虚拟的:响应时按请求的 `historyLength` 从会话现算。`ec.History`
是轮次开始前的快照,按 `MaxHistoryLength`(默认 100)截断。

请求 `configuration` 字段:`returnImmediately` 与 `historyLength` 由框架消费;
`acceptedOutputModes`(`ec.AcceptedOutputModes`)与 `taskPushNotificationConfig`
(`ec.PushConfig`)透传给你的 processor。

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

→ [examples/redis](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/redis)。
实现 `taskmanager.TaskManager` 接口即可自带后端。

留存:

| | memory 后端 | redis 后端 |
| --- | --- | --- |
| 会话 | 闲置超 `ConversationTTL`(默认 1h)即清理;单会话上限 `MaxHistoryLength` | key TTL(默认 1h),写时续期 |
| 终态任务 | **默认永久保留**(`TaskTTL`=0)——生产环境请设置 `memory.WithTaskTTL` | key TTL(默认 1h,`WithExpireTime`) |
| 挂起任务 | 永不回收(清理器只收终态)——请让 client 恢复或取消它们 | 随 key TTL 过期 |

没有按任务的删除 API;A2A 未定义此类接口。Redis 后端上,实时事件扇出是进程内的
(快照共享)。

## 鉴权

在服务端要求鉴权;三种方案,可链式组合。客户端一侧见
[客户端](client.md#鉴权)。

```go
provider := auth.NewChainAuthProvider(
    auth.NewJWTAuthProvider(secret, audience, issuer, time.Hour),
    auth.NewAPIKeyAuthProvider(keyMap, "X-API-Key"),
    auth.NewOAuth2AuthProviderWithConfig(oauth2Config, userInfoURL, userIDField),
)
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card), server.WithAuthProvider(provider))
```

card 的 `securitySchemes` 公示服务端接受什么。
→ [examples/auth](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/auth)。

## 推送通知

用于离线运行:客户端注册 webhook,服务端随任务进展回调它。框架用 JWT 签名每次
回调,并在 JWKS 端点发布校验公钥。

```go
authr := auth.NewPushNotificationAuthenticator()
authr.GenerateKeyPair()
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
    server.WithJWKSEndpoint(true, "/.well-known/jwks.json"),
    server.WithPushNotificationAuthenticator(authr),
)
```

配置经 `CreateTaskPushNotificationConfig` 入库(或请求内联的配置,作为
`ec.PushConfig` 到达你的 processor);发送方用 `OnPushNotificationGet` 解析并
POST 签名过的 payload。
→ [examples/jwks](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/jwks)。

## 多租户托管

一个进程可承载多个 agent。请求的租户经 `ec.Tenant` 到达;按租户的 card 用
`WithTenantCard` 提供。

```go
srv, _ := server.NewA2AServer(tm,
    server.WithTenantCard("weather", weatherCard),
    server.WithTenantCard("billing", billingCard),
)
// 在 ProcessMessage 里:switch ec.Tenant { … }
```

→ [examples/tenant](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/tenant)。

## 子路径部署

把整个 server 挂到路径前缀下(比如放在网关后面)。路径写进 agent card 的 URL 和
`WithBasePath`:

```go
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
    server.WithBasePath("/api/v1/agent"))   // card + JSON-RPC 位于 /api/v1/agent/…
```

→ [examples/subpath](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/subpath)。

## 服务 legacy v0.2.x 客户端

让未修改的 v0.2.x 客户端在迁移期间继续工作:把 `compat/v0` 挂到同一端点。legacy
斜杠方法名与 v1.0 的 PascalCase 名不相交,所以一个端点同时分发两代——在同一鉴权
链内、对同一个 `TaskManager`,并保留老默认(尤其非阻塞的 `message/send`)。

```go
import v0 "trpc.group/trpc-go/trpc-a2a-go/v2/compat/v0"

srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
    server.WithCompatHandler(v0.NewJSONRPCHandler(tm)))
```

→ [examples/compat](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/compat)。

## 遥测

server 通过你提供的 meter provider 记录 OpenTelemetry 指标,包括流式响应的
首 token 时延(TTFT)。当"首 token"对你的 agent 不是第一帧时,自定义策略:

```go
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
    server.WithTelemetryMeterProvider(meterProvider),
    server.WithFirstTokenPolicy(myFirstTokenPolicy),
)
```

## 能力状态

本框架当前服务 **JSON-RPC** 传输绑定。spec 还定义了 **gRPC** 与
**HTTP+JSON(REST)** 绑定——规划中、尚未实现。见 [框架概览](overview.md#你能得到什么)
的能力矩阵。
