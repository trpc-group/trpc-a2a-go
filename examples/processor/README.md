# Processor Style Example

The core agent contract is `taskmanager.MessageProcessor`: return an event
channel, close it to end the round. It is deliberately minimal — one method and
a native Go channel — but it leaves three chores with every agent body: build
the handle, spawn the emitting goroutine, and remember `Close`.

This example shows how to fold those three into a ~30-line adapter
([util/processor.go](../util/processor.go)) **without changing the framework
contract**, mirroring the shape of the official A2A executors (a2a-python's
`AgentExecutor.execute(context, event_queue)`, a2a-go's
`AgentExecutor.Execute(ctx, reqCtx, queue)`):

```go
type Processor interface {
    Process(ctx context.Context, ec *taskmanager.ExecContext, h *taskmanager.TaskHandle) error
}
```

The body is written straight-line: emit through `h`, work, and return — the
adapter owns the goroutine and the close. Wiring is one wrapper call:

```go
taskManager, err := memory.NewTaskManager(util.AsMessageProcessor(&chunkProcessor{}))
```

End-of-round semantics:

- return after emitting a terminal state (`completed`/`failed`/...): normal end;
- return with the task in `input-required` / `auth-required`: the task stays
  suspended awaiting a follow-up message;
- return a non-nil error: the adapter marks the round failed (a terminal state
  the body already emitted wins);
- return without any conclusion: the framework's close rule marks the task
  failed, same as the raw contract.

Copy [util/processor.go](../util/processor.go) into your project if you prefer
this style. For the raw contract, see [examples/basic](../basic) (TaskHandle,
synchronous body) and [examples/jwks](../jwks) (TaskHandle + emitting
goroutine). The adapter is a sibling of
[`util.NewMessageProcessor`](../util/adapter.go), which folds the same
goroutine-and-close ceremony away for agent event streams; `AsMessageProcessor`
does it for a plain straight-line body.

## Run

Terminal 1 — server (listens on :8090):

```bash
go run ./processor
```

Terminal 2 — reuse the simple example's client against it:

```bash
go run ./simple/client -host localhost:8090
```

You should see the working updates and per-chunk artifacts stream in, then the
final `completed` state carrying the full uppercased text.
