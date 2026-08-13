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
- Custom `auth.Provider` + `server.WithAuthProvider` + `OwnerResolver` (per-user task isolation)

For the minimal raw-channel `MessageProcessor` contract, see
[`examples/simple`](../simple). For an `input-required` continuation flow, see
[`examples/inputrequired`](../inputrequired).

## Run it

Start the server from this directory:

```bash
go run ./server

# Or choose another address.
go run ./server -host 0.0.0.0 -port 9000

# JSON-RPC only (HTTP+JSON is on by default).
go run ./server -http-json=false
```

In another shell, run the interactive client (default `alice` / `alice-pass`):

```bash
go run ./client -host localhost:8080

# Run as bob to demonstrate owner isolation.
go run ./client -host localhost:8080 -user bob -password bob-pass

# Consume ordinary messages through message/stream.
go run ./client -host localhost:8080 -stream

# Use JSON-RPC instead of HTTP+JSON (default is HTTP+JSON).
go run ./client -host localhost:8080 -http-json=false
```

Demo accounts: `alice` / `alice-pass`, `bob` / `bob-pass` (HTTP Basic Auth).
A2A operation requests without valid credentials get `401 Unauthorized`; the public Agent Card remains discoverable.

Useful REPL commands: `/help`, `/long-task`, `/async-long-task`, `/gettask`,
`/subscribe`, `/cancel`, `/new`, and `/quit`.

`/long-task` starts a task with `returnImmediately` and immediately subscribes
to its events. `/async-long-task` only starts the task, leaving `/gettask`,
`/subscribe`, and `/cancel` to be run separately.

## Verify owner isolation

Start an Alice client, enter `/async-long-task`, and copy the Task ID printed as `async task: id=...`. While that Task is running, start a Bob client and address Alice's ID explicitly:

```text
/gettask <alice-task-id>
/subscribe <alice-task-id>
/cancel <alice-task-id>
```

Each Bob operation reports task-not-found (`/subscribe` also attempts its documented `GetTask` fallback). The same `/gettask <alice-task-id>` command still succeeds in Alice's client. This proves owner isolation of retained Task lookup and live-task operations; the TaskManager tests additionally cover list, conversation, push, and Redis Stream boundaries.

> `http.Client.Timeout` bounds the whole response body read. For
> `message/stream` and `SubscribeToTask`, that includes the entire SSE
> lifetime. The example uses 60 seconds so its one-word-per-second task can
> finish with some headroom.
