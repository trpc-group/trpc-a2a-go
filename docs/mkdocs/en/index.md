# tRPC-A2A-Go Documentation

This directory documents the A2A protocol as trpc-a2a-go implements it, and
how to build agents on top of it. Suggested reading order:

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
