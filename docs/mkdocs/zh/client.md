# 调用 Agent（客户端）

本页讲如何从 Go 程序调用一个 A2A agent：发现 agent、发送消息、消费流式事件、管理任务、注册推送通知，以及在一个 agent 内部编排调用其他 agent。构建服务端见 [服务端](server.md)，协议对象与状态机见 [协议](protocol.md)。

```go
import (
    "trpc.group/trpc-go/trpc-a2a-go/v2/client"
    "trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

c, err := client.NewA2AClient("http://localhost:8080/")
if err != nil {
    return err
}
```

`NewA2AClient` 接收 agent endpoint，默认继续使用 JSON-RPC。直接指定 HTTP+JSON 时传 `client.WithProtocolBinding(protocol.ProtocolBindingHTTPJSON)`：

```go
restClient, _ := client.NewA2AClient(
    "https://agent.example.com/a2a",
    client.WithProtocolBinding(protocol.ProtocolBindingHTTPJSON),
)
```

从 Agent Card 选择 interface 时，把它的三个属性一起传给直接构造函数。`WithTenant` 会把所选 interface 的 tenant 自动放进每个请求，并拒绝请求中冲突的 tenant：

```go
iface := card.SupportedInterfaces[0] // 调用方选出的第一个兼容 interface
selectedClient, _ := client.NewA2AClient(
    iface.URL,
    client.WithProtocolBinding(iface.ProtocolBinding),
    client.WithTenant(iface.Tenant),
)
```

JSON-RPC 请求继续使用 `application/json`。HTTP+JSON 使用 REST 路由并发送 `application/a2a+json`；一元响应同时接受 `application/a2a+json` 与兼容 1.0.0 实现的 `application/json`。默认没有超时；生产代码通常会传 `client.WithTimeout(...)` 或自定义 `http.Client`。

## 先发现 Agent

A2A client 通常先取 agent card，确认这个 agent 是谁、支持哪些技能、是否支持 streaming / push notification / extended card，以及应该用哪个 endpoint。

```go
card, err := c.GetAgentCard(ctx, "")
if err != nil {
    return err
}
fmt.Println(card.Name, card.Skills)
```

`GetAgentCard(ctx, "")` 会先请求 `/.well-known/agent-card.json`，失败后回退到 legacy `/.well-known/agent.json`。如果 agent 部署在子路径，可以传相对路径；如果 card 托管在独立地址，可以传绝对 URL。

```go
card, _ = c.GetAgentCard(ctx, "/api/v1/agent")
card, _ = c.GetAgentCard(ctx, "https://example.com/cards/weather.json")
```

如果公开 card 声明了 `capabilities.extendedAgentCard=true`，鉴权后可以取扩展 card。Go 方法名是 `GetAuthenticatedExtendedCard`，底层调用 A2A v1.0 的 `GetExtendedAgentCard`。

```go
extended, err := c.GetAuthenticatedExtendedCard(ctx)
```

## 发送一条消息

所有调用都从同一个请求形状开始：

```go
params := protocol.SendMessageParams{
    Message: protocol.NewMessage(
        protocol.MessageRoleUser,
        []*protocol.Part{protocol.NewTextPart("hello")},
    ),
}
```

需要保持会话上下文时，带上 `contextId`：

```go
contextID := "order-123"
params.Message.ContextID = &contextID
```

服务端如果是多租户托管，额外设置 `Tenant`：

```go
params.Tenant = "weather"
```

## 四种消费模式

同一个 agent 可以按四种方式消费，区别只在 client 如何等待和跟进。

### 1. 阻塞 send（默认）

`SendMessage` 默认等到本轮到达终态或挂起态，再返回最终 `Task` 或直接 `Message`。

```go
resp, err := c.SendMessage(ctx, params)
if err != nil {
    return err
}

if task := resp.GetTask(); task != nil {
    // 有任务生命周期：读 task.Status、task.Artifacts、task.History。
} else if msg := resp.GetMessage(); msg != nil {
    // 纯消息回复：本轮没有创建任务。
}
```

