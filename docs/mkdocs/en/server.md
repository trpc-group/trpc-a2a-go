# Building an Agent (Server)

The server side: how to stand up an A2A server, define your agent as a
`MessageProcessor`, choose a TaskManager mode, and turn on the framework's
server capabilities. For calling agents, see [Client](client.md); the runtime contract these APIs
rely on is [The round contract](#the-round-contract) below.

```bash
go get trpc.group/trpc-go/trpc-a2a-go/v2
```

## The server in three parts

A server binds an **agent card** (identity + capabilities), a **TaskManager**
(execution and state policy), and your **MessageProcessor** (logic):

```go
import (
    "trpc.group/trpc-go/trpc-a2a-go/v2/server"
    "trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

tm, _ := memory.NewTaskManager(&myProcessor{})
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(agentCard))
srv.Start(":8080")   // serves JSON-RPC plus the well-known card
```

`NewA2AServer` takes functional options:

| Option | Purpose |
| --- | --- |
| `WithAgentCard(card)` | The public agent card (single-agent server). |
| `WithTenantCard(tenant, card)` / `WithTenantCardProvider(fn)` | Per-tenant cards (multi-tenant, below). |
| `WithAuthProvider(p)` | Require authentication (below). |
| `WithPushNotificationJWKSHandler(handler)` | Publish a push sender's verification keys. Pass `sender.JWKSHandler()` for `SignedSender`, or any custom `http.Handler`. |
| `WithJWKSEndpoint(false, "")` | Disable the built-in JWKS route when verification keys are published elsewhere; a non-empty path changes the route. |
| `WithBasePath(prefix)` | Mount under a subpath. |
| `WithV1JSONRPCEnabled(false)` | Disable the v1 JSON-RPC binding; a `WithCompatHandler` legacy JSON-RPC handler remains available. |
| `WithHTTPJSONEndpoint(prefix)` | Enable HTTP+JSON and set its base path independently of JSON-RPC. |
| `WithHTTPJSONMaxBodyBytes(bytes)` | Set the HTTP+JSON request-body limit (4 MiB by default; a non-positive value disables it). |
| `WithCompatHandler(h)` | Also serve the legacy v0.2.x wire. |
| `WithMiddleware(mw...)` | Wrap the HTTP handler chain. → [middleware context example](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/middleware) |
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

Wrap a **`TaskHandle`** — a small helper that carries the familiar verbs
(`UpdateTaskState`, `AddArtifact`, `AppendArtifact`, `Reply`) and hands you the
channel to return. A synchronous body works as-is; emits before `Events()`
never block, so no goroutine is required.
→ [examples/basic](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/basic)

```go
func (p *proc) ProcessMessage(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
    h := taskmanager.NewTaskHandle(ctx, ec)
    defer h.Close()
    h.UpdateTaskState(protocol.TaskStateWorking, nil)
    result := doWork(ec.Message)
    h.AddArtifact(result.Artifact, true)                              // lastChunk=true
    h.UpdateTaskState(protocol.TaskStateCompleted, protocol.NewAgentText("done"))
    return h.Events(), nil
}
```

Verbs: `UpdateTaskState(state, message)`, `AddArtifact(artifact, lastChunk)`,
`AppendArtifact(artifact, lastChunk)`, `Reply(message)`, plus reads `TaskID()`,
`GetContextID()`, `GetTask()`, `GetMessageHistory()`. `AddArtifact` starts a
new artifact (or replaces the same ID); `AppendArtifact` appends a continuation
chunk, which must reuse that `ArtifactID`. `protocol.NewAgentText(text)` builds
an agent message.

**`TaskHandle` is just channel operations underneath.** The real contract is
the `<-chan protocol.StreamEvent` you return: `UpdateTaskState` sends a
`*protocol.TaskStatusUpdateEvent`, `AddArtifact` and `AppendArtifact` send a
`*protocol.TaskArtifactUpdateEvent`, `Reply` sends a `*protocol.Message`, and
`Close` closes the channel. You rarely need to, but you can build and send
those events yourself — the only way to reach a field `TaskHandle` doesn't
expose, such as event-level `Metadata`:

```go
out := make(chan protocol.StreamEvent, 4)
go func() {
    defer close(out)
    out <- &protocol.TaskStatusUpdateEvent{Status: protocol.TaskStatus{State: protocol.TaskStateWorking}}
    out <- &protocol.TaskArtifactUpdateEvent{Artifact: art, LastChunk: &done, Metadata: map[string]any{"seq": 1}}
    out <- &protocol.TaskStatusUpdateEvent{Status: protocol.TaskStatus{State: protocol.TaskStateCompleted}}
}()
return out, nil
```

The first valid event selects the response shape. A `Reply` completes a taskless direct response, so later events from that round are discarded. A status or artifact selects a task-producing round. The processor does not emit `*protocol.Task`; for `SendStreamingMessage`, the manager materializes the required initial Task snapshot before forwarding that first update. A fresh round starts from `submitted`, while a continuation starts from `ec.Task`.

([examples/simple](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/simple)
is a processor written entirely on the raw channel.)

### Common shapes

- **Pure reply** (no task materializes): `h.Reply(protocol.NewAgentText("..."))`.
- **Live streaming** — run the body in a goroutine so each event reaches
  `SendStreamingMessage` consumers as it happens; check `ctx.Err()` in long
  loops and just close on cancellation (the framework persists `CANCELED`).
  → [examples/basic](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/basic)
  (`/long-task` and client `-stream`)
- **Multi-turn** — suspend with `h.UpdateTaskState(protocol.TaskStateInputRequired, protocol.NewAgentText("need more"))`, close promptly, and handle the follow-up (which echoes the `taskId`) as a new round with `ec.Task` set. Memory cancels the yielded old round's `ctx` as a teardown signal after publishing the suspend frame; this does not cancel the Task.
  → [examples/inputrequired](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/inputrequired)

### Usage constraints

- Close the channel/handle from the goroutine that emits — the round ends only
  on close, and a never-closing round pins the task and blocks shutdown.
- End every round in a terminal or suspend state; closing in `working` marks
  the task `FAILED`.
- One round drives exactly one task; never emit `*protocol.Task`.
- The first valid event chooses the response shape: `Reply` completes the taskless direct-response path and later events are discarded, while a status/artifact event starts a task lifecycle and lets the manager add the Task wire framing.
- The current `status.message` stays only on `Task.Status`. When a later status
  or follow-up user message supersedes it, the previous message moves into
  history. A terminal status message stays current forever. Emit a final answer
  worth remembering as a `Message`; artifacts never enter history.

## The round contract

The exact semantics your agent code lives under and clients observe. A
**round** is one `ProcessMessage` invocation and the drain of its channel.
The task lifecycle rules below apply to every manager unless qualified.
Memory and Redis persist tasks and conversation history; the stateless manager
described under [TaskManager implementations](#taskmanager-implementations)
applies task events only to a request-local snapshot and retains nothing after
the request.

### Round lifecycle

- **Lazy task creation** — the task materializes when the first *task event* is applied. For `SendStreamingMessage`, the response starts with the pre-update Task snapshot required by the wire protocol, then the triggering status/artifact update. Retaining managers persist the updated task and triggering event before delivering either response frame; the extra Task frame is not journaled as another event. If the first valid event is a `Message`, the direct response completes immediately and later processor events are discarded; no task is created (`GetTask` for that round's pre-allocated ID returns not-found).
- **One active run per task** — a second message for a task whose round is
  still running is rejected (`-32602`, "already has an active execution").
- **The round ends when you close the channel** — and only then. The close
  rules applied at that moment:

  | Task state at close | Outcome |
  | --- | --- |
  | terminal (you emitted it) | round ends normally |
  | `input-required` / `auth-required` | task **suspends**, awaiting a follow-up |
  | `submitted` / `working` | **`FAILED`** — "processor finished without terminal state" (a bug signal) |
  | cancellation was requested | **`CANCELED`** |

- **Suspend yields the round** — emitting `input-required`/`auth-required` releases the task immediately so a continuation can start; anything the old round emits afterwards is discarded. Memory also cancels the yielded old round's `ctx` after publishing the suspend frame and before releasing the slot. This is a round-teardown signal, not `CancelTask`: the Task remains suspended. Close the old round's channel promptly and deliver completion from the continuation round.
- **Contract violations fail fast** — emitting an event for a foreign `taskId`,
  a `*protocol.Task` snapshot (framework-only in v1.0), or an invalid status
  marks an already-materialized task `FAILED` and discards the rest.
- **Suspension requires retained state** — stateless rejects
  `input-required`/`auth-required`, because it cannot accept the continuation
  that those states require.

### Execution and cancellation

- Memory and Redis rounds run on a **detached context**: a client disconnect
  does **not** cancel the work; results stay retrievable via
  `GetTask`/`SubscribeToTask`.
- Stateless rounds are request-bound: a disconnect or manager shutdown cancels
  the processor because there is no retained task to retrieve or resubscribe
  to.
- In memory and Redis managers, `CancelTask` and manager shutdown cancel active processor work. The polite reaction is to **stop emitting and close** — the framework persists `CANCELED`. A terminal event emitted *after* the cancel still wins.
- Memory additionally cancels a yielded round's `ctx` after publishing `input-required`/`auth-required`. This signal only tears down the old round; it does **not** mark the suspended Task `CANCELED`.
- `CancelTask` **returns the snapshot at the moment cancellation was requested**
  (possibly still `working`); the terminal `CANCELED` lands when the round winds
  down. Canceling an already-terminal task returns `-32002`.

### Response derivation

- **`SendMessage` (blocking, the default)** answers with the **task snapshot**
  when a round touched a task; memory and Redis wait for the round to end, and
  stateless waits for a terminal task. A direct **Message** is a complete
  response; stateless returns the first one immediately. A round that emitted
  nothing is a processor bug (`-32006`).
- **`SendMessage` with `returnImmediately=true`** answers with the **earliest
  usable result**: the first task snapshot or the first Message. Retaining
  managers can keep a non-terminal task running; stateless cannot.
- **`SendStreamingMessage`** returns a direct Message without Task framing for a pure-message round. For a task-producing round, every manager first emits the operation-local Task snapshot and then forwards status/artifact updates in order; memory and Redis persist each update before delivery, while stateless applies it only to the request-local snapshot. The stream ends at the terminal or suspend frame.
- **`SubscribeToTask`** sends the current task snapshot first, then live
  increments; terminal tasks are rejected.

### Conversation, history, and what gets remembered

Storage is two-level: **message bodies by `messageId`**, and per-`contextId`
**conversation indexes**. What enters the conversation: every round's request
message, every **`Message` event** the processor emits, and every
**superseded `status.message`** (e.g. an input-required question once the user
continues the task).

> The **current** `status.message` is not in history. A later status transition
> or follow-up user message moves the previous one into history before the next
> turn; a **terminal** status message is never superseded and stays on
> `status.Message` only. **Artifacts never enter history.** Emit an LLM's final
> answer as a `Message` event if it must survive into another conversation.

`Task.history` is virtual: filled at response time from the conversation per the
request's `historyLength`. `ec.History` is a snapshot taken before the round,
truncated to `MaxHistoryLength` (default 100).

For memory and Redis, request `configuration` fields `returnImmediately` and
`historyLength` are consumed by the framework;
`acceptedOutputModes` (`ec.AcceptedOutputModes`) and
`taskPushNotificationConfig` (`ec.PushConfig`) are passed through to your
processor. Stateless still passes `acceptedOutputModes`, but rejects push
configuration. `returnImmediately=true` works for a direct `Message` or a Task
that is already terminal; a non-terminal Task is rejected because stateless
cannot continue it in the background.

## TaskManager implementations

The `TaskManager` chooses both execution lifetime and state ownership. Three
implementations ship in-tree.

**Stateless** — request-bound execution with no retained task, event, or
conversation history:

```go
import "trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/stateless"

tm, _ := stateless.NewTaskManager(proc)
```

Use this when the application already owns conversation context or deliberately
does not want A2A state persisted. The processor uses the same event contract
as every other manager: a direct `*protocol.Message` returns a Message, while
status/artifact events build an ephemeral Task for the originating unary or
streaming response. The Task and all events are discarded when that request
ends.

Because no task survives the request, `GetTask`, `CancelTask`, and
`SubscribeToTask` return task-not-found, `ListTasks` is empty, and task
continuations cannot be accepted. Push notifications and suspended tasks are
unsupported. `returnImmediately=true` works for direct Messages and tasks that
are already terminal, but not for non-terminal tasks that would have to keep
running after the request. Use memory or Redis for any cross-request lifecycle.

A direct Message is the complete response, so its returned `taskId` is cleared
and any later processor events are discarded. For a Task response, stateless
stamps empty event IDs from `ExecContext` and applies the normal close rule: a
channel that closes in `submitted` or `working` produces a `FAILED` Task.

**In-memory** — zero dependencies, single process:

```go
tm, _ := memory.NewTaskManager(proc,
    memory.WithMaxHistoryLength(100),
    memory.WithConversationTTL(time.Hour, 30*time.Second),
    memory.WithTaskTTL(time.Hour),   // 0 = keep terminal tasks forever (default)
)
```

**Redis** — shared across processes, survives restarts:

```bash
go get trpc.group/trpc-go/trpc-a2a-go/taskmanager/redis/v2@v2.0.0-alpha.3
```

```go
import redistm "trpc.group/trpc-go/trpc-a2a-go/taskmanager/redis/v2"

tm, _ := redistm.NewTaskManager(proc, redisClient,   // note: (processor, client)
    redistm.WithExpireTime(time.Hour),                // key TTL
    redistm.WithMaxHistoryLength(100),
)
```

The Redis TaskManager requires Redis 5.0 or newer and atomically stores Task updates with a per-task Redis Stream by default. This lets `SubscribeToTask` reconnect through a different replica sharing Redis without extra configuration. It does not route continuation, live-cancel, or execution requests between nodes. Each Task Stream retains approximately the latest 10,000 events; clients that lag beyond that bound may miss intermediate events. Upgrade every replica together: older TaskManager versions do not write the journal consumed by Stream-only subscribers.

→ [examples/redis](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/redis).
Implement the `taskmanager.TaskManager` interface for a custom backend.

Retention for the stateful managers:

| | memory backend | redis backend |
| --- | --- | --- |
| Conversations | cleaned after `ConversationTTL` idle (default 1h); capped at `MaxHistoryLength` | key TTL (default 1h), refreshed on writes |
| Terminal tasks | **kept forever by default** (`TaskTTL` = 0) — set `memory.WithTaskTTL` in production | key TTL (default 1h, `WithExpireTime`) |
| Suspended tasks | never collected (cleaner is terminal-only) — have clients resume or cancel them | expire with the key TTL; a live execution renews its lease until it finishes or yields |

There is no per-task delete API; A2A defines none. Redis `SubscribeToTask` observation is backed by the per-task Redis Stream; active local response pipes close when the client disconnects, the Task becomes terminal, or its Redis key expires.

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

For disconnected operation, the client registers a webhook and the framework
delivers task updates to it automatically. Delivery is opt-in: give the
TaskManager a `push.Sender` and it POSTs a `StreamResponse` to every webhook
registered for a task for every task event. To filter or batch events, wrap the
configured `push.Sender` with an application policy.

```go
// A SignedSender generates a temporary signing key by default. Production
// replicas share one key via pushauth.WithJWTKey(key, kid).
sender, _ := pushauth.NewSignedSender()

// TaskManager: the Sender enables automatic delivery. The manager reports its
// push capability to the server, which advertises it on the card.
tm, _ := memory.NewTaskManager(processor,
    memory.WithPushNotifications(push.Config{Sender: sender}),
)

// Server: publish the sender's verification keys at the JWKS endpoint.
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
    server.WithPushNotificationJWKSHandler(sender.JWKSHandler()))
```

Clients register configs via `CreateTaskPushNotificationConfig` (manage them with
`Get`/`List`/`Delete`; get and delete address both task ID and config ID).
Without either a sender or manual delivery, push is unsupported: the config RPCs
return `-32003 PushNotificationNotSupported`, matching the official SDK. To keep
registration open while the application controls delivery itself, set
`WithPushNotifications(push.Config{ManualDelivery: true})`. The TaskManager does
not need or use a sender in this mode; the application may deliver through a
`push.Sender`, queue, or durable outbox. Automatic delivery uses a bounded,
process-local queue: it preserves order per registered config and backpressures
task event processing when full, but it is not a durable outbox across process
crashes. Use manual delivery backed by durable storage when that guarantee is
required. An inline
`configuration.taskPushNotificationConfig` is a registration too: it is rejected
when push is unsupported and persisted once the round materializes a task.
Automatic mode delivers it; manual mode leaves delivery to the application. A
message-only round does not leave an orphan config. It also reaches your
processor as `ec.PushConfig`. Custom headers/tracing:
`push.WithRequestDecorator`.
→ [examples/jwks](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/jwks).

`SignedSender` honors static credentials declared by the client (such as Basic
or Bearer) and signs with its JWT identity only when `Authentication` is omitted.
A declared scheme without credentials must be resolved with
`push.NewHTTPSender(push.WithAuthorizationHeader(...))`; it is never silently
replaced by JWT. `HTTPSender` rejects private/special-use destinations and
redirects by default; trusted private callbacks require the explicit
`push.WithUnsafeAllowPrivateNetworks()` option. If callbacks must be unsigned,
use `push.NewHTTPSender()` and omit `WithPushNotificationJWKSHandler`.

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

On JSON-RPC the tenant travels in `params`. On HTTP+JSON it is the leading
`/{tenant}/…` path segment that the specification's Protocol Buffer definition
binds for every operation, and POST bodies carry the `tenant` field as well.
The server also accepts the tenant as a `tenant` query parameter.

→ [examples/tenant](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/tenant).

## Serving on a subpath

Mount the whole server under a path prefix, for example behind a gateway.
Use `WithBasePath`; it adjusts the Agent Card, JSON-RPC, enabled HTTP+JSON, and JWKS endpoints together:

```go
srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
    server.WithBasePath("/api/v1/agent"))   // card + enabled bindings under /api/v1/agent/…
```

Without `WithBasePath`, the server keeps the root path defaults. Agent Card URLs /
`supportedInterfaces` are client discovery metadata and do not derive listen paths.

→ [examples/subpath](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/subpath).

## Serving legacy v0.2.x clients

With memory or Redis, keep unmodified v0.2.x clients working while they
migrate: mount `compat/v0` on the same endpoint. Legacy slash-method names are
disjoint from the v1.0 PascalCase names, so one endpoint dispatches both inside
the same auth chain and against the same `TaskManager`. Memory and Redis
preserve the old defaults, notably non-blocking `message/send`. Stateless can
serve that default for direct Message responses, but cannot keep a non-terminal
Task running in the background.

```go
import v0 "trpc.group/trpc-go/trpc-a2a-go/v2/compat/v0"

srv, _ := server.NewA2AServer(tm, server.WithAgentCard(card),
    server.WithCompatHandler(v0.NewJSONRPCHandler(tm)))
```

To expose v1 only through HTTP+JSON while retaining JSON-RPC solely for legacy clients, disable the v1 JSON-RPC binding explicitly:

```go
srv, _ := server.NewA2AServer(tm,
    server.WithAgentCard(card),
    server.WithHTTPJSONEndpoint("/"),
    server.WithV1JSONRPCEnabled(false),
    server.WithCompatHandler(v0.NewJSONRPCHandler(tm)),
)
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

The framework implements both **JSON-RPC** and **HTTP+JSON (REST)**. V1 JSON-RPC is enabled by default and can be disabled with `WithV1JSONRPCEnabled(false)`; a configured compat/v0 JSON-RPC handler remains available. HTTP+JSON is enabled only by `WithHTTPJSONEndpoint`; it accepts `application/a2a+json` and compatibility `application/json`, responds with `application/a2a+json`, and emits raw `StreamResponse` objects in SSE `data:` fields. HTTP+JSON request bodies are limited to 4 MiB by default; use `WithHTTPJSONMaxBodyBytes` when a deployment needs a different limit. **gRPC** remains planned. Agent Card `supportedInterfaces` are client discovery metadata and do not drive server mounting; keep the card accurate for every enabled endpoint. The server does not add bindings to or otherwise rewrite signed cards.
