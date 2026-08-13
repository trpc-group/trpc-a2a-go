# Calling Agents (Client)

The client side: how to call an A2A agent — the four consumption modes, task
management, authentication, and calling agents from inside an agent. For
building an agent, see [Server](server.md).

```go
import (
    "trpc.group/trpc-go/trpc-a2a-go/v2/client"
    "trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

c, _ := client.NewA2AClient("http://localhost:8080/")
```

`NewA2AClient` keeps JSON-RPC as the default for direct endpoint construction. Select HTTP+JSON explicitly with `client.WithProtocolBinding(protocol.ProtocolBindingHTTPJSON)`:

```go
restClient, _ := client.NewA2AClient(
    "https://agent.example.com/a2a",
    client.WithProtocolBinding(protocol.ProtocolBindingHTTPJSON),
)
```

When selecting an entry from an Agent Card, pass all three interface properties to the direct constructor. `WithTenant` propagates the selected interface tenant to every request and rejects conflicting per-request values:

```go
iface := card.SupportedInterfaces[0] // first interface supported by this client
selectedClient, _ := client.NewA2AClient(
    iface.URL,
    client.WithProtocolBinding(iface.ProtocolBinding),
    client.WithTenant(iface.Tenant),
)
```

For JSON-RPC, the selected interface URL is the complete request endpoint and is used exactly as declared, including whether its path ends in `/`. HTTP+JSON treats the URL as a route base and appends the operation path.

JSON-RPC requests continue to use `application/json`. HTTP+JSON requests use REST paths and send `application/a2a+json`; unary responses accept both `application/a2a+json` and the 1.0.0-era `application/json` for compatibility.

## The four consumption modes

One agent, four ways to consume it — all from the same `SendMessage` request
shape:

```go
params := protocol.SendMessageParams{
    Message: protocol.Message{
        Role:  protocol.MessageRoleUser,
        Parts: []*protocol.Part{protocol.NewTextPart("hello")},
    },
}
```

**1. Blocking send (default)** — one call, waits for the round to finish,
returns the final `Task` or `Message` (the sealed union):

```go
resp, _ := c.SendMessage(ctx, params)
if task := resp.GetTask(); task != nil {
    // completed / failed / input-required task, with its artifacts
} else if msg := resp.GetMessage(); msg != nil {
    // a pure-message reply (no task was created)
}
```

**2. `returnImmediately`** — answers with the earliest usable result while the
work continues; follow up with `GetTasks` or `ResubscribeTask`:

```go
t := true
params.Configuration = &protocol.SendMessageConfiguration{ReturnImmediately: &t}
resp, _ := c.SendMessage(ctx, params)   // first task snapshot or first message
```

**3. Streaming** — every event live over SSE:

```go
events, _ := c.StreamMessage(ctx, params)
for event := range events {
    switch {
    case event.GetStatusUpdate() != nil:
        // status frame
    case event.GetArtifactUpdate() != nil:
        // artifact chunk
    case event.GetMessage() != nil:
        // message
    }
} // the channel closes when the task reaches a terminal (or interrupted) state
```

**4. Resubscribe** — reattach to a running task after a disconnect; the first
frame is the current task snapshot, then live increments:

```go
events, _ := c.ResubscribeTask(ctx, protocol.TaskIDParams{ID: taskID})
```

The three send modes are demonstrated together by the
[examples/simple client](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/simple).

## Task management

```go
task, _  := c.GetTasks(ctx, protocol.TaskQueryParams{ID: taskID})           // snapshot
list, _  := c.ListTasks(ctx, protocol.ListTasksParams{ContextID: contextID}) // filter + paginate
task, _  = c.CancelTasks(ctx, protocol.TaskIDParams{ID: taskID})            // request cancel
```

`GetTasks` takes an optional `HistoryLength` to shape how much conversation
history rides along. Canceling a task that has already finished returns
`-32002` (not cancelable); the returned task is the snapshot at the moment
cancellation was requested — see [Server: the round contract](server.md#the-round-contract).

## Multi-turn continuation

When a call returns a task in `input-required` (or `auth-required`), resume it
by sending another message that echoes the **same `taskId`**:

```go
follow := protocol.SendMessageParams{
    Message: protocol.Message{
        Role:   protocol.MessageRoleUser,
        TaskID: &taskID,   // continue the waiting task — not a new one
        Parts:  []*protocol.Part{protocol.NewTextPart("March 3rd")},
    },
}
resp, _ := c.SendMessage(ctx, follow)
```

A follow-up **without** the `taskId` starts a brand-new task and leaves the
suspended one waiting.

## Authentication

Attach credentials matching a scheme the agent's card advertises. The server
side is in [Server](server.md#authentication).

```go
c, _ := client.NewA2AClient("http://localhost:8080/",
    client.WithJWTAuth(secret, audience, issuer, time.Hour),
)
```

Runnable client/server wiring for JWT and API key:
→ [examples/auth](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/auth).

## Calling agents from an agent (orchestration)

An agent can call other agents through this same client, invoked from inside
its own `ProcessMessage`: a root agent fans work out to specialists and
aggregates their results into its own task. Note the outbound `SendMessage`
blocks until the sub-agent's round finishes (the v1.0 default), which is
usually what an orchestrator wants.

```go
func (p *root) ProcessMessage(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
    h := taskmanager.NewTaskHandle(ctx, ec)
    defer h.Close()
    h.UpdateTaskState(protocol.TaskStateWorking, nil)
    sub, _ := p.weatherClient.SendMessage(ctx, forward(ec.Message))   // call another agent
    h.UpdateTaskState(protocol.TaskStateCompleted, extractReply(sub))
    return h.Events(), nil
}
```

→ [examples/multi](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/multi).

## Legacy v0.2.x wire

To speak the legacy wire (for a v0.2.x server, or a v1.0 server with the compat
handler mounted), use the `compat/v0` client — it accepts the same v1 types and
converts underneath:

```go
import v0 "trpc.group/trpc-go/trpc-a2a-go/v2/compat/v0"

lc, _ := v0.NewClient("http://localhost:8080/")
resp, _ := lc.SendMessage(ctx, params)   // preserves the v0 non-blocking default
```

→ [examples/compat](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/compat).
