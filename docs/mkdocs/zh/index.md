# tRPC-A2A-Go 文档

tRPC-A2A-Go 是 [A2A（Agent-to-Agent）协议](https://a2a-protocol.org/) v1.0 的 Go 实现——把 agent 接入 A2A 生态、以及调用其他 agent 所需的一切。

第一次来？先读[框架概览](overview.md)，然后按需选择路径：

| 你想… | 阅读 |
| --- | --- |
| 了解这个框架是什么、各部分如何组合 | [框架概览](overview.md) |
| 学习 A2A 协议——对象、任务生命周期、交互 | [协议](protocol.md) |
| 构建 agent——服务端、processor、存储、鉴权、租户 | [服务端](server.md) |
| 调用 agent——四种消费模式、任务管理 | [客户端](client.md) |
| 把既有 v0.x agent 迁移到 v1.0 | [从 v0.x 迁移](migration.md) |

所有内容都有可运行的程序支撑，见 [examples/](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples)：从 [examples/basic](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/basic)（`TaskHandle` 风格）或 [examples/simple](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/simple)（裸 channel 风格）开始。

```bash
go get trpc.group/trpc-go/trpc-a2a-go/v2
```
