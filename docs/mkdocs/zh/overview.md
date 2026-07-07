# 框架概览

tRPC-A2A-Go 是 A2A（Agent-to-Agent）协议 v1.0 的 Go 实现。它提供 A2A 对话的两端——把你的 agent 暴露出去的 **server**，和调用其他 agent 的 **client**——并接管两者之间一切有状态的东西（任务生命周期、流式、会话历史、鉴权、推送通知、多租户托管），所以你唯一要写的就是 agent 的逻辑。

A2A 协议本身见 [协议](protocol.md)；构建配方与运行时契约见 [服务端](server.md)与 [客户端](client.md)。

## 你能得到什么

| 能力 | 状态 | 说明 |
| --- | --- | --- |
| A2A v1.0 协议对象、任务生命周期、`Task \| Message` 结果 union | ✅ | spec 完整的对象与状态模型。 |
| JSON-RPC 传输绑定（HTTP POST + SSE） | ✅ | 本框架当前服务的 wire。 |
| gRPC / HTTP+JSON（REST）传输绑定 | 🗺️ 规划中 | spec 已定义；尚未实现——目前只有 JSON-RPC。 |
| 一份 processor 服务 `SendMessage` / `SendStreamingMessage` | ✅ | 一条代码路径服务一元与流式。 |
| 阻塞 send、`returnImmediately`、实时流式、`SubscribeToTask` | ✅ | 同一 agent 的四种客户端消费模式。 |
| 内存 & Redis 任务存储 | ✅ | 可插拔 `TaskManager` 接口；可自带。 |
| 鉴权：JWT · API key · OAuth2 | ✅ | 可链式 provider，服务端与客户端两侧。 |
| 推送通知（webhook），JWT + JWKS 签名 | ✅ | 用于离线、回调驱动的运行。 |
| 多租户托管，按租户 agent card | ✅ | 一个进程、多个 agent，按租户路由。 |
| legacy v0.2.x wire 兼容 | ✅ | `compat/v0`，同端点、同鉴权链。 |
| 遥测：OpenTelemetry 指标、TTFT 追踪 | ✅ | 可插拔 meter provider 与首 token 策略。 |
| Redis 后端的跨副本流式 | 🚧 部分 | 快照共享；实时事件扇出是进程内的。 |

## 架构

```mermaid
flowchart TB
    subgraph clients["客户端"]
        C1["A2A client v1.0"]
        C2["legacy v0.2.x client"]
    end
    subgraph server["server"]
        direction TB
        AUTH["鉴权链"]
        RPC["JSON-RPC + SSE 分发"]
        CARD["agent card / 发现"]
        COMPAT["compat/v0 handler"]
    end
    TM["TaskManager<br/>memory 或 redis"]
    MP["MessageProcessor<br/>你的 agent"]

    C1 --> AUTH
    C2 --> AUTH
    AUTH --> RPC
    RPC --> TM
    COMPAT -.-> TM
    TM --> MP
    MP -- "事件流" --> TM
```

三层，各司其职：

- **`server`** 终结 wire 层。它鉴权、提供 agent card 供发现、分发 JSON-RPC 方法与 SSE 流，并（可选地）在同一端口、同一鉴权链内挂载 legacy v0.2.x 端点。
- **`TaskManager`** 拥有一切有状态的东西：懒创建、轮次 close 规则、取消、会话历史、留存、订阅者扇出。`taskmanager/memory` 与 `taskmanager/redis` 实现该接口，你也可以自带。
- **`MessageProcessor`** 是你唯一要写的部分。它读一份只读的请求快照（`ExecContext`），返回一个事件 channel。这就是你的 agent。

## 请求生命周期

每个请求——一元或流式——都走同一条流水线：

```mermaid
sequenceDiagram
    participant Client
    participant Server as server（鉴权、分发）
    participant TM as TaskManager
    participant P as MessageProcessor

    Client->>Server: SendMessage / SendStreamingMessage
    Server->>Server: 鉴权、解析租户 + agent card
    Server->>TM: OnSendMessage / OnSendMessageStream
    TM->>P: ProcessMessage(ctx, ec)
    P-->>TM: <-chan events（working, artifact, completed…）
    Note over TM: 每个事件先持久化再广播、<br/>首个任务事件懒创建任务、<br/>应用轮次 close 规则
    TM-->>Server: 最终任务快照（一元）或实时事件流
    Server-->>Client: JSON-RPC 结果 / SSE 帧
```

核心思想：**你的 agent 就是一个发出事件流的方法，框架从中派生每一种响应形状。**你的代码里没有流式/非流式分支；`SendMessage` 阻塞并返回终态快照，`SendStreamingMessage` 实时转发事件，两者都出自同一个 `ProcessMessage`。你通过包一个 `TaskHandle` 来写它——见 [服务端](server.md)。

## 框架为你做了什么

框架的很大一部分，是那些你不必自己做的协议工作：

- **结果派生**——`SendMessage` 从你的 processor 发的事件里返回 `Task` 或 `Message`（sealed union）；`SendStreamingMessage` 实时转发事件。一份 processor，全部响应形状。
- **懒创建与持久化**——任务在首个任务事件时成立，每个事件先持久化再广播。
- **agent card 归一化**——card 同时带 v1.0 字段与其 v0.2.x 弃用镜像，一张 card 两代客户端都能读。
- **legacy 翻译**——`compat/v0` 把 v0.2.x 斜杠方法 wire 映射到同一个 `TaskManager`，保留老默认。

确切的运行时契约——轮次生命周期、取消、历史与留存语义——与 agent 作者指南放在一起，见 [服务端](server.md)。

## 路线图

- **gRPC 与 HTTP+JSON（REST）传输绑定**——spec 定义了三种；本框架目前服务 JSON-RPC 绑定，agent card 已经建模了多传输声明，以备另外两种落地。
- **Redis 跨副本流式**——实时事件扇出目前是进程内的；共享 pub/sub 桥是通向完整多副本流式的规划路径。

## 下一步去哪

- [协议](protocol.md)——学习 A2A 本身：agent card、四个 wire 对象、任务状态机、交互流程。
- [服务端](server.md)——构建 agent：server、processor、运行时契约，以及每项服务端能力。
- [客户端](client.md)——调用 agent：消费模式、任务管理、编排。
- [从 v0.x 迁移](migration.md)——移植既有 v0.x agent。
