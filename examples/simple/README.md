# Simple A2A Example (MessageProcessor contract)

A minimal interactive example written directly on the v2 `MessageProcessor`
contract — the native channel style (for the `TaskHandle` compatibility style,
see [`examples/basic`](../basic)). It exists to show the one idea that changed
in v2: **you write the agent once, and the framework serves every consumption
mode from that single code path.**

## The one processor, several ways to consume it

The server implements a single method:

```go
func (p *simpleMessageProcessor) ProcessMessage(
    ctx context.Context, ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error)
```

It emits events — `Working` → artifact chunks → `Completed` — and never
branches on "is this streaming?". You are not writing a response; you are
writing the **task's event log**. Clients then consume that same log
differently:

| Client mode | How | What the caller gets |
|------|----------|----------------------|
| blocking send | type text in the REPL | Blocks; receives the **final task snapshot** (state + artifacts). |
| streaming | `go run ./client -stream` | Receives **every event as it happens** via `message/stream`. |
| long-running | `/long-task` or `/async-long-task` | `returnImmediately` kickoff; subscribe for live chunks, or poll with `/gettask` / cancel with `/cancel`. |
| pure-message | send a message with no text part | Processor replies with a plain `Message` and **no task is created**. |

The default path uses `taskmanager.TaskHandle` synchronously (buffers, then
`Events()`). The long-task path emits from a goroutine so
`returnImmediately` / `SubscribeToTask` / `CancelTasks` can observe progress.

## Run it

Start the server:

```bash
go run ./server            # listens on localhost:8080
```

In another shell, run the interactive client:

```bash
go run ./client -host localhost:8080

# Streaming consumption mode
go run ./client -host localhost:8080 -stream
```

Useful REPL commands: `/help`, `/long-task`, `/async-long-task`, `/gettask`,
`/subscribe`, `/cancel`, `/new`, `/quit`.

> A note on the client's `WithTimeout`: `http.Client.Timeout` bounds the whole
> response body read, which for `message/stream` / `SubscribeToTask` is the
> entire SSE lifetime. The 60s here covers the long-task demo with headroom.
