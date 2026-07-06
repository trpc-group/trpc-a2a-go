# Building an Agent (Server)

The server side: how to stand up an A2A server, define your agent as a
`MessageProcessor`, choose a storage backend, and turn on the framework's
server capabilities. For calling agents, see [Client](client.md); for the
runtime contract behind these APIs, see [behavior.md](behavior.md).

```bash
go get trpc.group/trpc-go/trpc-a2a-go/v2
```

## The server in three parts

A server binds an **agent card** (identity + capabilities), a **TaskManager**
(state), and your **MessageProcessor** (logic):

```go
import (
    "trpc.group/trpc-go/trpc-a2a-go/v2/server"
    "trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

tm, _ := memory.NewTaskManager(&myProcessor{})
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(agentCard))
srv.Start(":8080")   // serves JSON-RPC at "/" and the card at /.well-known/agent-card.json
```

`NewA2AServer` takes functional options:

| Option | Purpose |
| --- | --- |
| `WithAgentCard(card)` | The public agent card (single-agent server). |
| `WithTenantCard(tenant, card)` / `WithTenantCardProvider(fn)` | Per-tenant cards (multi-tenant, below). |
| `WithAuthProvider(p)` | Require authentication (below). |
| `WithJWKSEndpoint(enabled, path)` + `WithPushNotificationAuthenticator(a)` | Sign push notifications, publish keys. |
| `WithBasePath(prefix)` | Mount under a subpath. |
| `WithCompatHandler(h)` | Also serve the legacy v0.2.x wire. |
| `WithMiddleware(mw...)` | Wrap the HTTP handler chain. |
| `WithCORSEnabled(true)` | Emit CORS headers. |
| `WithReadTimeout` / `WithWriteTimeout` / `WithIdleTimeout` | HTTP server timeouts. |
| `WithTelemetryMeterProvider(mp)` / `WithFirstTokenPolicy(p)` | Metrics + TTFT. |

Shut down with `srv.Stop(ctx)`, which drains in-flight rounds before closing.

## Defining the MessageProcessor

Your agent is one method:

```go
type MessageProcessor interface {
    ProcessMessage(ctx context.Context, ec *ExecContext) (<-chan protocol.StreamEvent, error)
}
```

You read the request from a read-only snapshot and return a channel of events —
the task's event log. The framework serves `SendMessage` (unary) and
`SendStreamingMessage` (streaming) from this one method; there is no
streaming/non-streaming branch in your code.

`ExecContext` carries what you need to answer: `Message` (the incoming
message), `TaskID` (pre-allocated), `Task` (the current snapshot on a
continuation round, `nil` on a fresh one), `ContextID`, `Tenant`, `History`
(a conversation snapshot), `AcceptedOutputModes`, and `PushConfig` (an inline
webhook config, if the client sent one).

There are two authoring styles; both produce the same event stream.

### Style 1 — `TaskHandle` (recommended)

The familiar verb API. A synchronous body works as-is — emits before
`Events()` never block, so no goroutine is required.
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

Verbs: `UpdateTaskState(state, message)`, `AddArtifact(artifact, lastChunk)`,
`Reply(message)`, plus reads `TaskID()`, `GetContextID()`, `GetTask()`,
`GetMessageHistory()`. `taskmanager.ReplyText(text)` builds an agent message.

### Style 2 — raw channel

Construct `protocol.StreamEvent` values and send them yourself. Full control
over every field — needed for artifact-append chunking (`TaskHandle` does not
expose the `Append` flag).
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

### Common shapes

- **Pure reply** (no task materializes): `h.Reply(taskmanager.ReplyText("..."))`.
- **Live streaming** — run the body in a goroutine so each event reaches
  `SendStreamingMessage` consumers as it happens; check `ctx.Err()` in long
  loops and just close on cancellation (the framework persists `CANCELED`).
  → [examples/streaming](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/streaming)
- **Multi-turn** — suspend with
  `h.UpdateTaskState(protocol.TaskStateInputRequired, taskmanager.ReplyText("need more"))`,
  close, and handle the follow-up (which echoes the `taskId`) as a new round
  with `ec.Task` set.

### Rules that keep you out of trouble

- Close the channel/handle from the goroutine that emits — the round ends only
  on close, and a never-closing round pins the task and blocks shutdown.
- End every round in a terminal or suspend state; closing in `working` marks
  the task `FAILED`.
