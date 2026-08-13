# tRPC-A2A-Go 文档

tRPC-A2A-Go 是 [A2A（Agent-to-Agent）协议](https://a2a-protocol.org/) v1.0 的 Go 实现。你可以用它把自己的 agent 暴露为标准 A2A 服务，也可以作为客户端调用其他 agent；框架负责协议对象、任务生命周期、SSE 流、会话历史、鉴权、推送通知、多租户托管和 v0 兼容层。

```bash
go get trpc.group/trpc-go/trpc-a2a-go/v2
```

## 从哪里开始

| 你要做什么 | 先读 | 你会得到什么 |
| --- | --- | --- |
| 先判断这个库是否适合你的 agent | [框架概览](overview.md) | 能力矩阵、架构、请求生命周期和当前边界。 |
| 学清楚 A2A 的对象与交互模型 | [协议](protocol.md) | agent card、Message、Task、事件、状态机和 JSON-RPC 方法。 |
| 把 Go agent 发布为 A2A 服务 | [服务端](server.md) | `MessageProcessor` 写法、存储、鉴权、推送、多租户、部署选项。 |
| 从 Go 程序调用远端 agent | [客户端](client.md) | 发现 agent、四种消费模式、任务管理、推送配置和编排调用。 |
| 从 v0.x 升级到 v1.0 / `/v2` | [从 v0.x 迁移](migration.md) | API 映射、行为变化、兼容 v0 客户端的做法和迁移清单。 |

## 最短体验路径

想先跑起来，推荐从 `examples/basic` 开始：服务端用 `TaskHandle` 写法，客户端覆盖阻塞/流式调用、长任务启动、任务查询、订阅和取消。

```bash
# 终端 1：启动 server
cd examples/basic/server
go run main.go

# 终端 2：运行 client
cd examples/basic/client
go run main.go
```

新项目想直接理解底层事件流，可以看 `examples/simple`；它展示了裸 channel 风格的 `MessageProcessor`。生产能力相关示例见下表。

| 示例 | 重点能力 |
| --- | --- |
| [`examples/simple`](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/simple) | 裸 channel 最小示例；client 自动演示阻塞 send、`returnImmediately` 加轮询、streaming 和纯 Message。 |
| [`examples/basic`](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/basic) | `TaskHandle` 交互式 chat；长任务、Task 操作与跨用户 owner 隔离验证。 |
| [`examples/auth`](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/auth) | JWT、API key、鉴权用户与 `OwnerResolver` 接线。 |
| [`examples/jwks`](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/jwks) | push notification、JWT 签名、JWKS 校验。 |
| [`examples/redis`](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/redis) | Redis TaskManager、任务/会话持久化。 |
| [`examples/tenant`](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/tenant) | 一个进程托管多个 agent，按 `tenant` 路由。 |
| [`examples/subpath`](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/subpath) | 网关或子路径部署。 |
| [`examples/compat`](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/compat) | 同一端点同时服务 v1.0 与 legacy v0.2.x wire。 |
| [`examples/multi`](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/multi) | 一个 agent 内部调用多个下游 agent 做编排。 |
