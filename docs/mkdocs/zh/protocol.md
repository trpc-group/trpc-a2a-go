# A2A 协议：对象与语义

A2A（Agent-to-Agent）是面向可互操作 AI agent 的开放协议：client（往往本身也是
agent）通过远端 agent 的 **agent card** 发现它，然后走 JSON-RPC 通信——一元调用
基于 HTTP POST，流式基于 SSE。trpc-a2a-go 实现 A2A **v1.0**；legacy v0.2.x wire
经由 [compat/v0](https://github.com/trpc-group/trpc-a2a-go/tree/v2/compat/v0)
层提供（见 [examples/compat](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/compat)）。

本页讲协议的对象模型与交互流程——这些词各自是什么意思、spec 真正强制的是什么。
本框架如何实现它们见 [behavior.md](behavior.md)。

## 四个 wire 对象

| 对象 | 角色 | 一句话 |
| --- | --- | --- |
| **Message** | **沟通** | 一轮对话：`role`（user/agent）、`parts`（text/file/data）、`messageId`，可选 `taskId`/`contextId`。指令、提问、回答。 |
| **Task** | **工作单元** | 有生命周期的持久记录：`id`、`contextId`、`status`、`artifacts[]`、`history[]`。由服务端创建，进入终态后不可变。 |
| **TaskStatusUpdateEvent** | **进度** | 宣告一次状态迁移；可挂一条解释性 `message`；`final=true` 标记该轮的最后一帧状态。 |
| **TaskArtifactUpdateEvent** | **交付物** | 承载一个 `Artifact`——带 parts 的命名产出。分块流式用 `append`（续上一块）与 `lastChunk`。 |

判断准则：**Message 是"说话"，Artifact 是"交货"**。给用户的解释、追问走
Message；任务产出的文档/代码/数据走 Artifact。不做被跟踪工作的应答就是一条
纯 Message——此时根本不存在任务。

两个标识符把一切串起来：

- **`contextId`** 命名一个会话。请求不带时由服务端生成；每个任务和消息都归属
  某个会话，一个会话可以跨多个任务。
- **`taskId`** 命名一个工作单元。带 `taskId` 的后续消息**续跑该任务**（只在其
  等待输入时有意义）；不带的消息则开启新任务。

## 任务状态机

```
            ┌──────────────────────────── 终态 ────────────────────────────┐
            │                                                              │
 submitted ──► working ──┬──► completed | failed | canceled | rejected     │
                         │        （一旦到达即不可变）                     │
                         └──► input-required | auth-required ── 挂起 ──────┘
                                     │
                                     └── 带相同 taskId 的后续 Message
                                         恢复该任务（开启新一轮）
```

- **终态不可变**：completed/failed/canceled/rejected 的任务不可再修改，
  订阅终态任务会被拒绝。
- **`input-required` / `auth-required` 挂起**任务：服务端在向 client 要东西。
  client 通过**带相同 `taskId`** 的消息恢复。不带 `taskId` 的后续消息是
  *新*任务——只按 `contextId` 键控会话会把挂起的任务搁浅。

## RPC 面（v1.0）

| 方法 | 类型 | 用途 |
| --- | --- | --- |
| `message/send` | 一元 | 发送消息；响应是 **`Task` 或 `Message`**（union）。默认**等待**该轮结束。 |
| `message/stream` | SSE | 同样的请求，但每个事件实时流出。 |
| `tasks/get` | 一元 | 取任务快照；`historyLength` 决定随附多少历史。 |
| `tasks/list` | 一元 | 枚举任务。 |
| `tasks/cancel` | 一元 | 请求取消。 |
| `tasks/resubscribe` | SSE | 断连后重新挂上运行中任务的事件流。 |
| `tasks/pushNotificationConfig/*` | 一元 | webhook 配置 CRUD，用于离线运行。 |

### 阻塞与 `returnImmediately`

`message/send` 可带 `configuration.returnImmediately`：

> 若为 `false`（**默认**），操作 MUST 等到任务到达终态（COMPLETED、FAILED、
> CANCELED、REJECTED）或中断态（INPUT_REQUIRED、AUTH_REQUIRED）后再返回。

`returnImmediately=true` 时服务端以**最早可用的结果**应答——首个已持久化的任务
快照或首条 Message——工作继续进行；client 之后用 `tasks/get` 或
`tasks/resubscribe` 跟进。

> **v0.2.x 注**：老 wire 的默认值恰好*相反*——`blocking` 可选且缺席意味着
> "立即应答"。v1.0 把默认翻转了。compat 层为 legacy 客户端保留老默认；主动
> 迁移的客户端必须显式选择（见[迁移指南](https://github.com/trpc-group/trpc-a2a-go/blob/v2/README.md#migrating-from-v0x)）。

### resubscribe 语义

`tasks/resubscribe` 是**快照 + 增量**，不是事件重放：首帧是当前完整 `Task`
快照（状态与已积累的 artifacts——断连期间错过的内容都被它吸收），之后是实时
事件。订阅终态任务会被拒绝；最终结果请走 `tasks/get`。

## 典型事件范式

产生任务的一轮通常长这样，以及其中真正强制的部分：

```
status  -> submitted     可选：创建即隐含 submitted
status  -> working       惯例；自然的"已受理"信号
artifact-> chunk 1..N    同一 artifact 的分块用 append/lastChunk
status  -> completed     必须：以合法状态收尾；带 final=true
```

强制项：以合法状态结束（终态，或多轮场景的挂起态）、收尾状态帧带
`final=true`、artifact 分块标志。其余——要不要显式 `submitted`、发几帧
`working`、进度文字挂不挂在 status message 上——都由 agent 自定。

## 交互流程一览

```
纯对话：       user Message ──► agent Message                （无任务）

标准任务：     user Message ──► working ──► artifacts ──► completed

多轮：         user Message ──► input-required   （任务挂起）
               user Message（同 taskId）──► ... ──► completed

取消：         tasks/cancel ──► processor 的 ctx 被取消 ──► CANCELED

重连：         tasks/resubscribe ──► Task 快照 ──► 实时事件
```
