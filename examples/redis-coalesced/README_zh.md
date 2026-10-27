# Redis 合并快照持久化示例

演示 **`TaskPersistenceCoalesced`**：**每条 chunk 仍立即 `XADD` 流式事件**，**完整 `task:*` JSON** 仅在时间/条数阈值（或 status / 终态前）合并写入。

English version: [README.md](./README.md).

## 概览

| 组件 | 作用 |
|------|------|
| **Server** | 通过 Redis TaskManager 流式输出**不断增长的 Artifact**（多次 `AppendArtifact`），并启用合并快照 |
| **Client** | 调用 A2A `message/stream`，结束后 **`tasks/get`** 校验 Task 已完整落库 |

本示例侧重 **写路径**（减少大 `SET task:*`）。**不包含**跨 Pod **resubscribe**、snapshot cursor 补帧演示；读路径见 `taskmanager/redis/persistence_test.go` 及项目内说明文档。

与 [`examples/redis`](../redis/README_zh.md) 对照：后者使用默认 **`TaskPersistencePerEvent`**（每个 Task 变更事件都 SET 完整快照）。

### 持久化流程（Coalesced）

```text
每条 chunk：
  XADD 流式事件          （始终执行，单条约 KB 级）
  进程内合并完整 Task
  跳过 SET task:*        （多数情况，仅 PEXPIRE 续期）

达到阈值或 status/结束：
  SET 完整 task:*
  SET task-snapshot-cursor:*（flush Lua 中与 Stream 尾部对齐）
```

不创建 `artifact-parts:*` Sidecar。

## 前置条件

- Go 1.20+
- Redis 5.0+
- 在仓库 **`examples`** Go module 目录下运行

启动 Redis：

```bash
docker run --rm -p 6379:6379 redis:7-alpine
redis-cli ping   # 应返回 PONG
```

## 运行服务端

```bash
cd examples
go run ./redis-coalesced/server
```

默认**每个请求**：

| 参数 | 默认值 | 含义 |
|------|--------|------|
| `-chunks` | 64 | 流式 artifact 块数 |
| `-chunk-bytes` | 2048 | 每块文本字节数 |
| `-chunk-delay` | 10ms | 块间延迟 |
| `-flush-interval` | 200ms | Stream 静默该时长后刷 pending 快照 |
| `-max-buffered-events` | 32 | 累计多少条 artifact 事件后强制 flush |
| `-stream-max-len` | 1000 | 单 Task Stream 的近似 `MAXLEN ~` 上限 |
| `-addr` | `:8081` | 监听地址 |
| `-redis-addr` | `localhost:6379` | Redis 地址 |

完整参数示例：

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

## 运行客户端

另开终端：

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

客户端会打印 Task 快照、阶段性 artifact 进度；流结束后 **`tasks/get`** 核对持久化中的 artifact parts 数量。

## 观察 Redis 写入

第三个终端：

```bash
redis-cli MONITOR
```

预期现象：

- **每条** artifact chunk 都有 **`XADD`**（跨节点事件语义不变）
- **`SET task:*`** 主要在 flush 阈值附近及 **status / completed** 切换时
- 每次完整 Task flush 会更新 **`task-snapshot-cursor:*`**
- **没有** `HSET artifact-parts:*`

### 近似对比 PerEvent 写入频率

便于在 MONITOR 里对比「几乎每条都 SET task」的效果（与 `examples/redis` 不完全等同，但直观）：

```bash
go run ./redis-coalesced/server -max-buffered-events 1
```

持续高频 chunk 时，刷盘主要由 **`MaxBufferedTaskEvents`** 决定；**`SnapshotFlushInterval`** 在流式停顿后触发。

### 加重负载（更接近慢查询场景）

增大合并后的 Task JSON，更容易在 MONITOR 里看出合并写收益：

```bash
go run ./redis-coalesced/server -chunks 200 -chunk-bytes 8192 -max-buffered-events 32
```

## 库侧配置参考

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

未设置 `WithTaskPersistence` 时，仍为默认 **`TaskPersistencePerEvent`**，与升级前行为一致。