适合短任务、编排器调用下游 agent 后等待结果、以及命令行工具这类同步场景。

### 2. 立即返回，稍后跟进

如果不想等任务结束，设置 `returnImmediately=true`。服务端会在最早可用结果出现时返回，任务继续在服务端运行。

```go
immediate := true
params.Configuration = &protocol.SendMessageConfiguration{
    ReturnImmediately: &immediate,
}

resp, err := c.SendMessage(ctx, params)
```

如果返回的是任务快照，保存 `task.ID`，之后用 `GetTasks` 轮询或用 `ResubscribeTask` 接回事件流。

### 3. 实时流式

`StreamMessage` 建立 SSE 连接，每个状态事件、artifact 分块或消息都会实时到达。

```go
events, err := c.StreamMessage(ctx, params)
if err != nil {
    return err
}

for event := range events {
    switch {
    case event.GetStatusUpdate() != nil:
        // 状态更新：working、completed、input-required 等。
    case event.GetArtifactUpdate() != nil:
        // artifact 分块：注意 append / lastChunk。
    case event.GetMessage() != nil:
        // 直接消息回复。
    }
}
```

当任务到达终态或挂起态时，SSE 流会关闭。

### 4. 断线后重新订阅

对仍在运行的任务，`ResubscribeTask` 会先返回当前任务快照，再返回后续实时增量。它对应协议方法 `SubscribeToTask`。

```go
events, err := c.ResubscribeTask(ctx, protocol.TaskIDParams{ID: taskID})
```

终态任务不能订阅；已经结束的任务请用 `GetTasks` 读取快照。

## 任务管理

```go
task, err := c.GetTasks(ctx, protocol.TaskQueryParams{
    ID: taskID,
})

list, err := c.ListTasks(ctx, protocol.ListTasksParams{
    ContextID: contextID,
})

task, err = c.CancelTasks(ctx, protocol.TaskIDParams{
    ID: taskID,
})
```

`GetTasks` 可带 `HistoryLength` 控制响应里附带多少会话历史。`ListTasks` 可按 `ContextID`、`Status`、分页参数和时间过滤。`CancelTasks` 返回的是取消请求时刻的任务快照，可能仍是 `TASK_STATE_WORKING`；最终的 `TASK_STATE_CANCELED` 要等 processor 收尾后落库。

## 多轮续跑

当任务停在 `TASK_STATE_INPUT_REQUIRED` 或 `TASK_STATE_AUTH_REQUIRED`，下一条消息必须带相同 `taskId`，这样服务端才知道是在恢复原任务，而不是新建一个任务。

```go
follow := protocol.SendMessageParams{
    Message: protocol.NewMessageWithContext(
        protocol.MessageRoleUser,
        []*protocol.Part{protocol.NewTextPart("3 月 3 日")},
        &taskID,
        &contextID,
    ),
}

resp, err := c.SendMessage(ctx, follow)
```

只带 `contextId` 不够；不带 `taskId` 的后续消息会开启新任务，原来的挂起任务仍然挂起。

## 推送通知

如果 agent card 声明支持 push notification，client 可以为任务注册 webhook。服务端会在任务进展时回调该 URL；服务端侧的 JWT 签名与 JWKS 发布见 [服务端](server.md)。

```go
cfg, err := c.SetPushNotification(ctx, protocol.TaskPushNotificationConfig{
    TaskID: taskID,
    ID:     "default",
    URL:    "https://client.example.com/a2a/push",
    Token:  "opaque-correlation-token",
})

cfg, err = c.GetPushNotification(ctx, protocol.TaskIDParams{ID: taskID})

configs, err := c.ListPushNotifications(ctx,
    protocol.ListTaskPushNotificationConfigsParams{TaskID: taskID},
)

err = c.DeletePushNotification(ctx,
    protocol.DeleteTaskPushNotificationConfigParams{TaskID: taskID, ID: "default"},
)
```

