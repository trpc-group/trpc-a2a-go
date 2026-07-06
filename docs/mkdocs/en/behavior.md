# How tRPC-A2A-Go Behaves

This page describes the framework's runtime behavior: the contract your agent
code lives under, and the semantics clients observe. Protocol background is in
[protocol.md](protocol.md); code recipes are in [usage.md](usage.md).

## The MessageProcessor contract

Your agent is one method:

```go
type MessageProcessor interface {
    ProcessMessage(ctx context.Context, ec *ExecContext) (<-chan protocol.StreamEvent, error)
}
```

You read a **read-only request snapshot** (`ExecContext`) and return a channel
of events — the task's event log. The framework owns everything else: task
creation, persistence, fan-out to subscribers, and the derivation of every
response shape. **One processor serves `SendMessage` and `SendStreamingMessage`
alike**; there is no streaming/non-streaming branch in agent code.

`ExecContext` carries: `TaskID` (pre-allocated), `Task` (the snapshot on a
continuation round, nil on a fresh one), `Message`, `ContextID`, `Tenant`,
`History` (conversation snapshot), `AcceptedOutputModes`, and `PushConfig`
(the request's inline webhook config, passed through — honoring it is the
agent's decision).

## Round lifecycle

A **round** is one `ProcessMessage` invocation and the drain of its channel.

- **Lazy task creation** — the task materializes when the first *task event*
  is persisted. A round that only emits a `Message` leaves no task behind
  (`GetTask` for that round's pre-allocated ID returns not-found).
- **One active run per task** — a second message for a task whose round is
  still running is rejected (`-32602`, "already has an active execution").
- **The round ends when you close the channel** — and only then. The close
  rules applied at that moment:

  | Task state at close | Outcome |
  | --- | --- |
  | terminal (you emitted it) | round ends normally |
  | `input-required` / `auth-required` | task **suspends**, awaiting a follow-up |
  | `submitted` / `working` | **`FAILED`** — "processor finished without terminal state" (a bug signal) |
  | cancellation was requested | **`CANCELED`** |

- **Suspend yields the round** — emitting `input-required`/`auth-required`
  releases the task immediately so a continuation can start; anything the old
  round emits afterwards is discarded. Deliver the completion from the
  continuation round.
- **Contract violations fail fast** — emitting an event for a foreign
  `taskId`, a `*protocol.Task` snapshot (framework-only in v1.0), or a
  stateless status marks the round's task `FAILED` and discards the rest.
- **A round that never closes is a leak** — its execution slot stays pinned
  (all follow-ups rejected) and manager `Close()` / server `Stop()` wait for
  it. Always close, from the goroutine that emits.

## Execution and cancellation

- Rounds run on a **detached context**: a client disconnect does **not**
  cancel the work; results stay retrievable via `GetTask`/`SubscribeToTask`.
- Only `CancelTask` (and manager shutdown) cancels the processor's `ctx`.
  The polite processor reaction is to **stop emitting and close** — the
  framework persists `CANCELED`. A terminal event emitted *after* the cancel
  still wins (a round allowed to finish finishes).
- `CancelTask` **returns the snapshot at the moment cancellation was
  requested** (possibly still `working`); the terminal `CANCELED` state lands
  when the round winds down. Canceling an already-terminal task returns
  `-32002` (not cancelable).

## Response derivation

**`SendMessage` (blocking, the default)** waits for the round to end, then
answers with the **task snapshot** if the round touched a task, else the
**last Message**; a round that emitted nothing is a processor bug (`-32603`).

**`SendMessage` with `returnImmediately=true`** answers with the **earliest
usable result**: the first *persisted* task snapshot or the first Message.
Note a Message answered this way carries no `taskId` — if the client must
track the task, emit a task event first.

**`SendStreamingMessage`** forwards every event in order, each **persisted before
delivery** (`GetTask` never lags what a subscriber saw). The stream ends at
the terminal or suspend frame.

**`SubscribeToTask`** sends the current full task snapshot first, then live
increments (at-least-once around the snapshot boundary on the Redis backend);
terminal tasks are rejected. Subscriptions are tied to the request — a
disconnect cleans the subscription up server-side.

## Conversation, history, and what gets remembered

Storage is two-level: **message bodies by `messageId`**, and per-`contextId`
**conversation indexes** ordered by time. What enters the conversation:

- every round's **request message**, and
- every **`Message` event** the processor emits. **Nothing else.**

In particular — and this differs from the official a2a SDKs, which roll the
previous `status.message` into `task.history` on every transition:

> **Status messages are ephemeral here** (overwritten by the next status,
> never stored), and **artifacts never enter history**. Anything that must be
> remembered across rounds — an LLM's final answer above all — must be
> emitted as a `Message` event, or the next round's `ec.History` will contain
> the user's turns only.

(The spec itself only promises that `Task.history` contains *messages*
exchanged during execution, and notes that not every message is guaranteed to
be persisted — retention is implementation-defined.)

`Task.history` is virtual: never persisted on the task, filled at response
time from the conversation per the request's `historyLength` (unset = full
history up to the manager's cap, `<=0` = none). `ec.History` is a snapshot
taken before the round starts, truncated to the manager's
`MaxHistoryLength` (default 100).

## Retention

| | memory backend | redis backend |
| --- | --- | --- |
| Conversations | cleaned after `ConversationTTL` idle (default 1h, cleaner on by default); capped at `MaxHistoryLength` messages | key TTL (default 1h), refreshed on writes |
| Terminal tasks | **retained forever by default** (`TaskTTL` = 0) — set `memory.WithTaskTTL` in production | key TTL (default 1h, `WithExpireTime`) |
| Suspended tasks | never collected (cleaner is terminal-only) — make clients resume or cancel them | expire with the key TTL, including legitimately suspended ones |

There is no per-conversation or per-task delete API; A2A defines none.

## Configuration capabilities (`SendMessage` configuration)

| Field | Consumed by | Effect |
| --- | --- | --- |
| `returnImmediately` | framework | response timing (see above); absent = wait |
| `historyLength` | framework | how much history rides on response tasks |
| `acceptedOutputModes` | your processor (`ec.AcceptedOutputModes`) | output negotiation hint — not enforced |
| `taskPushNotificationConfig` | your processor (`ec.PushConfig`) | inline webhook config, passed through without registration |

## Multi-tenant and legacy clients

- **Multi-tenant**: one process can host several agents; the request's tenant
  arrives as `ec.Tenant` and per-tenant agent cards are served via
  `server.WithTenantCard`. See [examples/tenant](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/tenant).
- **Legacy v0.2.x clients**: mount `compat/v0` on the same endpoint with
  `server.WithCompatHandler` — legacy method names are disjoint from v1.0's,
  and the legacy defaults (notably the non-blocking `message/send`) are
  preserved. See [examples/compat](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/compat).
