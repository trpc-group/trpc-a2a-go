# 用 tRPC-A2A-Go 构建 Agent

覆盖每项能力的配方，每条链接一个可运行示例。概念见 [protocol.md](protocol.md)
与 [behavior.md](behavior.md)；移植 v0.x agent 见 [从 v0.x 迁移](migration.md)。

```bash
go get trpc.group/trpc-go/trpc-a2a-go/v2
```

## 1. 服务端

一个 server 绑定三样东西：**agent card**（身份 + 能力）、**TaskManager**
（状态）、你的 **MessageProcessor**（逻辑）。

```go
import (
    "trpc.group/trpc-go/trpc-a2a-go/v2/server"
    "trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

tm, _ := memory.NewTaskManager(&myProcessor{})
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(agentCard))
srv.Start(":8080")            // 在 "/" 服务 JSON-RPC，在 /.well-known/agent-card.json 服务 card
```

`NewA2AServer` 接收函数式 option。你常用的：

| Option | 用途 |
| --- | --- |
| `WithAgentCard(card)` | 公开 agent card（单 agent server）。 |
| `WithTenantCard(tenant, card)` / `WithTenantCardProvider(fn)` | 按租户 card（多租户 server）。 |
| `WithAuthProvider(p)` | 要求鉴权（§5）。 |
| `WithJWKSEndpoint(enabled, path)` + `WithPushNotificationAuthenticator(a)` | 签名推送通知、发布公钥（§6）。 |
| `WithBasePath(prefix)` | 挂载到子路径（§8）。 |
| `WithCompatHandler(h)` | 同时服务 legacy v0.2.x wire（§9）。 |
| `WithMiddleware(mw...)` | 包裹 HTTP handler 链。 |
| `WithCORSEnabled(true)` | 输出 CORS 头。 |
| `WithReadTimeout` / `WithWriteTimeout` / `WithIdleTimeout` | HTTP server 超时。 |
| `WithTelemetryMeterProvider(mp)` / `WithFirstTokenPolicy(p)` | 指标 + TTFT（§10）。 |

用 `srv.Stop(ctx)` 停机，它会先排空在途轮次再关闭。

## 2. 写 processor

你的 agent 就是一个方法。读 [`ExecContext`](behavior.md)、发事件、关闭流。两种
编写风格产出同样的事件。

**`TaskHandle` 风格（推荐）。** 同步函数体原样可用——`Events()` 之前的 emit 永不
阻塞。
→ [examples/basic](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/basic)

```go
func (p *proc) ProcessMessage(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
    h := taskmanager.NewTaskHandle(ctx, ec)
    defer h.Close()
    h.UpdateTaskState(protocol.TaskStateWorking, nil)
    result := doWork(ec.Message)
    h.AddArtifact(result.Artifact, true)
    h.UpdateTaskState(protocol.TaskStateCompleted, taskmanager.ReplyText("done"))
    return h.Events(), nil
}
```

**裸 channel 风格。** 对每个 `protocol.StreamEvent` 完全可控——artifact-append
分块就需要它。
→ [examples/simple](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/simple)

```go
out := make(chan protocol.StreamEvent, 4)
go func() {
    defer close(out)
    out <- &protocol.TaskStatusUpdateEvent{Status: protocol.TaskStatus{State: protocol.TaskStateWorking}}
    // ... artifacts ...
}()
return out, nil
```

常见形态：

- **纯回复**（无任务）：`h.Reply(taskmanager.ReplyText("..."))`。
- **实时流式**：把函数体放进 goroutine，事件就会实时到达 `SendStreamingMessage`
  的消费者；长循环里检查 `ctx.Err()`，被取消时直接关闭——框架落 `CANCELED`。
  → [examples/streaming](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/streaming)
- **多轮**：用
  `h.UpdateTaskState(protocol.TaskStateInputRequired, taskmanager.ReplyText("need more"))`
  挂起并关闭；后续消息（回传 `taskId`）作为新一轮到来，此时 `ec.Task` 已就位。

保平安的规则：channel/handle 由发事件的 goroutine 负责关闭；每轮以终态或挂起态
收尾；一轮只驱动一个任务；永不发 `*protocol.Task`；需要跨轮记住的内容一律作为
`Message` 事件发出。

## 3. 存储后端

`TaskManager` 拥有任务与会话状态。内置两个后端。

**内存**——零依赖、单进程：

```go
tm, _ := memory.NewTaskManager(proc,
    memory.WithMaxHistoryLength(100),          // 会话上限
    memory.WithConversationTTL(time.Hour, 30*time.Second),
    memory.WithTaskTTL(time.Hour),             // 0 = 终态任务永久保留（默认）
)
```

**Redis**——跨进程共享、重启不丢：

```go
import redistm "trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/redis"

tm, _ := redistm.NewTaskManager(proc, redisClient,   // 注意参数序：(processor, client)
    redistm.WithExpireTime(time.Hour),                // key TTL
    redistm.WithMaxHistoryLength(100),
)
```

→ [examples/redis](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/redis)。
留存差异（内存 `TaskTTL` 默认*永久保留*；Redis 用 key TTL；挂起任务与跨副本的
注意点）见 [behavior.md](behavior.md)。实现 `taskmanager.TaskManager` 接口即可
自带后端。

