# Simple A2A Example (MessageProcessor contract)

A minimal server written directly on the v2 `MessageProcessor` contract — the
native channel style (for the `TaskHandle` compatibility style, see
[`examples/basic`](../basic)). It reverses text, and exists to show the one
idea that changed in v2: **you write the agent once, and the framework serves
every consumption mode from that single code path.**

## The one processor, three ways to consume it

The server implements a single method:

```go
func (p *simpleMessageProcessor) ProcessMessage(
    ctx context.Context, ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error)
```

It emits events — `Working` → artifact → `Completed` — on a channel and never
branches on "is this streaming?". You are not writing a response; you are
writing the **task's event log**. Three clients then consume that same log
differently:

| Demo | Endpoint | What the caller gets |
|------|----------|----------------------|
| blocking send | `message/send` | Blocks; receives the **final task snapshot** (state + artifacts). Intermediate events were persisted and fanned out, but this caller sees only the derived result. |
| returnImmediately + poll | `message/send` (`returnImmediately`) | Returns the **first** task snapshot at once; the caller polls `GetTasks` (or resubscribes) for the terminal state. |
| streaming | `message/stream` | Receives **every event as it happens**; the channel closes when the round ends. |
| pure-message | `message/send` (no text part) | The processor replies with a plain `Message` and **no task is created** (lazy creation) — the union carries a Message, not a Task. |

The server code is identical for all four. `TaskID`/`ContextID` are left empty
on the emitted events; the framework stamps them from the `ExecContext` and
creates the task lazily on the first task event.

## Run it

Start the server:

```bash
go run ./server            # listens on localhost:8080
```

In another shell, run the client (it exercises all four demos in sequence):

```bash
go run ./client -host localhost:8080
```

Expected client output (abridged):

```
=== message/send (non-streaming, blocking) ===
final task: state=TASK_STATE_COMPLETED
  artifact: !dlrow olleH
=== message/stream (streaming) ===
status: TASK_STATE_WORKING
artifact: !dlrow olleH
status: TASK_STATE_COMPLETED
stream closed (round ended)
=== message/send (pure-message path: no task created) ===
message reply (no task): input message must contain text.
```

> A note on the client's `WithTimeout`: `http.Client.Timeout` bounds the whole
> response body read, which for `message/stream` is the entire SSE lifetime. A
> real long-running streaming agent needs a longer timeout (or none); the 30s
> here only suits this toy example.
