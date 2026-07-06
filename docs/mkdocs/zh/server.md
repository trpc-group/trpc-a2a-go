# 构建 Agent（服务端）

服务端:如何起一个 A2A server、把你的 agent 定义为 `MessageProcessor`、选存储后
端、打开框架的各项服务端能力。调用 agent 见 [客户端](client.md);这些 API 背后
的运行时契约见 [behavior.md](behavior.md)。

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

两种编写风格,产出同一条事件流。

### 风格一 —— `TaskHandle`(推荐)

熟悉的动词 API。同步函数体原样可用——`Events()` 之前的 emit 永不阻塞,不需要
goroutine。
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

### 风格二 —— 裸 channel

自己构造 `protocol.StreamEvent` 并发送。对每个字段完全可控——artifact-append
分块就需要它(`TaskHandle` 不暴露 `Append` 标志)。
→ [examples/simple](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/simple)

```go
out := make(chan protocol.StreamEvent, 4)
go func() {
    defer close(out)
    out <- &protocol.TaskStatusUpdateEvent{Status: protocol.TaskStatus{State: protocol.TaskStateWorking}}
    out <- &protocol.TaskArtifactUpdateEvent{Artifact: art, LastChunk: &done}
    out <- &protocol.TaskStatusUpdateEvent{Status: protocol.TaskStatus{State: protocol.TaskStateCompleted}}
}()
return out, nil
```

### 常见形态

- **纯回复**(不产生任务):`h.Reply(taskmanager.ReplyText("..."))`。
- **实时流式**——把函数体放进 goroutine,事件就会实时到达 `SendStreamingMessage`
  的消费者;长循环里检查 `ctx.Err()`,被取消时直接关闭(框架落 `CANCELED`)。
  → [examples/streaming](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/streaming)
- **多轮**——用
  `h.UpdateTaskState(protocol.TaskStateInputRequired, taskmanager.ReplyText("need more"))`
  挂起并关闭;后续消息(回传 `taskId`)作为新一轮到来,此时 `ec.Task` 已就位。

### 保平安的规则

- channel/handle 由发事件的 goroutine 负责关闭——轮次只在关闭时结束,永不关闭的
  轮次会钉死任务并阻塞停机。
- 每轮以终态或挂起态收尾;在 `working` 关闭会把任务打成 `FAILED`。
- 一轮只驱动一个任务;永不发 `*protocol.Task`。
- 需要跨轮记住的内容必须作为 `Message` 事件发出(status message 是易失的;
  artifact 永不进入历史)。

完整契约见 [behavior.md](behavior.md)。

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
留存差异与 Redis 跨副本的注意点见 [behavior.md](behavior.md)。实现
`taskmanager.TaskManager` 接口即可自带后端。

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
