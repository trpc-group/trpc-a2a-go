# 理解 A2A 协议

本页从使用者的视角讲 A2A：它解决什么问题、心智模型是什么、如何发现一个 agent、你和一个 agent 之间实际会发生哪些交互，然后才是正式的对象与 RPC 定义。

阅读方式：前半段帮你建立“会话、任务、消息、交付物”的心智模型；后半段是 reference，用于查字段、状态和 JSON-RPC 方法。本页使用 A2A v1.0 JSON-RPC wire 方法名；Go client 的方法名映射见 [客户端](client.md)。

## A2A 解决什么问题？

AI agent 正在变成服务：报告生成器、差旅预订助手、代码评审员——各自出自不同团队、不同框架、不同语言，甚至不同组织。A2A（Agent-to-Agent）就是让任意 client 无需了解 agent 怎么实现就能与之对话的开放协议。典型场景：

- **产品后端**调用一个远端 agent 拿答案；
- **长时任务**（生成报告、处理数据集）：流式进度、断线不丢、随时接回；
- **表单式交互**：agent 需要追问补充信息才能继续；
- **编排 agent** 把工作分发给多个专家 agent；
- **跨组织调用**：需要鉴权与 webhook 回调。

传输层刻意保持朴素：client 应用先取 agent 的 **agent card** 了解其身份、技能、能力与有序的 `supportedInterfaces`，再选择 JSON-RPC 或 HTTP+JSON，并用所选 interface 的 URL、binding、tenant 构造 client；两种绑定的流式响应都使用 SSE。trpc-a2a-go 实现 A2A **v1.0**（legacy v0.2.x wire 由 [compat/v0](https://github.com/trpc-group/trpc-a2a-go/tree/v2/compat/v0) 层继续支持）。

## 心智模型

A2A 的一切都挂在两个名词上：

- **会话**（`contextId`）——你与 agent 之间持续的交流，可以跨任意多个问题和任务；
- **任务**（`taskId`）——会话里一个被跟踪的工作单元，有可观察、可恢复、可取消的生命周期。

内容则恰好分两类：

> **Message 是"说话"，Artifact 是"交货"。** 提问、回答、澄清——是 `Message`；任务产出的报告、代码、数据——是 `Artifact`，挂在任务上。

```mermaid
flowchart LR
    subgraph Conversation["会话 (contextId)"]
        M1["Message: user"] --> M2["Message: agent"]
        subgraph Task["任务 (taskId)"]
            S["status: submitted -> working -> ..."]
            A1["Artifact: report.pdf"]
        end
    end
```

不是每次交流都会产生任务：一个即答问题可以只返回一条 `Message`，没有任务生命周期，也没有任务清理负担。注意这不等于“没有会话历史”；具体实现可以把请求消息和 agent 发出的 `Message` 事件放入同一个 `contextId` 的历史里。只有值得跟踪的工作才需要开任务；从那之后，agent 汇报的一切都是**事件**：状态更新（进度）或 artifact 更新（交付物分块）。

## 发现：Agent Card

在调用一个 agent 之前，client 需要知道它存在、部署在哪、能做什么、如何鉴权。这就是 **Agent Card**——agent 发布在 well-known 路径上的一份 JSON 文档：

```
GET https://agent.example.com/.well-known/agent-card.json
```

card 是整个协议的入口。client 取一次、得到所需的一切，然后开始交互。主要分区：

| 分区 | 字段 | 告诉 client 什么 |
| --- | --- | --- |
| **身份** | `name`、`description`、`version`、`provider`、`iconUrl`、`documentationUrl` | 这个 agent 是谁。 |
| **传输** | `supportedInterfaces[]`（每项：`url`、`protocolBinding`、`protocolVersion`、可选 `tenant`） | 在哪、以何种方式调用。第一项为首选。 |
| **能力** | `capabilities.streaming`、`.pushNotifications`、`.extendedAgentCard`、`.extensions` | 支持哪些可选特性。 |
| **技能** | `skills[]`（每项：`id`、`name`、`description`、`tags`、`examples`、`inputModes`、`outputModes`） | 实际能做什么，以离散公示的能力表达。 |
| **输入输出模态** | `defaultInputModes`、`defaultOutputModes` | 默认接受与产出的媒体类型（如 `"text"`）。 |
| **安全** | `securitySchemes`、`securityRequirements` | 如何鉴权（API key / HTTP / OAuth2 / OIDC / mTLS）。 |

一张 v1.0 原生最小 card 的样子：

```go
agentCard := server.AgentCard{
    Name:        "Text Reversal Agent",
    Description: "Reverses text input",
    Version:     "1.0.0",
    SupportedInterfaces: []server.AgentInterface{{
        URL:             "http://localhost:8080/",
        ProtocolBinding: "JSONRPC",
        ProtocolVersion: "1.0",
    }},
    Capabilities: server.AgentCapabilities{
        Streaming: boolPtr(true),
    },
    DefaultInputModes:  []string{"text"},
    DefaultOutputModes: []string{"text"},
    Skills: []server.AgentSkill{{
        ID:          "reverse",
        Name:        "Text Reverser",
        Description: stringPtr("Input: reverse hello → Output: olleh"),
        Tags:        []string{"text"},
    }},
}
```

如果你为了兼容旧代码仍填写 top-level `URL`，server 会把它归一化成 `supportedInterfaces` 项；新文档和新代码建议优先写 `supportedInterfaces`。

两个相关概念：

- **扩展 card**——agent 可以在 client **鉴权之后**提供一张更丰富的 card（不想公开的技能或细节）。wire 方法是 `GetExtendedAgentCard`，Go client 方法是 `GetAuthenticatedExtendedCard`；公开 card 用 `capabilities.extendedAgentCard` 声明这一点。
- **多传输**——`supportedInterfaces` 可列出多个绑定（JSON-RPC、gRPC、REST），在本框架里还可按租户列不同 URL；调用方按需自行选择并构造 client。
- **扩展（Extensions）**——URI 标识的协议扩展，agent 在 `capabilities.extensions` 中声明；client 按请求选入，agent 可把某个标为 `required`（未选入则报 `-32008`）。
- **签名**——card 可以经 JWS 签名（`signatures`），让 client 校验它未被篡改。

## 交互逻辑：一次一个场景

先用这张表选交互方式：

| 你需要 | 用什么 | 返回/观察到什么 |
| --- | --- | --- |
| 问一句、直接拿回答 | `SendMessage`，agent 返回 `Message` | 没有 `Task`，不能查询或取消。 |
| 提交任务并等到结束 | `SendMessage` 默认模式 | 最终 `Task` 快照，包含状态和 artifacts。 |
| 提交后马上返回 | `SendMessage` + `returnImmediately=true` | 最早可用的 `Task` 或 `Message`；任务可继续跑。 |
| 实时看进度 | `SendStreamingMessage` | SSE 事件流：status / artifact / message。 |
| 断线后接回任务 | `SubscribeToTask` | 先给当前 `Task` 快照，再给实时增量。 |
| 不在线也要拿进展 | push notification | 服务端回调 client 的 webhook。 |

### 1. 即问即答——完全没有任务

最简单的交流：`SendMessage`，agent 用一条纯 Message 应答。它不会创建 `Task`，因此不能用 `GetTask` 跟踪；如果带了 `contextId`，这条消息仍可成为会话历史的一部分。

```mermaid
sequenceDiagram
    participant C as Client
    participant A as Agent
    C->>A: SendMessage "法国的首都是哪里？"
    A-->>C: Message "巴黎。"
```

### 2. 带实时进度的跟踪任务

同样的请求换到流式端点：每个事件发生的瞬间就到达你这里。首个任务事件就是任务诞生的时刻。

```mermaid
sequenceDiagram
    participant C as Client
    participant A as Agent
    C->>A: SendStreamingMessage "生成 Q3 报告"
    A-->>C: status working
    A-->>C: artifact report.pdf（分块 1）
    A-->>C: artifact report.pdf（分块 2，lastChunk）
    A-->>C: status completed（终态）— 流结束
```

想一次调用等到底？普通 `SendMessage` 默认就是阻塞的，返回最终任务快照，artifacts 都在里面。

### 3. 不想等——先拿结果，稍后跟进

设 `returnImmediately=true`，服务端以最早可用的结果应答，工作继续跑。之后用 `GetTask` 轮询，或用 `SubscribeToTask` 接回——它的首帧永远是当前任务快照，离线期间错过的内容都已被快照吸收。

```mermaid
sequenceDiagram
    participant C as Client
    participant A as Agent
    C->>A: SendMessage（returnImmediately=true）
    A-->>C: Task {id, working}
    Note over A: 工作在服务端继续
    C->>A: GetTask {id}
    A-->>C: Task {working}
    C->>A: SubscribeToTask {id}
    A-->>C: Task 快照，然后是实时事件…
    A-->>C: status completed（终态）
```

要完全离线运行，注册 webhook（`CreateTaskPushNotificationConfig`），让服务端反过来通知你。

### 4. agent 需要你补充信息——多轮交互

无法继续时，agent 把任务停在 `input-required`（或 `auth-required`）并向你提问。你**带相同的 `taskId`** 发消息来恢复——这是多轮交互唯一的规则。不带 `taskId` 的后续消息会开一个全新任务，把旧任务永远晾在那里。

```mermaid
sequenceDiagram
    participant C as Client
    participant A as Agent
    C->>A: SendMessage "帮我订去东京的机票"
    A-->>C: Task {input-required} "哪一天出发？"
    C->>A: SendMessage "3 月 3 日"（同 taskId）
    A-->>C: Task {completed} + 订票 artifact
```

### 5. 改主意了——取消

`CancelTask` 请求 agent 停手。响应是请求时刻的任务快照（往往还是 `working`）；终态 `CANCELED` 在 agent 收尾时落定。已结束的任务不可取消（`-32002`）。

```mermaid
sequenceDiagram
    participant C as Client
    participant A as Agent
    C->>A: SendStreamingMessage "处理这个数据集"
    A-->>C: status working
    C->>A: CancelTask {id}
    A-->>C: Task {working} — 取消已受理
    A-->>C: status canceled（终态）— 流结束
```

---

## 协议定义参考

上述场景背后的正式定义。

### 四个 wire 对象

| 对象 | 角色 | 一句话 |
| --- | --- | --- |
| **Message** | **沟通** | 一轮对话。 |
| **Task** | **工作单元** | 有状态、可观察的记录。 |
| **TaskStatusUpdateEvent** | **进度** | 一次状态迁移的宣告。 |
| **TaskArtifactUpdateEvent** | **交付物** | 一个产出的 artifact（或分块）。 |

**Message**——必填 `messageId`、`role`（user/agent）、`parts`；可选 `taskId`（定向/续跑任务）、`contextId`（会话）、`referenceTaskIds`（引用相关任务而不恢复它们）、`extensions`、`metadata`。

**Task**——必填 `id`、`status`；另有 `contextId`、`artifacts[]`、`history[]`（任务执行期间交换的 messages；不保证每条都被持久化——留存策略由实现自定）、`metadata`。

**TaskStatusUpdateEvent**——`taskId`、`contextId`、`status`（一个 `TaskStatus`，携带 `state` 与可选的解释性 `message`）、`metadata`。v1.0 wire 没有显式的 `final` 字段：`state` 为终态（或中断态）的那一帧就是最后一帧，之后 SSE 流关闭。

**TaskArtifactUpdateEvent**——`taskId`、`contextId`、`artifact`，以及两个分块标志：`append`（本事件续上同一 artifact 的上一块，而非新起一块）与 `lastChunk`（最后一块）。

两个标识符把一切串起来：**`contextId`** 命名会话（缺席时服务端生成；一个会话跨多个任务），**`taskId`** 命名工作单元（回传它可恢复等待中的任务）。由此有一条 spec 规则：`contextId` 与目标任务不匹配的消息 agent **MUST 拒绝**。

多租户场景还有一个独立的 **`tenant`**：它选择本次请求要路由到哪个 agent，不等同于会话 ID，也不替代 `taskId`。

### Part：message 或 artifact 携带什么

message 和 artifact 都携带一个 **parts** 列表，所以一轮可以混合文本、文件与结构化数据：

| Part | 携带 | 构造器 |
| --- | --- | --- |
| **Text** | 一个 UTF-8 字符串 | `protocol.NewTextPart(text)` |
| **File** | 按 URL + 文件名 + 媒体类型引用文件（或原始字节） | `protocol.NewFilePart(url, name, mediaType)` / `NewRawPart(bytes, mediaType)` |
| **Data** | 任意结构化 JSON | `protocol.NewDataPart(value)` |

agent card 上的 `defaultInputModes` / `defaultOutputModes`，以及请求上的 `acceptedOutputModes`，协商流动哪些媒体类型。输出协商是建议性的；不支持的*输入*内容类型则是硬错误（`-32005`）。

### 任务状态机

```mermaid
stateDiagram-v2
    [*] --> submitted: 首个任务事件
    submitted --> working
    working --> completed
    working --> failed
    working --> canceled
    working --> rejected
    working --> suspended: input-required / auth-required
    suspended --> working: 带相同 taskId 的后续消息
    completed --> [*]
    failed --> [*]
    canceled --> [*]
    rejected --> [*]
```

| 状态（wire 枚举） | 类别 | 含义 |
| --- | --- | --- |
| `TASK_STATE_SUBMITTED` | 活跃 | 已受理，尚未开始 |
| `TASK_STATE_WORKING` | 活跃 | 正在处理 |
| `TASK_STATE_INPUT_REQUIRED` | 中断 | 需要 client 补充输入 |
| `TASK_STATE_AUTH_REQUIRED` | 中断 | 需要鉴权才能继续 |
| `TASK_STATE_COMPLETED` | 终态 | 成功完成 |
| `TASK_STATE_FAILED` | 终态 | 出错结束 |
| `TASK_STATE_CANCELED` | 终态 | 完成前被取消 |
| `TASK_STATE_REJECTED` | 终态 | agent 拒绝了该任务 |
| `TASK_STATE_UNSPECIFIED` | — | 状态不明的占位 |

**终态**不可变：终态任务不可再修改，订阅它会被拒绝。**中断**态暂停处理、等待 client 行动（一条后续消息，或鉴权）。

### RPC 面（v1.0）

v1.0 JSON-RPC 绑定定义的方法名如下（v0.2.x wire 用的是斜杠分隔名，列出以供对照）。Go client 为兼容历史保留了少量不同的方法名，例如 `StreamMessage` 对应 `SendStreamingMessage`，`GetTasks` 对应 `GetTask`。

| 方法（v1.0） | 类型 | 用途 | v0.2.x 名称 |
| --- | --- | --- | --- |
| `SendMessage` | 一元 | 发送消息；响应是 **`Task` 或 `Message`**（union）。默认**等待**该轮结束。 | `message/send` |
| `SendStreamingMessage` | SSE | 同样的请求；每个事件实时流出。 | `message/stream` |
| `GetTask` | 一元 | 取任务快照；`historyLength` 决定随附多少历史。 | `tasks/get` |
| `ListTasks` | 一元 | 枚举任务：可按 `contextId`/状态过滤、分页。 | —（v1.0 新增） |
| `CancelTask` | 一元 | 请求取消。 | `tasks/cancel` |
| `SubscribeToTask` | SSE | 接回运行中任务的事件流：先快照，后增量。 | `tasks/resubscribe` |
| `CreateTaskPushNotificationConfig` / `Get…` / `List…` / `Delete…` | 一元 | webhook 配置 CRUD，用于离线运行。 | `tasks/pushNotificationConfig/*` |
| `GetExtendedAgentCard` | 一元 | 鉴权后的扩展 agent card。 | `agent/getAuthenticatedExtendedCard` |

v1.0 spec 定义了三种功能等价的传输绑定——JSON-RPC、gRPC、HTTP+JSON/REST——各自有绑定特定的方法命名。本框架实现 JSON-RPC 与 HTTP+JSON；agent card 的有序 `supportedInterfaces` 声明一个 agent 提供哪些绑定及 endpoint URL，gRPC 仍在规划中。

### 请求配置

`SendMessage` 与 `SendStreamingMessage` 都使用 `SendMessageParams`。除 `message` 外，常用配置在 `configuration` 里：

| 字段 | 作用 |
| --- | --- |
| `returnImmediately` | 只影响 `SendMessage`。`false` 或缺省表示等待本轮到达终态/挂起态；`true` 表示最早可用结果出现后立即返回。 |
| `historyLength` | 控制响应里的 `Task.history` 带多少条历史。 |
| `acceptedOutputModes` | client 声明可接受的输出媒体类型，agent 可据此选择输出形式。 |
| `taskPushNotificationConfig` | 随本次请求内联携带 webhook 配置；框架会透传给 processor。 |

`returnImmediately` 的默认值是 v1.0 最容易踩的变化：

> 若为 `false`（**默认**），操作 MUST 等到任务到达终态（COMPLETED、FAILED、CANCELED、REJECTED）或中断态（INPUT_REQUIRED、AUTH_REQUIRED）后再返回。

> **v0.2.x 注**：老 wire 的默认值恰好*相反*——`blocking` 可选且缺席意味着"立即应答"。v1.0 把默认翻转了。compat 层为 legacy 客户端保留老默认；主动迁移的客户端必须显式选择（见[从 v0.x 迁移](migration.md)）。

### 错误映射

标准 JSON-RPC 码适用（`-32700` 解析错误、`-32600` 无效请求、`-32601` 方法不存在、`-32602` 参数无效、`-32603` 内部错误）。A2A 专用失败按 binding 映射；HTTP+JSON 返回 `google.rpc.Status` JSON body，并用 `google.rpc.ErrorInfo` 区分共享同一 HTTP status 的失败：

| 含义 | JSON-RPC 码 | HTTP status |
| --- | --- | --- |
| 任务未找到 | `-32001` | `404` |
| 任务不可取消（已终态） | `-32002` | `400` |
| 不支持推送通知 | `-32003` | `400` |
| 不支持该操作 | `-32004` | `400` |
| 内容类型不兼容 | `-32005` | `400` |
| agent 响应无效 | `-32006` | `500` |
| 未配置扩展 agent card | `-32007` | `400` |
| client 未选入某个必需的 extension | `-32008` | `400` |
| 请求的 A2A 协议版本不受支持 | `-32009` | `400` |

### 典型事件范式

常见输出分两类。

```
纯消息回复（不创建任务）：
message  -> direct reply

任务事件流（创建并推进任务）：
status   -> submitted      可选：创建即隐含 submitted
status   -> working        惯例；自然的"已受理"信号
artifact -> chunk 1..N     同一 artifact 的分块用 append/lastChunk
status   -> completed      必须：以合法状态收尾；终态即关闭流
```

强制项：产生任务后，要以合法状态结束（终态，或多轮场景的挂起态）并标注 artifact 分块；终态（或中断态）的那一帧即流的最后一帧，之后 SSE 流关闭。其余——要不要显式 `submitted`、发几帧 `working`、进度文字挂不挂在 status message 上——都由 agent 自定。

实现层面还有一个重要边界：`TaskStatus.message` 更适合放进度解释，它会被下一个 status 覆盖；需要跨轮进入会话历史的最终回答，应作为独立 `Message` 事件发出。详见 [服务端](server.md)。

下一步：[服务端](server.md) 讲本框架如何把这条事件流变成持久化任务与派生响应；[客户端](client.md) 展示如何从 Go 代码消费这些响应。
