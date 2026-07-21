# Basic A2A Chat Example

An interactive chat example written in the `taskmanager.TaskHandle` style.
One processor serves blocking `message/send`, streaming `message/stream`, and
long-running task operations while the client keeps a `contextId` across turns.

The example covers:

- Blocking and streaming message consumption
- `returnImmediately` kickoff for a long-running task
- `GetTask`, `SubscribeToTask`, and `CancelTask`
- Conversation grouping and server-side message history through `contextId`
- Multiple artifact chunks aggregated into task snapshots

For the minimal raw-channel `MessageProcessor` contract, see
[`examples/simple`](../simple). For an `input-required` continuation flow, see
[`examples/inputrequired`](../inputrequired).

## Run it

Start the server from this directory:

```bash
go run ./server

# Or choose another address.
go run ./server -host 0.0.0.0 -port 9000
```

In another shell, run the interactive client:

```bash
go run ./client -host localhost:8080

# Consume ordinary messages through message/stream.
go run ./client -host localhost:8080 -stream
```

Useful REPL commands: `/help`, `/long-task`, `/async-long-task`, `/gettask`,
`/subscribe`, `/cancel`, `/new`, and `/quit`.

`/long-task` starts a task with `returnImmediately` and immediately subscribes
to its events. `/async-long-task` only starts the task, leaving `/gettask`,
`/subscribe`, and `/cancel` to be run separately.

> `http.Client.Timeout` bounds the whole response body read. For
> `message/stream` and `SubscribeToTask`, that includes the entire SSE
> lifetime. The example uses 60 seconds so its one-word-per-second task can
> finish with some headroom.
