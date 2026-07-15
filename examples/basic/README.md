# A2A Basic Task Lifecycle Example

This example focuses on the v2 task lifecycle. It intentionally uses one
server, one scripted client, and the in-memory task manager.

It demonstrates:

- blocking `message/send`
- suspending a task with `input-required`
- continuing that task by sending a follow-up message with the same `taskId`
- reading a task with `tasks/get`
- filtering tasks by `contextId` with `tasks/list`
- starting work with `returnImmediately` and canceling the live task with
  `tasks/cancel`
- v1.0 agent discovery through `supportedInterfaces`

Push notifications and streaming are covered by their dedicated examples.

## Run

Start the server:

```bash
cd examples/basic/server
go run .
```

In another terminal, run the client:

```bash
cd examples/basic/client
go run .
```

The server listens at `http://localhost:8080/` by default. Use `-host` and
`-port` to change the server address, or point the client at another endpoint:

```bash
go run ./client -agent http://localhost:8080/
```

The client runs the complete lifecycle in order and checks that the
`input-required` follow-up completes the original task instead of creating a
new one.

## Server commands

- Any ordinary text completes as an uppercase artifact.
- `profile` suspends with `input-required`; the next message must carry the
  returned `taskId` and completes the same task.
- `wait` stays in `working` for up to 30 seconds, giving the client a live task
  to cancel.

On a continuation round, the framework supplies the current task as
`ExecContext.Task`. The server relies on that snapshot and does not maintain a
separate session map.
