# v0 Compatibility Example

This example shows how one server serves **both protocol generations**: v1.0
clients on the standard endpoint, and unmodified v0.2.x clients through the
`compat/v0` handler — same endpoint, same `TaskManager`, same authentication
chain. Use this setup to keep existing v0.x clients working while they
migrate.

## How it works

The agent is written once, on the v2 `MessageProcessor` contract. The legacy
wire is a mount-time decision:

```go
tm, _ := memory.NewTaskManager(&reverseProcessor{})
srv, _ := server.NewA2AServer(tm,
    server.WithAgentCard(agentCard),
    server.WithCompatHandler(v0.NewJSONRPCHandler(tm)),
)
```

Legacy method names (`message/send`, `tasks/get`, `tasks/resubscribe`, ...)
are disjoint from the v1.0 names, so a single JSON-RPC endpoint dispatches
both generations. `WithCompatHandler` mounts the legacy path **inside** the
authentication middleware chain — legacy clients authenticate exactly like v1
clients.

## What the client demonstrates

The client speaks the legacy wire via `compat/v0.NewClient` (it accepts v1
types and converts underneath):

1. **Configuration-less `message/send`** — the v0.2.x non-blocking default,
   preserved by the compat layer: the call answers immediately with the
   `working` task snapshot, then polls `tasks/get` to completion. (On the
   v1.0 wire the same configuration-less request would block until the round
   ends — the default was inverted in v1.0.)
2. **`blocking=true` send** — maps to v1 `returnImmediately=false`: one call,
   final task in the response.
3. **Legacy `message/stream`** — SSE frames converted to the legacy event
   shapes (`task_status_update`, `task_artifact_update`, ...).

## Running

```bash
# Terminal 1
cd server && go run main.go

# Terminal 2
cd client && go run main.go --message "hello legacy world"
```

Expected client output: an immediate `working` snapshot, a couple of polls,
the reversed-text artifact, then the same flow again as a single blocking
call, and finally the live stream frames.
