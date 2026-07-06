# 调用 Agent（客户端）

客户端:如何调用一个 A2A agent——四种消费模式、任务管理、鉴权,以及从一个 agent
内部调用别的 agent。构建 agent 见 [服务端](server.md)。

```go
import "trpc.group/trpc-go/trpc-a2a-go/v2/client"

c, _ := client.NewA2AClient("http://localhost:8080/")
```

`NewA2AClient` 接收 option(超时、HTTP client、鉴权——见下)。它经 JSON-RPC 绑定
与 agent 通信。

## 四种消费模式

一个 agent,四种消费方式——都出自同一个 `SendMessage` 请求形状:

```go
params := protocol.SendMessageParams{
    Message: protocol.Message{
        Role:  protocol.MessageRoleUser,
        Parts: []*protocol.Part{protocol.NewTextPart("hello")},
    },
}
```

**1. 阻塞 send(默认)**——一次调用,等该轮结束,返回最终的 `Task` 或 `Message`
(sealed union):

```go
resp, _ := c.SendMessage(ctx, params)
if task := resp.GetTask(); task != nil {
    // completed / failed / input-required 任务，带其 artifacts
} else if msg := resp.GetMessage(); msg != nil {
    // 纯消息回复（没有创建任务）
}
```

**2. `returnImmediately`**——以最早可用的结果应答,工作继续;之后用 `GetTasks`
或 `ResubscribeTask` 跟进:

```go
t := true
params.Configuration = &protocol.SendMessageConfiguration{ReturnImmediately: &t}
resp, _ := c.SendMessage(ctx, params)   // 首个任务快照或首条消息
```

**3. 流式**——每个事件经 SSE 实时到达:

```go
events, _ := c.StreamMessage(ctx, params)
for event := range events {
    switch {
    case event.GetStatusUpdate() != nil:
        // 状态帧
    case event.GetArtifactUpdate() != nil:
        // artifact 分块
    case event.GetMessage() != nil:
        // 消息
    }
} // 任务到达终态（或中断态）时 channel 关闭
```

**4. Resubscribe**——断连后接回运行中的任务;首帧是当前任务快照,之后是实时
增量:

```go
events, _ := c.ResubscribeTask(ctx, protocol.TaskIDParams{ID: taskID})
```

三种 send 模式由
[examples/simple 的 client](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/simple)
一并演示。

## 任务管理

```go
task, _  := c.GetTasks(ctx, protocol.TaskQueryParams{ID: taskID})            // 快照
list, _  := c.ListTasks(ctx, protocol.ListTasksParams{ContextID: contextID}) // 过滤 + 分页
task, _  = c.CancelTasks(ctx, protocol.TaskIDParams{ID: taskID})             // 请求取消
```

`GetTasks` 可带可选的 `HistoryLength` 决定随附多少会话历史。取消一个已结束的任务
返回 `-32002`(不可取消);返回的任务是取消请求时刻的快照——见
[behavior.md](behavior.md)。

## 多轮续跑

当一次调用返回 `input-required`(或 `auth-required`)的任务,回传**相同的
`taskId`** 发另一条消息来恢复它:

```go
follow := protocol.SendMessageParams{
    Message: protocol.Message{
        Role:   protocol.MessageRoleUser,
        TaskID: &taskID,   // 续跑等待中的任务——不是新任务
        Parts:  []*protocol.Part{protocol.NewTextPart("March 3rd")},
    },
}
resp, _ := c.SendMessage(ctx, follow)
```

**不带** `taskId` 的后续消息会开一个全新任务,把挂起的那个晾在那里。

## 鉴权

按 agent card 公示的方案附带凭据。服务端一侧见
[服务端](server.md#鉴权)。

```go
c, _ := client.NewA2AClient("http://localhost:8080/",
    client.WithJWTAuth(secret, audience, issuer, time.Hour),
)
```

另有 `client.WithAPIKeyAuth`、`WithOAuth2ClientCredentials`、
`WithOAuth2TokenSource`、`WithAuthProvider`。JWT、API key、OAuth2 的完整客户端
接法:
→ [examples/auth](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/auth)。

## 从一个 agent 内部调用别的 agent（编排）

一个 agent 可以经这个同样的 client 调用别的 agent,在自己的 `ProcessMessage` 内
部调用:根 agent 把工作分发给专家 agent,再把它们的结果聚合进自己的任务。注意出
站的 `SendMessage` 会阻塞到子 agent 那轮结束(v1.0 默认),这通常正是编排器想要
的。

```go
func (p *root) ProcessMessage(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
    h := taskmanager.NewTaskHandle(ctx, ec)
    defer h.Close()
    h.UpdateTaskState(protocol.TaskStateWorking, nil)
    sub, _ := p.weatherClient.SendMessage(ctx, forward(ec.Message))   // 调用另一个 agent
    h.UpdateTaskState(protocol.TaskStateCompleted, extractReply(sub))
    return h.Events(), nil
}
```

→ [examples/multi](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/multi)。

## legacy v0.2.x wire

要讲 legacy wire(对一个 v0.2.x server,或挂了 compat handler 的 v1.0 server),
用 `compat/v0` 客户端——它接收同样的 v1 类型,底层转换:

```go
import v0 "trpc.group/trpc-go/trpc-a2a-go/v2/compat/v0"

lc, _ := v0.NewClient("http://localhost:8080/")
resp, _ := lc.SendMessage(ctx, params)   // 保留 v0 的非阻塞默认
```

→ [examples/compat](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/compat)。
