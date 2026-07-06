# Building Agents with tRPC-A2A-Go

Recipes for every capability, each linked to a runnable example. Concepts are
in [protocol.md](protocol.md) and [behavior.md](behavior.md); porting a v0.x
agent is [Migrating from v0.x](migration.md).

```bash
go get trpc.group/trpc-go/trpc-a2a-go/v2
```

## 1. The server

A server binds three things: an **agent card** (identity + capabilities), a
**TaskManager** (state), and your **MessageProcessor** (logic).

```go
import (
    "trpc.group/trpc-go/trpc-a2a-go/v2/server"
    "trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

tm, _ := memory.NewTaskManager(&myProcessor{})
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(agentCard))
srv.Start(":8080")            // serves JSON-RPC at "/" and the card at /.well-known/agent-card.json
```

`NewA2AServer` takes functional options. The ones you will reach for:

| Option | Purpose |
| --- | --- |
| `WithAgentCard(card)` | The public agent card (single-agent server). |
| `WithTenantCard(tenant, card)` / `WithTenantCardProvider(fn)` | Per-tenant cards (multi-tenant server). |
| `WithAuthProvider(p)` | Require authentication (§5). |
| `WithJWKSEndpoint(enabled, path)` + `WithPushNotificationAuthenticator(a)` | Sign push notifications, publish keys (§6). |
| `WithBasePath(prefix)` | Mount under a subpath (§8). |
| `WithCompatHandler(h)` | Also serve the legacy v0.2.x wire (§9). |
| `WithMiddleware(mw...)` | Wrap the HTTP handler chain. |
| `WithCORSEnabled(true)` | Emit CORS headers. |
| `WithReadTimeout` / `WithWriteTimeout` / `WithIdleTimeout` | HTTP server timeouts. |
| `WithTelemetryMeterProvider(mp)` / `WithFirstTokenPolicy(p)` | Metrics + TTFT (§10). |

Shut down with `srv.Stop(ctx)`, which drains in-flight rounds before closing.

## 2. Writing the processor

Your agent is one method. Read [`ExecContext`](behavior.md), emit events, and
close the stream. Two authoring styles produce the same events.

**`TaskHandle` style (recommended).** A synchronous body works as-is — emits
before `Events()` never block.
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

**Raw channel style.** Full control over each `protocol.StreamEvent` — needed
for artifact-append chunking.
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

Common shapes:

- **Pure reply** (no task): `h.Reply(taskmanager.ReplyText("..."))`.
- **Live streaming**: run the body in a goroutine so each event reaches
  `SendStreamingMessage` consumers as it happens; check `ctx.Err()` in long
  loops and just close on cancellation — the framework persists `CANCELED`.
  → [examples/streaming](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/streaming)
- **Multi-turn**: suspend with
  `h.UpdateTaskState(protocol.TaskStateInputRequired, taskmanager.ReplyText("need more"))`,
  close, and handle the follow-up (which echoes the `taskId`) as a new round
  with `ec.Task` set.

Rules that keep you out of trouble: close the channel/handle from the goroutine
that emits; end every round in a terminal or suspend state; one round drives
exactly one task; never emit `*protocol.Task`; anything worth remembering
across rounds goes out as a `Message` event.

## 3. Storage backends

The `TaskManager` owns task and conversation state. Two backends ship in-tree.

**In-memory** — zero dependencies, single process:

```go
tm, _ := memory.NewTaskManager(proc,
    memory.WithMaxHistoryLength(100),          // conversation cap
    memory.WithConversationTTL(time.Hour, 30*time.Second),
    memory.WithTaskTTL(time.Hour),             // 0 = keep terminal tasks forever (default)
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
Retention differences (memory `TaskTTL` defaults to *keep forever*; Redis uses
key TTL; suspended-task and cross-replica caveats) are in [behavior.md](behavior.md).
Implement the `taskmanager.TaskManager` interface for a custom backend.

## 4. Clients and the consumption modes

```go
import "trpc.group/trpc-go/trpc-a2a-go/v2/client"

c, _ := client.NewA2AClient("http://localhost:8080/")
```

One processor, four ways to consume it:

```go
// 1. Blocking send (default): one call, final Task-or-Message result.
resp, _ := c.SendMessage(ctx, params)

// 2. returnImmediately: earliest result now, poll or resubscribe later.
t := true
params.Configuration = &protocol.SendMessageConfiguration{ReturnImmediately: &t}
resp, _ = c.SendMessage(ctx, params)

// 3. Streaming: every event live.
events, _ := c.StreamMessage(ctx, params)

// 4. Resubscribe: reattach to a running task (snapshot first, then increments).
events, _ = c.ResubscribeTask(ctx, protocol.TaskIDParams{ID: taskID})
```

Also `c.GetTasks`, `c.ListTasks`, `c.CancelTasks`. The three consumption modes
are demonstrated together by the
[examples/simple client](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/simple).

## 5. Authentication

Require auth on the server; the client attaches credentials. Three schemes,
composable into a chain.

```go
// Server: accept any of several schemes.
provider := auth.NewChainAuthProvider(
    auth.NewJWTAuthProvider(secret, audience, issuer, time.Hour),
    auth.NewAPIKeyAuthProvider(keyMap, "X-API-Key"),
    auth.NewOAuth2AuthProviderWithConfig(oauth2Config, userInfoURL, userIDField),
)
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card), server.WithAuthProvider(provider))
```

The agent card's `securitySchemes` advertises what the server accepts; a client
authenticates per the scheme it chose. Full server + client wiring for JWT,
API key, and OAuth2:
→ [examples/auth](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/auth).

## 6. Push notifications

For disconnected operation, the client registers a webhook and the server calls
it as the task progresses. The framework signs each callback with JWT and
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
inline `pushNotificationConfig`, which reaches your processor as
`ec.PushConfig`); the sender resolves them with `OnPushNotificationGet` and
POSTs the signed payload. End-to-end, with client-side JWT verification via
JWKS:
→ [examples/jwks](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/jwks).

## 7. Multi-tenant hosting

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

## 8. Serving on a subpath

Mount the whole server under a path prefix (behind a gateway, say). The path
goes in the agent card URL and `WithBasePath`:

```go
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
    server.WithBasePath("/api/v1/agent"))
// card + JSON-RPC now live under /api/v1/agent/…
```

→ [examples/subpath](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/subpath).

## 9. Serving legacy v0.2.x clients

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

## 10. Telemetry

The server records OpenTelemetry metrics through a meter provider you supply,
including time-to-first-token (TTFT) for streaming responses. When "first
token" is not the first frame for your agent, customize the policy:

```go
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
    server.WithTelemetryMeterProvider(meterProvider),
    server.WithFirstTokenPolicy(myFirstTokenPolicy),
)
```

## 11. Agent orchestration

An agent can call other agents through the A2A client — the same client you'd
use standalone, invoked from inside a `ProcessMessage`. The root agent fans
work out to specialists and aggregates their results into its own task.
→ [examples/multi](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/multi).

## Capability status

The framework serves the **JSON-RPC** transport binding today. The spec also
defines **gRPC** and **HTTP+JSON (REST)** bindings — these are planned and not
yet implemented; the agent card already models multi-transport declaration for
when they land. On the Redis backend, live event fan-out is per-process
(snapshots are shared); full cross-replica streaming is a planned addition.
See [behavior.md](behavior.md) for the current runtime limits.
