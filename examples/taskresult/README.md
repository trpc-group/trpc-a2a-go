# Task Result Example

Demonstrates collecting a task's result **after** a non-blocking send: the
client sends a message with `returnImmediately=true`, gets the task id back
right away while the agent keeps working, and then fetches the result two ways.

The server's processing logic is a mock agent from [`examples/util`](../util).
It uppercases the input and streams it in chunks over a few seconds, so the
task stays in the working state long enough to observe the collection flow.

## Run

Terminal 1 — server:

```bash
go run ./taskresult/server
```

Terminal 2 — client:

```bash
go run ./taskresult/client
```

## What it shows

1. **send + `tasks/get` (polling).** `SendMessage` with
   `Configuration.ReturnImmediately=true` returns the first task snapshot
   (`working`) immediately. The client then polls `GetTasks` until the task
   reaches a terminal state and prints the final status message and artifacts.

2. **send + `tasks/resubscribe` (streaming).** Same non-blocking send, but the
   client calls `ResubscribeTask` to stream the remaining events. Resubscribe
   delivers the current task snapshot first, then the live events from the point
   of subscription onward (early chunks emitted before resubscription are not
   replayed), ending with the artifact and the terminal `completed` frame.

Both paths rely on the framework retaining the terminal task so it stays
retrievable via `tasks/get` / `tasks/resubscribe`. Retention is bounded by the
task manager's `TaskTTL` (configurable via `memory.WithTaskTTL`).