- One round drives exactly one task; never emit `*protocol.Task`.
- Anything worth remembering across rounds must be emitted as a `Message`
  event (status messages are ephemeral; artifacts never enter history).

The full contract is [behavior.md](behavior.md).

## Storage backends

The `TaskManager` owns task and conversation state. Two backends ship in-tree.

**In-memory** — zero dependencies, single process:

```go
tm, _ := memory.NewTaskManager(proc,
    memory.WithMaxHistoryLength(100),
    memory.WithConversationTTL(time.Hour, 30*time.Second),
    memory.WithTaskTTL(time.Hour),   // 0 = keep terminal tasks forever (default)
)
```

**Redis** — shared across processes, survives restarts:

```go
import redistm "trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/redis"

tm, _ := redistm.NewTaskManager(proc, redisClient,   // note: (processor, client)
    redistm.WithExpireTime(time.Hour),                // key TTL
    redistm.WithMaxHistoryLength(100),
)
```

→ [examples/redis](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/redis).
Retention differences and the Redis cross-replica caveat are in
[behavior.md](behavior.md). Implement the `taskmanager.TaskManager` interface
for a custom backend.

## Authentication

Require auth on the server; three schemes, composable into a chain. The client
side is in [Client](client.md#authentication).

```go
provider := auth.NewChainAuthProvider(
    auth.NewJWTAuthProvider(secret, audience, issuer, time.Hour),
    auth.NewAPIKeyAuthProvider(keyMap, "X-API-Key"),
    auth.NewOAuth2AuthProviderWithConfig(oauth2Config, userInfoURL, userIDField),
)
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card), server.WithAuthProvider(provider))
```

The card's `securitySchemes` advertises what the server accepts.
→ [examples/auth](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/auth).

## Push notifications

For disconnected operation, the client registers a webhook and the server
calls it as the task progresses. The framework signs each callback with JWT and
publishes the verification keys at a JWKS endpoint.

```go
authr := auth.NewPushNotificationAuthenticator()
authr.GenerateKeyPair()
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
    server.WithJWKSEndpoint(true, "/.well-known/jwks.json"),
    server.WithPushNotificationAuthenticator(authr),
)
```

Configs are stored via `CreateTaskPushNotificationConfig` (or the request's
inline config, reaching your processor as `ec.PushConfig`); the sender resolves
them with `OnPushNotificationGet` and POSTs the signed payload.
→ [examples/jwks](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/jwks).

## Multi-tenant hosting

One process can host several agents. The request's tenant arrives as
`ec.Tenant`; per-tenant cards are served with `WithTenantCard`.

```go
srv, _ := server.NewA2AServer(tm,
    server.WithTenantCard("weather", weatherCard),
    server.WithTenantCard("billing", billingCard),
)
// in ProcessMessage: switch ec.Tenant { … }
```

→ [examples/tenant](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/tenant).

## Serving on a subpath

Mount the whole server under a path prefix (behind a gateway, say). The path
goes in the agent card URL and `WithBasePath`:

```go
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
    server.WithBasePath("/api/v1/agent"))   // card + JSON-RPC under /api/v1/agent/…
```

→ [examples/subpath](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/subpath).

## Serving legacy v0.2.x clients

Keep unmodified v0.2.x clients working while they migrate: mount `compat/v0` on
the same endpoint. Legacy slash-method names are disjoint from the v1.0
PascalCase names, so one endpoint dispatches both — inside the same auth chain,
against the same `TaskManager`, preserving the old defaults (notably the
non-blocking `message/send`).

```go
import v0 "trpc.group/trpc-go/trpc-a2a-go/v2/compat/v0"

srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
    server.WithCompatHandler(v0.NewJSONRPCHandler(tm)))
```

→ [examples/compat](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/compat).

## Telemetry

The server records OpenTelemetry metrics through a meter provider you supply,
including time-to-first-token (TTFT) for streaming responses. When "first
token" is not the first frame for your agent, customize the policy:

```go
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
    server.WithTelemetryMeterProvider(meterProvider),
    server.WithFirstTokenPolicy(myFirstTokenPolicy),
)
```

## Capability status

The framework serves the **JSON-RPC** transport binding today. The spec also
defines **gRPC** and **HTTP+JSON (REST)** bindings — planned, not yet
implemented. See the [Overview](overview.md#what-you-get) capability matrix.
