# tRPC-A2A-Go 的行为

本页描述框架的运行时行为：你的 agent 代码遵循的契约，以及 client 观察到的语义。
协议背景见 [protocol.md](protocol.md)，代码配方见 [server.md](server.md) 与 [client.md](client.md)。

## MessageProcessor 契约

你的 agent 就是一个方法：

```go
type MessageProcessor interface {
    ProcessMessage(ctx context.Context, ec *ExecContext) (<-chan protocol.StreamEvent, error)
}
```

读一份**只读的请求快照**（`ExecContext`），返回一个事件 channel——任务的事件日
志。其余一切归框架所有：任务创建、持久化、订阅者扇出、以及每种响应形状的推导。
**一份 processor 同时服务 `SendMessage` 与 `SendStreamingMessage`**；agent 代码里没
有流式/非流式分支。

`ExecContext` 携带：`TaskID`（预分配）、`Task`（续跑轮的快照，首轮为 nil）、
`Message`、`ContextID`、`Tenant`、`History`（会话快照）、`AcceptedOutputModes`、
`PushConfig`（请求内联的 webhook 配置，透传——是否兑现由 agent 决定）。

## 轮次生命周期

一个**轮次**是一次 `ProcessMessage` 调用及其 channel 的排空过程。

- **懒创建**——首个*任务事件*落库时任务才成立。只发 Message 的轮次不留任务
  （对该轮预分配 ID 的 `GetTask` 返回 not-found）。
- **单活跃 run**——上一轮还在跑时，同一任务的第二条消息被拒
  （`-32602`，"already has an active execution"）。
- **关 channel 才是轮次结束**——且只有这一种结束方式。关闭时应用 close 规则：

  | 关闭时的任务状态 | 结果 |
  | --- | --- |
  | 终态（你发过了） | 正常结束 |
  | `input-required` / `auth-required` | 任务**挂起**，等待后续 |
  | `submitted` / `working` | **`FAILED`**——"processor finished without terminal state"（bug 信号） |
  | 期间收到过取消请求 | **`CANCELED`** |

- **挂起即让出**——发出 `input-required`/`auth-required` 后任务立刻被释放以便
  续跑开始；老轮之后再发的事件一律丢弃。完成要由续跑轮交付。
- **违规快速失败**——给别的 `taskId` 发事件、发 `*protocol.Task` 快照
  （v1.0 中仅框架可产生）、发无状态的 status，都会把该轮任务打成 `FAILED`
  并丢弃其余事件。
- **永不关闭的轮次是泄漏**——执行槽被永久占用（后续请求全被拒），manager
  `Close()` / server `Stop()` 会等它。务必由发事件的 goroutine 负责关闭。

## 执行与取消

- 轮次跑在**分离的 context** 上：client 断连**不会**取消工作；结果始终可经
  `GetTask`/`SubscribeToTask` 取回。
- 只有 `CancelTask`（和 manager 停机）会取消 processor 的 `ctx`。得体的反应
  是**停止发送并关闭**——框架落 `CANCELED`。取消*之后*发出的终态事件依然生效
  （被允许收尾的轮次就让它收尾）。
- `CancelTask` **返回取消请求时刻的快照**（可能仍是 `working`）；终态
  `CANCELED` 在该轮收尾时落库。取消已终态的任务返回 `-32002`（不可取消）。

## 响应推导

**`SendMessage`（阻塞，默认）**等该轮结束：碰过任务就答**任务快照**，否则答
**最后一条 Message**；一个事件都没发的轮次是 processor 的 bug（`-32603`）。

**`SendMessage` + `returnImmediately=true`** 以**最早可用结果**应答：首个*已
落库*的任务快照或首条 Message。注意以 Message 应答时不带 `taskId`——client 若
需要追踪任务，先发任务事件。

**`SendStreamingMessage`** 按序转发每个事件，且**先持久化再投递**（`GetTask` 永远
不落后于订阅者所见）。流在终态或挂起帧结束。

**`SubscribeToTask`** 先发当前完整任务快照、再发实时增量（Redis 后端在快照
边界上是 at-least-once）；终态任务被拒。订阅绑定请求——断连即在服务端清理。

## 会话、历史，以及什么会被记住

存储是两级的：**按 `messageId` 存消息本体**，按 `contextId` 存按时间有序的
**会话索引**。进入会话的只有：

- 每轮的**请求消息**，以及
- processor 发出的每个 **`Message` 事件**。**没有别的。**

特别地——这一点与官方 a2a SDK 不同（官方在每次状态迁移时把上一条
`status.message` 滚入 `task.history`）：

> **本实现中 status message 是易失的**（被下一个 status 覆盖、从不入库），
> **artifact 永不进入历史**。任何需要跨轮记住的内容——尤其是 LLM 的最终回
> 答——必须作为 `Message` 事件发出，否则下一轮的 `ec.History` 只有用户的发言。

（spec 本身对 `Task.history` 的承诺也只是"包含执行期间交换的 *messages*"，
并明确"不保证每条消息都被持久化"——留存策略由实现自定。）

`Task.history` 是虚拟的：从不持久化在任务上，响应时按请求的 `historyLength`
从会话现算（缺省 = 全量至 manager 上限，`<=0` = 不带）。`ec.History` 是轮次
开始前的快照，按 manager 的 `MaxHistoryLength`（默认 100）截断。

## 留存

| | memory 后端 | redis 后端 |
| --- | --- | --- |
| 会话 | 闲置超 `ConversationTTL`（默认 1h，清理器默认开）即清理；单会话上限 `MaxHistoryLength` 条 | key TTL（默认 1h），写时续期 |
| 终态任务 | **默认永久保留**（`TaskTTL`=0）——生产环境请设置 `memory.WithTaskTTL` | key TTL（默认 1h，`WithExpireTime`） |
| 挂起任务 | 永不回收（清理器只收终态）——请让 client 恢复或取消它们 | 随 key TTL 过期，包括合法挂起中的 |

没有按会话/按任务的删除 API；A2A 未定义此类接口。

## configuration 能力（`SendMessage` 的 configuration）

| 字段 | 消费方 | 效果 |
| --- | --- | --- |
| `returnImmediately` | 框架 | 响应时机（见上）；缺席 = 等待 |
| `historyLength` | 框架 | 响应任务随附多少历史 |
| `acceptedOutputModes` | 你的 processor（`ec.AcceptedOutputModes`） | 输出协商提示——不强制 |
| `taskPushNotificationConfig` | 你的 processor（`ec.PushConfig`） | 内联 webhook 配置，透传、不代注册 |

## 多租户与 legacy 客户端

- **多租户**：一个进程可承载多个 agent；请求的租户经 `ec.Tenant` 到达，按租户
  的 agent card 用 `server.WithTenantCard` 提供。见
  [examples/tenant](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/tenant)。
- **legacy v0.2.x 客户端**：用 `server.WithCompatHandler` 把 `compat/v0` 挂到
  同一端点——legacy 方法名与 v1.0 不相交，且 legacy 默认（尤其非阻塞的
  `message/send`）被保留。见
  [examples/compat](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/compat)。
