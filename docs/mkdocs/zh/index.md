# tRPC-A2A-Go

tRPC-A2A-Go 是 A2A（Agent-to-Agent）协议 v1.0 的 Go 实现：把 agent 接入 A2A
生态、以及调用其他 agent 所需的一切——服务端、客户端、任务生命周期管理、流
式、鉴权、推送通知、多租户托管，以及让 legacy v0.2.x 客户端继续工作的兼容层。

## 架构

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

- **server** 终结 wire 层：鉴权、agent card 发现、JSON-RPC 分发、SSE 流式，
  以及（可选地）在同一端口、同一鉴权链上提供 legacy v0.2.x 端点。
- **TaskManager** 拥有一切有状态的东西：懒创建、轮次 close 规则、取消、会话
  历史、订阅者扇出。内置 memory 与 Redis 两种后端。
- **MessageProcessor** 是你唯一要写的部分：读一份请求快照（`ExecContext`），
  往 channel 上发事件。两种风格——`TaskHandle` 动词 API 或裸 channel——一份
  代码同时服务 `SendMessage` 与 `SendStreamingMessage`。

## 功能亮点

- A2A **v1.0** wire 协议；sealed 结果 union、agent card、租户。
- **一份 processor，全部消费模式**：阻塞 send、`returnImmediately`、实时流
  式、`SubscribeToTask` 重连。
- **鉴权**：JWT / API key / OAuth2，provider 可链式组合。
- **推送通知** JWT 签名，公钥经 JWKS 端点分发。
- **多租户托管**，按租户提供 agent card。
- **legacy 兼容**：v0.2.x 客户端经 `compat/v0` 原样接入，老 wire 默认值保留。

## 文档地图

建议按以下顺序阅读：

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
