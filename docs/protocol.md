# The A2A Protocol: Objects and Semantics

A2A (Agent-to-Agent) is an open protocol for interoperable AI agents: a client
(often itself an agent) discovers a remote agent through its **agent card**,
then talks to it over JSON-RPC — unary calls over HTTP POST, streaming over
SSE. trpc-a2a-go implements A2A **v1.0**; the legacy v0.2.x wire is served
through the [compat/v0](../compat/v0) layer (see
[examples/compat](../examples/compat)).

This page explains the protocol's object model and interaction flows — what
the words mean and what the spec actually mandates. For how this framework
implements them, see [behavior.md](behavior.md).

## The four wire objects

| Object | Role | One-liner |
| --- | --- | --- |
| **Message** | **Communication** | One conversational turn: `role` (user/agent), `parts` (text/file/data), `messageId`, optional `taskId`/`contextId`. Instructions, questions, answers. |
| **Task** | **The unit of work** | A stateful record with a lifecycle: `id`, `contextId`, `status`, `artifacts[]`, `history[]`. Created by the server, immutable once terminal. |
| **TaskStatusUpdateEvent** | **Progress** | Announces a state transition; may carry an explanatory `message`; `final=true` marks the round's last status frame. |
| **TaskArtifactUpdateEvent** | **Deliverables** | Carries an `Artifact` — a named output with parts. Chunked streaming uses `append` (continue the previous chunk) and `lastChunk`. |

The rule of thumb: **Message is talking, Artifact is delivering**.
Explanations and questions go out as messages; the documents/code/data a task
produces go out as artifacts. An agent that answers without doing tracked
work replies with a plain Message and no task exists at all.

Two identifiers glue everything together:

- **`contextId`** names a conversation. The server generates one when the
  request carries none; every task and message belongs to a context. One
  context can span many tasks.
- **`taskId`** names one unit of work. A follow-up message that carries a
  `taskId` **continues that task** (only meaningful while it waits for input);
  a message without one starts fresh.

## The task state machine

```
            ┌──────────────────────────── terminal ─────────────────────────┐
            │                                                               │
 submitted ──► working ──┬──► completed | failed | canceled | rejected      │
                         │        (immutable once reached)                  │
                         └──► input-required | auth-required ── suspended ──┘
                                     │
                                     └── follow-up Message with same taskId
                                         resumes the task (a new round)
```

- **Terminal states are immutable**: nothing may modify a completed/failed/
  canceled/rejected task, and subscribing to one is rejected.
- **`input-required` / `auth-required` suspend** the task: the server has
  asked the client for something. The client resumes by sending a message
  **with the same `taskId`**. A follow-up without the `taskId` is a *new*
  task — sessions keyed only on `contextId` strand the suspended one.

## The RPC surface (v1.0)

| Method | Kind | Purpose |
| --- | --- | --- |
| `message/send` | unary | Send a message; the response is a **`Task` or a `Message`** (a union). By default it **waits** for the round to finish. |
| `message/stream` | SSE | Same request, but every event streams live as it happens. |
| `tasks/get` | unary | Fetch a task snapshot; `historyLength` shapes how much history rides along. |
| `tasks/list` | unary | Enumerate tasks. |
| `tasks/cancel` | unary | Request cancellation. |
| `tasks/resubscribe` | SSE | Reattach to a live task's stream after a disconnect. |
| `tasks/pushNotificationConfig/*` | unary | CRUD for webhook configs, for disconnected operation. |

### Blocking vs `returnImmediately`

`message/send` takes an optional `configuration.returnImmediately`:

> If `false` (**default**), the operation MUST wait until the task reaches a
> terminal (COMPLETED, FAILED, CANCELED, REJECTED) or interrupted
> (INPUT_REQUIRED, AUTH_REQUIRED) state before returning.

With `returnImmediately=true` the server answers with the **earliest usable
result** — the first persisted task snapshot or the first Message — and the
work continues; the client follows up via `tasks/get` or `tasks/resubscribe`.

> **v0.2.x note**: the old wire had the *opposite* default — `blocking` was
> optional and absent meant "answer immediately". v1.0 inverted it. The
> compat layer preserves the old default for legacy clients; migrating
> clients must opt in explicitly (see the
> [migration guide](../README.md#migrating-from-v0x)).

### Resubscribe semantics

`tasks/resubscribe` is **snapshot + increments**, not event replay: the first
frame is the current full `Task` snapshot (state and accumulated artifacts —
anything missed while disconnected is absorbed into it), then live events
follow. Resubscribing to a terminal task is rejected; fetch the final result
with `tasks/get` instead.

## The canonical event paradigm

The typical shape of a task-producing round, and what is actually mandatory:

```
status  -> submitted     optional: creation implies submitted
status  -> working       conventional; the natural "accepted" signal
artifact-> chunk 1..N    append/lastChunk for chunks of one artifact
status  -> completed     REQUIRED to end well; carries final=true
```

Mandatory: end in a legal state (a terminal state, or a suspend state for
multi-turn), `final=true` on the closing status frame, artifact chunk flags.
Everything else — whether `submitted` is explicit, how many `working` frames,
whether progress text rides on status messages — is the agent's choice.

## Interaction flows at a glance

```
Pure conversation:   user Message ──► agent Message              (no task)

Standard task:       user Message ──► working ──► artifacts ──► completed

Multi-turn:          user Message ──► input-required  (task suspends)
                     user Message(same taskId) ──► ... ──► completed

Cancellation:        tasks/cancel ──► processor's ctx canceled ──► CANCELED

Reconnect:           tasks/resubscribe ──► Task snapshot ──► live events
```
