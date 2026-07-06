# 用 tRPC-A2A-Go 构建 Agent

常见任务的配方，每条链接一个可运行示例。概念见 [protocol.md](protocol.md) 与
[behavior.md](behavior.md)；移植 v0.x agent 见
[从 v0.x 迁移](migration.md)。

```bash
go get trpc.group/trpc-go/trpc-a2a-go/v2
```

## 一页起一个服务端

```go
import (
    "trpc.group/trpc-go/trpc-a2a-go/v2/server"
    "trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

tm, _ := memory.NewTaskManager(&myProcessor{})     // 你的 agent 逻辑
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(agentCard))
srv.Start(":8080")
```

agent card 在 `/.well-known/agent-card.json` 公示身份与能力。有意思的事都发生
在 processor 里。

## 写 processor —— 两种风格

**`TaskHandle` 风格**——熟悉的动词 API；同步函数体原样可用（`Events()` 之前的
emit 永不阻塞）。从这里开始，尤其是移植 v0.x 代码时。
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

**裸 channel 风格**——底层契约本身，事件的每个字段都可控（比如 artifact 的
`append` 分块就需要它）。
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

**实时流式**：同样的函数体放进 goroutine，事件就会实时到达 `SendStreamingMessage`
的消费者；长循环里检查 `ctx.Err()`，被取消时直接关闭即可。
→ [examples/streaming](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/streaming)

**纯回复**：`h.Reply(taskmanager.ReplyText("..."))` 直接应答，完全不创建任务。

**多轮**：用
`h.UpdateTaskState(protocol.TaskStateInputRequired, taskmanager.ReplyText("need more"))`
挂起并关闭；后续消息（必须回传 `taskId`）作为新一轮到来，此时 `ec.Task` 已就
位。→ [examples/basic](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/basic)（`multi` 命令）

保平安的规则：channel/handle 由发事件的 goroutine 负责关闭；每轮以终态或挂起
态收尾；一轮只驱动一个任务；永不发 `*protocol.Task`；需要跨轮记住的内容一律
作为 `Message` 事件发出。

## 客户端 —— 三种消费模式

一份 processor，三种消费方式（[examples/simple](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/simple)
的 client 全部演示）：

```go
c, _ := client.NewA2AClient("http://localhost:8080/")

// 1. 阻塞（默认）：一次调用拿最终结果。
resp, _ := c.SendMessage(ctx, params)                 // Task 或 Message union

// 2. returnImmediately：先拿最早结果，之后轮询或 resubscribe。
t := true
params.Configuration = &protocol.SendMessageConfiguration{ReturnImmediately: &t}

// 3. 流式：事件实时到达。
events, _ := c.StreamMessage(ctx, params)
```

重连运行中的任务：`SubscribeToTask` 先给当前任务快照，再给实时事件。

## 功能配方

| 需求 | 做法 | 示例 |
| --- | --- | --- |
| 鉴权（JWT / API key / OAuth2） | `server.WithAuthProvider(...)`；多方案用链式 provider | [examples/auth](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/auth) |
| 推送通知（webhook） | 配置经 `CreateTaskPushNotificationConfig` 入库，用 `OnPushNotificationGet` 解析，JWT + JWKS 签名 | [examples/jwks](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/jwks) |
| Redis 持久化 | `redis.NewTaskManager(processor, rdb)`；留存用 `redis.WithExpireTime` | [examples/redis](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/redis) |
| 多租户 | 按 `ec.Tenant` 分发；按租户卡片用 `server.WithTenantCard` | [examples/tenant](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/tenant) |
| 子路径部署 | 把路径写进 agent card 的 URL，服务端按此挂载 | [examples/subpath](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/subpath) |
| 服务 legacy v0.2.x 客户端 | `server.WithCompatHandler(v0.NewJSONRPCHandler(tm))`——同端点、同鉴权链 | [examples/compat](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/compat) |
| Agent 编排 | agent 经 A2A client 调用其他 agent | [examples/multi](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/multi) |