也可以在 `SendMessageConfiguration.PushConfig` 内联传入一次性 push 配置。框架会把它透传给服务端 processor 的 `ec.PushConfig`；是否兑现或注册，由服务端实现决定。

## 鉴权与请求头

按 agent card 公示的 `securitySchemes` 附带凭据。内置 client option 覆盖 JWT、API key、OAuth2，也可以接入自定义 provider。

```go
c, err := client.NewA2AClient("https://agent.example.com/",
    client.WithTimeout(30*time.Second),
    client.WithJWTAuth(secret, audience, issuer, time.Hour),
)
```

常用 option：

| Option | 用途 |
| --- | --- |
| `WithTimeout` / `WithHTTPClient` | 控制 HTTP client 与请求超时。 |
| `WithJWTAuth` | 使用 JWT 鉴权。 |
| `WithAPIKeyAuth` | 在指定 header 中发送 API key。 |
| `WithOAuth2ClientCredentials` / `WithOAuth2TokenSource` | 使用 OAuth2。 |
| `WithAuthProvider` | 自定义鉴权 provider。 |
| `WithUserAgent` | 设置 User-Agent。 |
| `WithChannelSize` / `WithBuffer` | 调整 SSE 读取缓冲。 |

单次请求可以追加 header，例如 trace ID 或一次性鉴权信息：

```go
resp, err := c.SendMessage(ctx, params,
    client.WithRequestHeader("X-Trace-ID", traceID),
)
```

## 从一个 Agent 内部调用别的 Agent

一个 root agent 可以在自己的 `ProcessMessage` 中使用同一个 client 调用下游 agent，再把结果汇总成自己的任务事件。由于 v1.0 `SendMessage` 默认阻塞，编排器通常不用额外轮询就能等到下游结果。

```go
func (p *root) ProcessMessage(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
    h := taskmanager.NewTaskHandle(ctx, ec)
    defer h.Close()

    h.UpdateTaskState(protocol.TaskStateWorking, nil)
    sub, err := p.weatherClient.SendMessage(ctx, forward(ec.Message))
    if err != nil {
        h.UpdateTaskState(protocol.TaskStateFailed, protocol.NewAgentText(err.Error()))
        return h.Events(), nil
    }
    h.UpdateTaskState(protocol.TaskStateCompleted, extractReply(sub))
    return h.Events(), nil
}
```

完整示例见 [`examples/multi`](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/multi)。

## Go API 与协议方法名

A2A v1.0 wire 使用 PascalCase JSON-RPC 方法名；Go client 保留了一些历史命名。看日志或抓包时，以右侧 wire 名为准。

| Go client 方法 | A2A v1.0 JSON-RPC 方法 |
| --- | --- |
| `SendMessage` | `SendMessage` |
| `StreamMessage` | `SendStreamingMessage` |
| `GetTasks` | `GetTask` |
| `ListTasks` | `ListTasks` |
| `CancelTasks` | `CancelTask` |
| `ResubscribeTask` | `SubscribeToTask` |
| `SetPushNotification` | `CreateTaskPushNotificationConfig` |
| `GetPushNotification` | `GetTaskPushNotificationConfig` |
| `ListPushNotifications` | `ListTaskPushNotificationConfigs` |
| `DeletePushNotification` | `DeleteTaskPushNotificationConfig` |
| `GetAgentCard` | HTTP GET `/.well-known/agent-card.json` |
| `GetAuthenticatedExtendedCard` | `GetExtendedAgentCard` |

## legacy v0.2.x wire

如果需要调用 v0.2.x server，或调用一个挂了 `compat/v0` handler 的 v1.0 server，可以使用 `compat/v0` client。它接收 v1 类型，底层转换成 legacy 斜杠方法名，并保留 v0 的非阻塞默认行为。

```go
import v0 "trpc.group/trpc-go/trpc-a2a-go/v2/compat/v0"

lc, err := v0.NewClient("http://localhost:8080/")
resp, err := lc.SendMessage(ctx, params)
```

完整示例见 [`examples/compat`](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/compat)。
