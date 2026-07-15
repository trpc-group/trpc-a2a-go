# Streaming example

This example demonstrates the v2 asynchronous streaming flow:

1. `SendMessage` with `returnImmediately=true` starts a task and returns its ID.
2. `ResubscribeTask` invokes `SubscribeToTask` on the wire and receives the
   current task snapshot followed by live events.
3. Every artifact update uses the same artifact ID. The first chunk has
   `append=false`, later chunks have `append=true`, and the final chunk has
   `lastChunk=true`.
4. `CancelTask` cancels the processor context; closing the processor event
   channel then lets the task manager persist the canceled state.

Start the server:

```sh
go run ./streaming/server
```

Run a task to completion:

```sh
go run ./streaming/client
```

Run a task and actually cancel it after two chunks:

```sh
go run ./streaming/client -cancel-after=2
```
