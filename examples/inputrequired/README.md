# Input-required Example

This example shows one task across two `message/send` rounds:

1. The first message has no `taskId`. The processor emits
   `TASK_STATE_INPUT_REQUIRED`, so the framework persists a suspended task and
   returns it to the client.
2. The client sends another message with that task's `taskId`. The framework
   invokes the processor again with `ExecContext.Task != nil`, and the second
   round completes the same task.

An `input-required` processor must not keep its event channel open while it
waits for a person. Emitting the suspend state yields ownership of the task;
the processor should close the channel and let a later request start the
continuation round.

## Run

Start the server from the `examples` module:

```bash
go run ./inputrequired/server
```

In another terminal, run the client:

```bash
go run ./inputrequired/client -answer Bob
```

Expected output:

```text
round 1: task=task-... state=TASK_STATE_INPUT_REQUIRED
agent: What is your name?
round 2: task=task-... state=TASK_STATE_COMPLETED
agent: Nice to meet you, Bob!
```

The task ID printed in both rounds is the same. `contextId` groups conversation
history, but it does not select a task; the follow-up must carry `taskId` to
continue the suspended task.
