# Redis Coalesced Task Persistence Example

Demonstrates **`TaskPersistenceCoalesced`**: stream events are still **`XADD`’d on every chunk**, while **full `task:*` JSON snapshots** are flushed only on time/event thresholds (or before status / terminal transitions).

## Overview

| Component | Role |
|-----------|------|
| **Server** | Streams a **growing artifact** (many `AppendArtifact` chunks) through Redis TaskManager with coalesced snapshot writes |
| **Client** | Calls A2A `message/stream`, then **`tasks/get`** to verify the final persisted Task |

This example focuses on the **write path** (fewer large `SET task:*` operations). It does **not** exercise cross-pod **resubscribe** or snapshot-cursor replay; see `taskmanager/redis/persistence_test.go` and project docs for read-path behavior.

Compare with [`examples/redis`](../redis/README.md), which uses the default **`TaskPersistencePerEvent`** (full Task `SET` on every task-changing event).

### Persistence flow (coalesced)

```text
Each chunk:
  XADD stream event          (always, ~KB per event)
  merge full Task in memory
  skip SET task:*            (usually; PEXPIRE keys only)

On threshold or status/finish:
  SET full task:*
  SET task-snapshot-cursor:* (aligned with Stream tail in flush Lua)
```

No `artifact-parts:*` sidecar keys are used.

## Prerequisites

- Go 1.20+
- Redis 5.0+
- Run from the **`examples`** Go module directory

Start Redis:

```bash
docker run --rm -p 6379:6379 redis:7-alpine
redis-cli ping   # expect PONG
```

## Run the server

```bash
cd examples
go run ./redis-coalesced/server
```

Default workload **per request**:

| Flag | Default | Meaning |
|------|---------|---------|
| `-chunks` | 64 | Artifact chunks streamed |
| `-chunk-bytes` | 2048 | Text bytes per chunk |
| `-chunk-delay` | 10ms | Delay between chunks |
| `-flush-interval` | 200ms | Flush pending Task snapshot after stream quiet time |
| `-max-buffered-events` | 32 | Force flush after this many artifact events |
| `-stream-max-len` | 1000 | Approximate `XADD MAXLEN ~` per task stream |
| `-addr` | `:8081` | Listen address |
| `-redis-addr` | `localhost:6379` | Redis address |

Full flag example:

```bash
go run ./redis-coalesced/server \
  -addr :8081 \
  -public-url http://localhost:8081/ \
  -redis-addr localhost:6379 \
  -redis-password '' \
  -chunks 128 \
  -chunk-bytes 4096 \
  -chunk-delay 10ms \
  -flush-interval 200ms \
  -max-buffered-events 32 \
  -stream-max-len 1000
```

## Run the client

In another terminal:

```bash
cd examples
go run ./redis-coalesced/client
```

```bash
go run ./redis-coalesced/client \
  -addr http://localhost:8081/ \
  -text "generate a large streamed artifact" \
  -timeout 30s
```

The client prints Task snapshots, periodic artifact progress, and after the stream ends calls **`tasks/get`** to confirm stored artifact parts.

## Observe Redis writes

Use a third terminal:

```bash
redis-cli MONITOR
```

You should see:

- **`XADD`** on **every** artifact chunk (cross-node event journal unchanged)
- **`SET task:*`** only around flush thresholds and **status / completed** transitions
- **`SET task-snapshot-cursor:*`** updated together with each full Task snapshot flush
- **No** `HSET artifact-parts:*`

### Approximate PerEvent comparison

To approximate **`TaskPersistencePerEvent`** write frequency (not identical to `examples/redis`, but useful for MONITOR contrast):

```bash
go run ./redis-coalesced/server -max-buffered-events 1
```

With steady high-rate chunks, **`MaxBufferedTaskEvents`** dominates flush timing; **`SnapshotFlushInterval`** applies when the stream goes quiet.

### Heavier workload (closer to slow-query scenarios)

Larger artifacts increase merged Task JSON size and make coalescing benefits easier to see in MONITOR:

```bash
go run ./redis-coalesced/server -chunks 200 -chunk-bytes 8192 -max-buffered-events 32
```

## Related configuration (library)

```go
cfg := redis.CoalescedTaskPersistenceDefaults()
cfg.SnapshotFlushInterval = 200 * time.Millisecond
cfg.MaxBufferedTaskEvents = 32
cfg.FlushOnGetTask = false

manager, err := redis.NewTaskManager(
    processor,
    redisClient,
    redis.WithTaskPersistence(cfg),
    redis.WithTaskEventStreamMaxLen(1000),
)
```

Default **`TaskPersistencePerEvent`** behavior is unchanged unless `WithTaskPersistence` is set.
