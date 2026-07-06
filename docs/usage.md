# Building Agents with tRPC-A2A-Go

Recipes for the common tasks, each linked to a runnable example. Concepts are
in [protocol.md](protocol.md) and [behavior.md](behavior.md); porting a v0.x
agent is covered by the [migration guide](../README.md#migrating-from-v0x).

```bash
go get trpc.group/trpc-go/trpc-a2a-go/v2
```

## A server in one page

```go
import (
    "trpc.group/trpc-go/trpc-a2a-go/v2/server"
    "trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

tm, _ := memory.NewTaskManager(&myProcessor{})     // your agent logic
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(agentCard))
srv.Start(":8080")
```

The agent card advertises identity and capabilities at
`/.well-known/agent-card.json`. Everything interesting happens in the
processor.

## Writing the processor — two styles

**`TaskHandle` style** — the familiar verb API; a synchronous body works
as-is (emits never block before `Events()`). Start here, especially when
porting v0.x code. → [examples/basic](../examples/basic)

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

**Raw channel style** — the underlying contract, full control over every
event field (needed e.g. for artifact `append` chunking).
→ [examples/simple](../examples/simple)

```go
out := make(chan protocol.StreamEvent, 4)
go func() {
    defer close(out)
    out <- &protocol.TaskStatusUpdateEvent{Status: protocol.TaskStatus{State: protocol.TaskStateWorking}}
    // ... artifacts ...
}()
return out, nil
```

**Live streaming**: run the same body in a goroutine so each event reaches
`message/stream` consumers as it happens; check `ctx.Err()` in long loops and
just close on cancellation. → [examples/streaming](../examples/streaming)

**Pure replies**: `h.Reply(taskmanager.ReplyText("..."))` answers without
creating a task at all.

**Multi-turn**: suspend with
`h.UpdateTaskState(protocol.TaskStateInputRequired, taskmanager.ReplyText("need more"))`,
close, and handle the follow-up (which must echo the `taskId`) as a new round
with `ec.Task` set. → [examples/basic](../examples/basic) (`multi` command)

Rules that keep you out of trouble: always close the channel/handle from the
goroutine that emits; end every round in a terminal or suspend state; one
round drives exactly one task; never emit `*protocol.Task`; anything worth
remembering across rounds goes out as a `Message` event.

## Clients — the three consumption modes

One processor, three ways to consume it (all three demonstrated by the
[examples/simple client](../examples/simple)):

```go
c, _ := client.NewA2AClient("http://localhost:8080/")

// 1. Blocking send (default): one call, final result.
resp, _ := c.SendMessage(ctx, params)                 // Task or Message union

// 2. returnImmediately: earliest result now, poll or resubscribe later.
t := true
params.Configuration = &protocol.SendMessageConfiguration{ReturnImmediately: &t}

// 3. Streaming: every event live.
events, _ := c.StreamMessage(ctx, params)
```

Reconnecting to a running task: `tasks/resubscribe` delivers the current task
snapshot first, then live events.

## Feature recipes

| Need | How | Example |
| --- | --- | --- |
| Authentication (JWT / API key / OAuth2) | `server.WithAuthProvider(...)`; chain providers for multiple schemes | [examples/auth](../examples/auth) |
| Push notifications (webhooks) | store configs via `tasks/pushNotificationConfig/set`, resolve with `OnPushNotificationGet`, sign with JWT + JWKS | [examples/jwks](../examples/jwks) |
| Redis-backed persistence | `redis.NewTaskManager(processor, rdb)`; retention via `redis.WithExpireTime` | [examples/redis](../examples/redis) |
| Multi-tenant hosting | dispatch on `ec.Tenant`; per-tenant cards via `server.WithTenantCard` | [examples/tenant](../examples/tenant) |
| Serving on a subpath | put the path in the agent card URL; the server mounts accordingly | [examples/subpath](../examples/subpath) |
| Legacy v0.2.x clients | `server.WithCompatHandler(v0.NewJSONRPCHandler(tm))` — same endpoint, same auth chain | [examples/compat](../examples/compat) |
| Agent orchestration | an agent calling agents through the A2A client | [examples/multi](../examples/multi) |
