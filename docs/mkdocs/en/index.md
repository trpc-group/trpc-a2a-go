# tRPC-A2A-Go Documentation

tRPC-A2A-Go is the Go implementation of the [A2A (Agent-to-Agent)
protocol](https://a2a-protocol.org/), v1.0 — everything you need to expose an
agent to the A2A ecosystem and to call other agents.

New here? Read the [Overview](overview.md) first, then pick a path:

| If you want to… | Read |
| --- | --- |
| Understand what the framework is and how it fits together | [Overview](overview.md) |
| Learn the A2A protocol — objects, task lifecycle, interactions | [Protocol](protocol.md) |
| Reason about your agent's runtime behavior in production | [Behavior](behavior.md) |
| Write code — servers, processors, clients, auth, Redis, tenants | [Usage](usage.md) |
| Port an existing v0.x agent to v1.0 | [Migrating from v0.x](migration.md) |

Everything is backed by runnable programs in
[examples/](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples): start
with [examples/basic](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/basic)
(the `TaskHandle` style) or
[examples/simple](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/simple)
(the raw channel style).

```bash
go get trpc.group/trpc-go/trpc-a2a-go/v2
```
