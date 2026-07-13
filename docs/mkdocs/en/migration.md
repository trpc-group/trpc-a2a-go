# Migrating from v0.x

This guide ports an existing v0.x agent to v1.0 (the `/v2` module). It expands
the mapping table in the README into a full account of what changed, what each
v0.x symbol becomes, and the behavior changes that compile cleanly but run
differently. For the runtime contract behind the new API, see
[Server: the round contract](server.md#the-round-contract); for build recipes see [Server](server.md) and [Client](client.md).

## What changed

v1.0 replaces the **multi-outcome callback** with a **single event-stream
contract**. In v0.x, `ProcessMessage` received the message, an options struct,
and a `TaskHandler`, and returned a `MessageProcessingResult` that could be a
`Message`, a subscriber stream, or a task — three shapes for three response
modes, with the agent choosing between them. The agent also owned parts of the
task lifecycle: it called `BuildTask`, `SubscribeTask`, and `CleanTask`, and
threaded a `taskID` through every write.

In v1.0 the agent is one method that reads a **read-only request snapshot**
(`ExecContext`) and returns **one channel of events**. The framework owns
everything else — lazy task creation, persistence, subscriber fan-out — and
**derives** every response shape from the one stream: the `message/send`
snapshot and the `message/stream` feed come out of the same emitted events.
There is no longer a streaming/non-streaming branch in agent code.

**The migration strategy has two halves:**

- **Server code must be ported.** The `ProcessMessage` signature changed, and
  the `TaskHandler` interface is gone. The familiar names survive as a thin
  compatibility layer (`TaskHandle`), so most v0.x bodies — including the
  common fully synchronous style — port with mechanical edits.
- **Existing v0.x *clients* keep working, unchanged.** Mount `compat/v0` on the
  same endpoint and legacy v0.2.x clients are served side by side with v1.0
  clients, through the same authentication chain, with the legacy defaults
  preserved. See [Keeping v0.x clients working](#keeping-v0x-clients-working).

## The mental shift

Three sentences capture the port:

1. **The signature collapses.** `(message, options, handle) -> result` becomes
   `(ec) -> (<-chan event)`. Everything you *read* is a field on `ec`;
   everything you *produce* is an event on the returned channel.
2. **You no longer choose an outcome shape.** There is no `Message` vs
   `StreamingEvents` vs task decision. You emit events; the framework derives
   the unary result and the live stream from them.
3. **The framework owns the task lifecycle.** No `BuildTask`, no
   `SubscribeTask`, no `CleanTask`, no threading a `taskID` through every write.
   One round drives exactly one task; you end it by closing the channel.

### Before (v0.x) / after (v1.0)

The same processor — a `working` → artifact → `completed` task round, plus a
pure-message reply for empty input — in both generations. The **after** form
matches the [examples/basic](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/basic)
style: a synchronous body over `TaskHandle`.

**Before — v0.x (`MessageProcessor` + `TaskHandler`):**

```go
func (p *myProcessor) ProcessMessage(
    ctx context.Context,
    message protocol.Message,
    options taskmanager.ProcessOptions,
    handle taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
    text := extractText(message)
    if text == "" {
        // A pure message reply — no task.
        reply := protocol.NewMessage(
            protocol.MessageRoleAgent,
            []*protocol.Part{protocol.NewTextPart("input message must contain text")},
        )
        return &taskmanager.MessageProcessingResult{Result: &reply}, nil
    }

    // Explicitly create the task, then drive it by ID.
    contextID := handle.GetContextID()
    taskID, err := handle.BuildTask(nil, &contextID)
    if err != nil {
        return nil, err
    }

    // Subscribe to obtain the stream to hand back.
    subscriber, err := handle.SubscribeTask(&taskID)
    if err != nil {
        return nil, err
    }

    go func() {
        handle.UpdateTaskState(&taskID, protocol.TaskStateWorking, nil)
        result := doWork(text)
        handle.AddArtifact(&taskID, protocol.Artifact{
            ArtifactID: "processed-" + taskID,
            Parts:      []*protocol.Part{protocol.NewTextPart(result)},
        }, true /* isFinal */, false /* needMoreData */)
        done := protocol.NewMessage(
            protocol.MessageRoleAgent,
            []*protocol.Part{protocol.NewTextPart(result)},
        )
        handle.UpdateTaskState(&taskID, protocol.TaskStateCompleted, &done)
    }()

    return &taskmanager.MessageProcessingResult{StreamingEvents: subscriber}, nil
}
```

**After — v1.0 (`MessageProcessor` + `TaskHandle`):**

```go
func (p *myProcessor) ProcessMessage(
    ctx context.Context,
    ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
    handle := taskmanager.NewTaskHandle(ctx, ec)
    defer handle.Close()

    text := extractText(ec.Message)
    if text == "" {
        // A pure message reply: no task comes into existence this round.
        handle.Reply(taskmanager.ReplyText("input message must contain text"))
        return handle.Events(), nil
    }

    // No BuildTask / SubscribeTask: the framework creates the task lazily on
    // the first task event and stamps the IDs. Emits before Events() never
    // block, so the body stays synchronous — no goroutine required.
    handle.UpdateTaskState(protocol.TaskStateWorking, nil)
    result := doWork(text)
    handle.AddArtifact(protocol.Artifact{
        ArtifactID: "processed-" + handle.TaskID(),
        Parts:      []*protocol.Part{protocol.NewTextPart(result)},
    }, true /* lastChunk */)
    handle.UpdateTaskState(protocol.TaskStateCompleted, taskmanager.ReplyText(result))

    return handle.Events(), nil
}
```

The `taskID` arguments, the explicit `BuildTask`/`SubscribeTask`, and the
`MessageProcessingResult` wrapping all fall away. For live streaming, run the
same body in a goroutine and call `Events()` as the return expression — the raw
channel underneath is the actual contract, shown in
[examples/simple](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/simple).

## API mapping

### The processor signature

| v0.x | v1.0 |
| --- | --- |
| `ProcessMessage(ctx, message, options, handler) (*MessageProcessingResult, error)` | `ProcessMessage(ctx, ec *ExecContext) (<-chan protocol.StreamEvent, error)` |
| `message protocol.Message` (parameter) | `ec.Message` |
| `MessageProcessingResult{Result: &msg}` | emit the message: `handle.Reply(&msg)` (or `out <- &msg` on the raw channel) |
| `MessageProcessingResult{StreamingEvents: subscriber}` | the returned channel **is** the stream: `return handle.Events(), nil` |
| `MessageProcessingResult` (type) | removed — closing the channel is the only outcome; the framework derives each response shape |

### `ProcessOptions`

| v0.x | v1.0 |
| --- | --- |
| `ProcessOptions` (type) | removed — its fields moved onto `ExecContext` |
| `ProcessOptions.Streaming` | removed — one processor serves `message/send` and `message/stream`; the framework derives each result |
| `ProcessOptions.Blocking` | removed — the client controls timing via `returnImmediately`; the framework applies it (note the [inverted default](#response-timing)) |
| `ProcessOptions.HistoryLength` | removed — the framework applies it to response tasks |
| `ProcessOptions.PushNotificationConfig` | `ec.PushConfig` |
| `ProcessOptions.AcceptedOutputModes` | `ec.AcceptedOutputModes` |
| `ProcessOptions.Tenant` | `ec.Tenant` |

### `TaskHandler` verbs

| v0.x | v1.0 |
| --- | --- |
| `TaskHandler` (interface) | removed — reads come from `ec`, writes go through the returned channel (or `TaskHandle`) |
| `BuildTask(...)` | removed — tasks are created lazily on the first task event; the ID is `ec.TaskID` / `handle.TaskID()` |
| `UpdateTaskState(taskID, state, msg)` | `handle.UpdateTaskState(state, msg)` (or emit `*protocol.TaskStatusUpdateEvent` on the raw channel) — no `taskID` argument |
| `AddArtifact(taskID, artifact, isFinal, needMoreData)` | `handle.AddArtifact(artifact, appendChunk, lastChunk)` — `needMoreData` maps to `appendChunk`, `isFinal` to `lastChunk`; chunks sharing an `ArtifactID` with `appendChunk=true` reassemble into one artifact |
| `SubscribeTask(taskID)` | removed — the returned channel is the stream; the framework owns subscriber fan-out |
| `GetTask(taskID)` | `handle.GetTask()` — this round's continuation snapshot only, `nil` on a fresh round; arbitrary-task reads are gone |
| `CleanTask(taskID)` | removed — the framework owns the task lifecycle; nothing deletes tasks by default (set `memory.WithTaskTTL` to collect terminal tasks) |
| `GetContextID()` | `handle.GetContextID()` / `ec.ContextID` |
| `GetMessageHistory()` | `handle.GetMessageHistory()` / `ec.History` — a **pre-round snapshot** truncated to the manager's `MaxHistoryLength`, not a live store read |
| `GetMetadata()` | removed (it always returned an error in the v0.x memory implementation); the incoming message's metadata is `ec.Message.Metadata` |

### Removed types and constructors

| v0.x | v1.0 |
| --- | --- |
| `taskmanager.TaskSubscriber` | removed with the callback design |
| `taskmanager.CancellableTask` (and `CancellableTask.Cancel`) | removed — cancellation is `CancelTask` cancelling the processor's `ctx` |
| `memory.NewTaskManager(processor, opts...)` | unchanged shape |
| `redis.NewTaskManager(...)` | `redis.NewTaskManager(processor, rdb, opts...)` — **note the argument order** `(processor, rdb)` |
| redis `NewTaskSubscriber` / `WithSubscriberSendHook` / `WithSubscriberBlockingSend` | removed — cross-replica streaming is out of scope for the built-in managers |

### Wire method names

The v1.0 JSON-RPC binding uses PascalCase method names. The slash-delimited
names are the v0.2.x wire, still served by `compat/v0`. This affects clients on
the wire, not agent code.

| v0.2.x wire | v1.0 wire |
| --- | --- |
| `message/send` | `SendMessage` |
| `message/stream` | `SendStreamingMessage` |
| `tasks/get` | `GetTask` |
| `tasks/cancel` | `CancelTask` |
| `tasks/resubscribe` | `SubscribeToTask` |
| `tasks/pushNotificationConfig/set` / `get` / `list` / `delete` | `CreateTaskPushNotificationConfig` / `GetTaskPushNotificationConfig` / `ListTaskPushNotificationConfigs` / `DeleteTaskPushNotificationConfig` |
| `agent/getAuthenticatedExtendedCard` | `GetExtendedAgentCard` |
| _(new in v1.0)_ | `ListTasks` |

### A2A error codes

Business failures the framework maps to JSON-RPC errors:

| Code | Name | When |
| --- | --- | --- |
| `-32001` | TaskNotFound | `GetTask`/`CancelTask` for an unknown or collected task |
| `-32002` | TaskNotCancelable | `CancelTask` on an already-terminal task |
| `-32003` | PushNotificationNotSupported | push config on an agent without support |
| `-32004` | UnsupportedOperation | operation not supported by this agent |
| `-32005` | ContentTypeNotSupported | incompatible content type |
| `-32006` | InvalidAgentResponse | the agent produced an invalid response |
| `-32603` | InternalError | what an empty round (no events emitted) returns |

## Behavior changes to check

These compile fine but behave differently from v0.x. Each ends with a one-line
**what to do**.

### Response timing

- **The blocking default inverted.** In v1.0, `message/send` without
  `returnImmediately` **waits for the round to end**; v0.x with
  `blocking:false` (or absent) answered immediately. → *What to do:* if you
  relied on early return, set `returnImmediately=true` on those calls. Legacy
  clients served via `compat/v0` keep the v0.x default automatically.
- **An empty round is a bug.** A round that emits no event at all makes
  `message/send` fail with `-32603` (InternalError). → *What to do:* always
  emit at least one event (a `Message`, or a task status/artifact) before
  closing.
- **Inline push config is passed through, not registered.** A request's
  `configuration.pushNotificationConfig` now arrives as `ec.PushConfig`; the
  framework does **not** auto-register it. → *What to do:* honor or register it
  yourself if your agent sends webhooks.

### Lifecycle and the round

- **The round ends only when you close the channel.** A round that never closes
  pins the task's execution slot — follow-ups are rejected with "already has an
  active execution" — and blocks manager `Close()` / server `Stop()`. → *What
  to do:* close the channel (or `TaskHandle`) from the goroutine that emits;
  the synchronous idiom is `defer handle.Close()`.
- **Closing in `submitted`/`working` marks the task `FAILED`.** Finishing a
  round without a conclusion is treated as a processor bug. → *What to do:* end
  every round deliberately in a terminal state (`completed`/`failed`/
  `canceled`/`rejected`) or a suspend state (`input-required`/`auth-required`).
- **A pure-message reply leaves no task behind.** `message/send` no longer
  always materializes a task; `tasks/get` for that round's pre-allocated ID
  returns not-found. → *What to do:* don't expect a task from a message-only
  reply; emit a task event first if the client must track one.
- **One round drives exactly one task** (`ec.TaskID`). An event carrying any
  other task ID is a contract violation that fails the round's task. v0.x could
  `BuildTask` several tasks per call. → *What to do:* keep a round to its own
  task; use separate sends for separate tasks.
- **Emitting `*protocol.Task` is now a contract violation.** Sending a task
  snapshot as a stream event was legal in v0.x; the framework now materializes
  snapshots itself. → *What to do:* emit `TaskStatusUpdateEvent` /
  `TaskArtifactUpdateEvent`, never a `*protocol.Task`.
- **`tasks/cancel` returns the pre-cancel snapshot** (possibly still
  `working`); the terminal `CANCELED` state is persisted when the round winds
  down. Canceling an already-terminal task returns `-32002`. → *What to do:*
  don't assume the cancel response is terminal; read the task again, or watch
  the stream, for the settled state.
- **`AddArtifact` signature changed to `(artifact, appendChunk, lastChunk)`.**
  `needMoreData` maps to `appendChunk`, `isFinal` to `lastChunk`. → *What to do:*
  keep passing your `append` flag as `appendChunk`; chunks sharing an
  `ArtifactID` with `appendChunk=true` now reassemble into one artifact.

### Multi-turn and continuations

- **`input-required`/`auth-required` yields the round.** Emitting a suspend
  status releases the task immediately so a continuation can start; any events
  the old round emits afterward are **discarded**. → *What to do:* deliver the
  completion from the continuation round; close the channel right after
  suspending.
- **A follow-up without `taskId` starts a new task.** Sessions keyed on
  `contextId` alone strand the suspended task (on the memory backend it is
  never collected). → *What to do:* echo the `taskId` when answering an
  `input-required` prompt so the follow-up lands on the same task.

### Retention and reads

- **`GetMessageHistory` is a pre-round snapshot** (`ec.History`), not a live
  store read, and it is truncated to the manager's `MaxHistoryLength`. → *What
  to do:* treat it as read-once state captured before the round; don't expect
  it to reflect writes made during the round.
- **`GetTask()` takes no argument and returns only this round's task** (`nil`
  on a fresh round). Arbitrary-task reads from inside the processor are gone.
  → *What to do:* use `ec.Task` / `handle.GetTask()` for the continuation
  snapshot; read other tasks through the `TaskManager` API from outside the
  processor.

For the full runtime contract behind these rules, see [Server: the round contract](server.md#the-round-contract).

## Keeping v0.x clients working

Porting the server does not require touching your clients. Mount the
[compat/v0](https://github.com/trpc-group/trpc-a2a-go/tree/v2/compat/v0) handler
on the same JSON-RPC endpoint with `server.WithCompatHandler`, and unmodified
v0.2.x clients keep working:

```go
import (
    v0 "trpc.group/trpc-go/trpc-a2a-go/v2/compat/v0"
    "trpc.group/trpc-go/trpc-a2a-go/v2/server"
    "trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

tm, err := memory.NewTaskManager(&myProcessor{})
if err != nil {
    log.Fatalf("Failed to create task manager: %v", err)
}

// One TaskManager, two wire protocols. The legacy v0.2.x method names are
// disjoint from the v1.0 names, so both client generations are served on the
// same endpoint, inside the same authentication chain.
srv, err := server.NewA2AServer(tm,
    server.WithAgentCard(agentCard),
    server.WithCompatHandler(v0.NewJSONRPCHandler(tm)),
)
```

The agent itself is written once, on the v1.0 `MessageProcessor` contract; the
compat handler translates the legacy wire to and from it. Crucially, **the v0.x
non-blocking default is preserved on the legacy path**: a configuration-less
legacy `message/send` still returns immediately, even though the native v1.0
`message/send` now blocks by default. See
[examples/compat](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/compat)
for a runnable server and legacy-wire client.

## Migration checklist

- [ ] Change the `ProcessMessage` signature to
      `(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error)`.
- [ ] Read the message and options off `ec` (`ec.Message`, `ec.AcceptedOutputModes`,
      `ec.Tenant`, `ec.PushConfig`, `ec.History`).
- [ ] Drop `BuildTask` — the task is created lazily on the first task event;
      use `ec.TaskID` / `handle.TaskID()`.
- [ ] Remove the `taskID` argument from `UpdateTaskState` / `AddArtifact`, and
      map `needMoreData` → `appendChunk`, `isFinal` → `lastChunk` in
      `AddArtifact(artifact, appendChunk, lastChunk)`.
- [ ] Replace `SubscribeTask` + `MessageProcessingResult{StreamingEvents}` with
      `return handle.Events(), nil`; replace `MessageProcessingResult{Result: &msg}`
      with `handle.Reply(&msg)`.
- [ ] Close the channel from the goroutine that emits (`defer handle.Close()`),
      always in a terminal or suspend state — never leave a round in
      `submitted`/`working`.
- [ ] Echo `taskId` when answering `input-required`, and deliver the completion
      from the continuation round.
- [ ] Remove `GetMetadata` and `CleanTask`; audit `GetMessageHistory` (now a
      snapshot) and `GetTask` (this round's task only) uses.
- [ ] If you build a Redis manager, switch to
      `redis.NewTaskManager(processor, rdb, ...)` (mind the argument order).
- [ ] Re-check response timing: native v1.0 `message/send` blocks by default —
      pass `returnImmediately` where you relied on early return.
- [ ] Add `server.WithCompatHandler(v0.NewJSONRPCHandler(tm))` if any v0.2.x
      clients must keep working.
- [ ] Set `memory.WithTaskTTL` (or a Redis TTL) so terminal tasks are collected
      in production.