## 4. 客户端与消费模式

```go
import "trpc.group/trpc-go/trpc-a2a-go/v2/client"

c, _ := client.NewA2AClient("http://localhost:8080/")
```

一份 processor，四种消费方式：

```go
// 1. 阻塞 send（默认）：一次调用，拿最终 Task-或-Message 结果。
resp, _ := c.SendMessage(ctx, params)

// 2. returnImmediately：先拿最早结果，之后轮询或 resubscribe。
t := true
params.Configuration = &protocol.SendMessageConfiguration{ReturnImmediately: &t}
resp, _ = c.SendMessage(ctx, params)

// 3. 流式：事件实时到达。
events, _ := c.StreamMessage(ctx, params)

// 4. Resubscribe：接回运行中的任务（先快照，后增量）。
events, _ = c.ResubscribeTask(ctx, protocol.TaskIDParams{ID: taskID})
```

另有 `c.GetTasks`、`c.ListTasks`、`c.CancelTasks`。三种消费模式由
[examples/simple 的 client](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/simple)
一并演示。

## 5. 鉴权

在服务端要求鉴权；客户端附带凭据。三种方案，可链式组合。

```go
// 服务端：接受多种方案中的任意一种。
provider := auth.NewChainAuthProvider(
    auth.NewJWTAuthProvider(secret, audience, issuer, time.Hour),
    auth.NewAPIKeyAuthProvider(keyMap, "X-API-Key"),
    auth.NewOAuth2AuthProviderWithConfig(oauth2Config, userInfoURL, userIDField),
)
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card), server.WithAuthProvider(provider))
```

agent card 的 `securitySchemes` 公示服务端接受什么；客户端按它选定的方案鉴权。
JWT、API key、OAuth2 的完整服务端 + 客户端接法：
→ [examples/auth](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/auth)。

## 6. 推送通知

用于离线运行：客户端注册一个 webhook，服务端随任务进展回调它。框架用 JWT 签名
每次回调，并在 JWKS 端点发布校验公钥。

```go
authr := auth.NewPushNotificationAuthenticator()
authr.GenerateKeyPair()
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
    server.WithJWKSEndpoint(true, "/.well-known/jwks.json"),
    server.WithPushNotificationAuthenticator(authr),
)
```

配置经 `CreateTaskPushNotificationConfig` 入库（或请求内联的
`pushNotificationConfig`，它作为 `ec.PushConfig` 到达你的 processor）；发送方用
`OnPushNotificationGet` 解析并 POST 签名过的 payload。端到端、含客户端经 JWKS
做 JWT 校验：
→ [examples/jwks](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/jwks)。

## 7. 多租户托管

一个进程可承载多个 agent。请求的租户经 `ec.Tenant` 到达；按租户的 card 用
`WithTenantCard` 提供。

```go
srv, _ := server.NewA2AServer(tm,
    server.WithTenantCard("weather", weatherCard),
    server.WithTenantCard("billing", billingCard),
)
// 在 ProcessMessage 里：switch ec.Tenant { … }
```

→ [examples/tenant](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/tenant)。

## 8. 子路径部署

把整个 server 挂到路径前缀下（比如放在网关后面）。路径写进 agent card 的 URL
和 `WithBasePath`：

```go
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
    server.WithBasePath("/api/v1/agent"))
// card + JSON-RPC 现在位于 /api/v1/agent/…
```

→ [examples/subpath](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/subpath)。

## 9. 服务 legacy v0.2.x 客户端

让未修改的 v0.2.x 客户端在迁移期间继续工作：把 `compat/v0` 挂到同一端点。legacy
斜杠方法名与 v1.0 的 PascalCase 名不相交，所以一个端点同时分发两代——在同一鉴权
链内、对同一个 `TaskManager`，并保留老默认（尤其非阻塞的 `message/send`）。

```go
import v0 "trpc.group/trpc-go/trpc-a2a-go/v2/compat/v0"

srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
    server.WithCompatHandler(v0.NewJSONRPCHandler(tm)))
```

→ [examples/compat](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/compat)。

## 10. 遥测

server 通过你提供的 meter provider 记录 OpenTelemetry 指标，包括流式响应的
首 token 时延（TTFT）。当"首 token"对你的 agent 不是第一帧时，自定义策略：

```go
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
    server.WithTelemetryMeterProvider(meterProvider),
    server.WithFirstTokenPolicy(myFirstTokenPolicy),
)
```

## 11. Agent 编排

一个 agent 可以经 A2A client 调用其他 agent——就是你独立使用的那个 client，在
`ProcessMessage` 内部调用。根 agent 把工作分发给专家 agent，再把它们的结果聚合
进自己的任务。
→ [examples/multi](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/multi)。

## 能力状态

本框架当前服务 **JSON-RPC** 传输绑定。spec 还定义了 **gRPC** 与
**HTTP+JSON（REST）** 绑定——它们在规划中、尚未实现；agent card 已经建模了多传输
声明，以备它们落地。Redis 后端上，实时事件扇出是进程内的（快照共享）；完整的跨
副本流式是规划中的补充。当前运行时限制见 [behavior.md](behavior.md)。
