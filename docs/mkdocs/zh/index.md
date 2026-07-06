# tRPC-A2A-Go 文档

本目录介绍 trpc-a2a-go 所实现的 A2A 协议，以及如何基于它构建 agent。建议按以下顺序阅读：

| 文档 | 内容 |
| --- | --- |
| [protocol.md](protocol.md) | A2A 协议本身：wire 对象（Message / Task / status / artifact）、任务状态机、交互流程，以及 spec 真正强制的部分。读完它你就会"说" A2A。 |
| [behavior.md](behavior.md) | 本框架的行为：`MessageProcessor` 契约、轮次生命周期、取消语义、会话与历史语义、留存策略，以及在 spec 之上属于本实现有意选择的部分。读完它你能推断你的 agent 在生产环境中的表现。 |
| [usage.md](usage.md) | 如何动手：服务端、两种 processor 风格、客户端与三种消费模式、鉴权、推送通知、多租户、Redis，以及服务 legacy v0 客户端——每节都链接到可运行的示例。读完它就能写代码。 |

另外两个入口在本目录之外：

- **[从 v0.x 迁移](https://github.com/trpc-group/trpc-a2a-go/blob/v2/README.md#migrating-from-v0x)**（根 README）——
  v0.x → v1.0 的 API 对照表，以及移植既有 agent 时需要核对的行为变化清单。
- **[examples/](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples)** —— 可运行的示例程序，一个主题一个。两个风格参照分别是
  [examples/basic](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/basic)（`TaskHandle` 风格，v0 processor 的最小改动移植）和
  [examples/simple](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/simple)（裸 channel 契约）。
