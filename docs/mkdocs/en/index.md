# tRPC-A2A-Go

tRPC-A2A-Go is the Go implementation of the A2A (Agent-to-Agent) protocol,
v1.0: everything needed to expose an agent to the A2A ecosystem and to call
other agents — server, client, task lifecycle management, streaming,
authentication, push notifications, multi-tenant hosting, and a compatibility
layer that keeps legacy v0.2.x clients working.

## Architecture

```
A2A client (v1.0) ─────────► ┌────────────────────────────────┐
                             │ server                         │
legacy v0.2.x client ──────► │   auth chain / agent cards     │
                             │   JSON-RPC + SSE               │
                             │   compat/v0 handler            │
                             └───────────────┬────────────────┘
                                             ▼
                             TaskManager (memory | redis)
                                             ▼
                             MessageProcessor  ◄── your agent
```

- **server** terminates the wire: authentication, agent-card discovery,
  JSON-RPC dispatch, SSE streaming, and (optionally) the legacy v0.2.x
  endpoint on the same port and auth chain.
- **TaskManager** owns everything stateful: lazy task creation, the round
  close rules, cancellation, conversation history, and subscriber fan-out.
  Memory and Redis backends ship in-tree.
- **MessageProcessor** is the only part you write: read a request snapshot
  (`ExecContext`), emit events on a channel. Two styles — the `TaskHandle`
  verb API or the raw channel — and one code path serves `SendMessage` and
  `SendStreamingMessage` alike.

## Feature highlights

- A2A **v1.0** wire protocol; sealed result unions, agent cards, tenants.
- **One processor, every consumption mode**: blocking send,
  `returnImmediately`, live streaming, and `SubscribeToTask` reattachment.
- **Authentication**: JWT / API key / OAuth2, chainable providers.
- **Push notifications** signed with JWT, keys served via a JWKS endpoint.
- **Multi-tenant hosting** with per-tenant agent cards.
- **Legacy compatibility**: unmodified v0.2.x clients via `compat/v0`, with
  the original wire defaults preserved.

## Documentation map

Suggested reading order:

| Document | What it covers |
| --- | --- |
| [protocol.md](protocol.md) | The A2A protocol itself: the wire objects (Message / Task / status / artifact), the task state machine, the interaction flows, and what the spec actually mandates. Read this to speak A2A. |
| [behavior.md](behavior.md) | How this framework behaves: the `MessageProcessor` contract, round lifecycle, cancellation, conversation & history semantics, retention, and the places where behavior is a deliberate choice on top of the spec. Read this to reason about your agent in production. |
| [usage.md](usage.md) | How to build with it: servers, the two processor styles, clients and the three consumption modes, auth, push notifications, multi-tenant, Redis, and serving legacy v0 clients — each section linked to a runnable example. Read this to write code. |

Two more entry points live outside this directory:

- **[Migrating from v0.x](https://github.com/trpc-group/trpc-a2a-go/blob/v2/README.md#migrating-from-v0x)** (root README) — the
  v0.x → v1.0 API mapping and the behavior changes to check when porting an
  existing agent.
- **[examples/](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples)** — runnable programs, one per topic. The two
  reference styles are [examples/basic](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/basic) (`TaskHandle`,
  minimal-edit port of a v0 processor) and
  [examples/simple](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/simple) (the raw channel contract).
