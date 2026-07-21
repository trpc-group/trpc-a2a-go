# 从 v0.x 迁移

本指南面向已经有 v0.x agent 的用户，目标是迁移到 A2A v1.0（`/v2` 模块）。如果你是新项目，直接读 [服务端](server.md) 和 [客户端](client.md) 即可；如果你要保留老客户端或移植旧 processor，本页按“概念变化 -> API 映射 -> 行为差异 -> 迁移清单”的顺序展开。

新 API 背后的运行时规则见 [服务端](server.md)，构建配方也在同一页。

## 迁移路线

建议按这个顺序做：

1. 先把服务端 processor 改到新的 `ProcessMessage(ctx, ec) (<-chan protocol.StreamEvent, error)` 签名。
2. 用 `taskmanager.NewTaskHandle(ctx, ec)` 保留旧代码里熟悉的 `UpdateTaskState`、`AddArtifact`、`Reply` 写法。
3. 移除 `BuildTask`、`SubscribeTask`、`CleanTask` 和所有显式 `taskID` 写入参数。
4. 明确每轮如何结束：终态、挂起态，或取消后关闭 channel。
5. 复核客户端响应时机：v1.0 `SendMessage` 默认阻塞；依赖早返回的调用要设置 `returnImmediately=true`。
6. 如果 v0.2.x 客户端还要继续用，在同一个 v1.0 server 上挂 `compat/v0` handler。

## 改了什么

v1.0 用**单一事件流契约**取代了**多出口回调**。在 v0.x 中，`ProcessMessage` 接收消息、一个 options 结构体和一个 `TaskHandler`，返回一个 `MessageProcessingResult`——它可以是 `Message`、订阅者流或任务，三种形状对应三种响应模式，由 agent 自行选择。agent 还持有部分任务生命周期：它调用 `BuildTask`、`SubscribeTask`、`CleanTask`，并把 `taskID` 穿过每一次写入。

在 v1.0 中，agent 就是一个方法：读一份**只读的请求快照**（`ExecContext`），返回**一个事件 channel**。其余一切归框架所有——懒创建任务、持久化、订阅者扇出——并从这一条流中**推导**出每种响应形状：`message/send` 的快照和 `message/stream` 的实时流都出自同一批发出的事件。agent 代码里不再有流式/非流式分支。

**迁移策略分两半：**

- **服务端代码必须迁移。** `ProcessMessage` 签名变了，`TaskHandler` 接口没了。熟悉的名字以一层薄兼容层（`TaskHandle`）的形式保留，所以大多数 v0.x 方法体——包括常见的全同步写法——都能靠机械改动完成迁移。
- **既有的 v0.x *客户端*照常工作，无需改动。** 把 `compat/v0` 挂到同一端点，legacy v0.2.x 客户端就与 v1.0 客户端并肩服务，走同一条认证链，并保留 legacy 默认行为。见下文「保持 v0.x 客户端可用」一节。

## 心智转变

三句话概括这次迁移：

1. **签名坍缩。** `(message, options, handle) -> result` 变成 `(ec) -> (<-chan event)`。你所*读*的一切都是 `ec` 上的字段；你所*产出*的一切都是返回 channel 上的事件。
2. **你不再挑选出口形状。** 没有 `Message` vs `StreamingEvents` vs 任务的抉择。你发事件，框架从中推导出一元结果和实时流。
3. **框架持有任务生命周期。** 没有 `BuildTask`、没有 `SubscribeTask`、没有 `CleanTask`、不用把 `taskID` 穿过每次写入。一个轮次恰好驱动一个任务；关闭 channel 即结束它。

### 迁移前（v0.x）/ 迁移后（v1.0）

同一个 processor——一个 `working` → artifact → `completed` 的任务轮次，外加对空输入的纯消息回复——在两代 API 下的样子。**迁移后**形式与 [examples/basic](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/basic)风格一致：一个基于 `TaskHandle` 的同步方法体。

**迁移前 —— v0.x（`MessageProcessor` + `TaskHandler`）：**

```go
func (p *myProcessor) ProcessMessage(
    ctx context.Context,
    message protocol.Message,
    options taskmanager.ProcessOptions,
    handle taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
    text := extractText(message)
    if text == "" {
        // A pure message reply — no task.
        reply := protocol.NewMessage(
            protocol.MessageRoleAgent,
            []*protocol.Part{protocol.NewTextPart("input message must contain text")},
        )
        return &taskmanager.MessageProcessingResult{Result: &reply}, nil
    }

    // Explicitly create the task, then drive it by ID.
    contextID := handle.GetContextID()
    taskID, err := handle.BuildTask(nil, &contextID)
    if err != nil {
        return nil, err
    }

    // Subscribe to obtain the stream to hand back.
    subscriber, err := handle.SubscribeTask(&taskID)
    if err != nil {
        return nil, err
    }

    go func() {
        handle.UpdateTaskState(&taskID, protocol.TaskStateWorking, nil)
        result := doWork(text)
        handle.AddArtifact(&taskID, protocol.Artifact{
            ArtifactID: "processed-" + taskID,
            Parts:      []*protocol.Part{protocol.NewTextPart(result)},
        }, true /* isFinal */, false /* needMoreData */)
        done := protocol.NewMessage(
            protocol.MessageRoleAgent,
            []*protocol.Part{protocol.NewTextPart(result)},
        )
        handle.UpdateTaskState(&taskID, protocol.TaskStateCompleted, &done)
    }()

    return &taskmanager.MessageProcessingResult{StreamingEvents: subscriber}, nil
}
```

**迁移后 —— v1.0（`MessageProcessor` + `TaskHandle`）：**

```go
func (p *myProcessor) ProcessMessage(
    ctx context.Context,
    ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
    handle := taskmanager.NewTaskHandle(ctx, ec)
    defer handle.Close()

    text := extractText(ec.Message)
    if text == "" {
        // A pure message reply: no task comes into existence this round.
        handle.Reply(protocol.NewAgentText("input message must contain text"))
        return handle.Events(), nil
    }

    // No BuildTask / SubscribeTask: the framework creates the task lazily on
    // the first task event and stamps the IDs. Emits before Events() never
    // block, so the body stays synchronous — no goroutine required.
    handle.UpdateTaskState(protocol.TaskStateWorking, nil)
    result := doWork(text)
    handle.AddArtifact(protocol.Artifact{
        ArtifactID: "processed-" + handle.TaskID(),
        Parts:      []*protocol.Part{protocol.NewTextPart(result)},
    }, true /* lastChunk */)
    handle.UpdateTaskState(protocol.TaskStateCompleted, protocol.NewAgentText(result))

    return handle.Events(), nil
}
```

`taskID` 参数、显式的 `BuildTask`/`SubscribeTask`、以及 `MessageProcessingResult` 包装全都消失。要做实时流式，把同样的方法体放进一个 goroutine，并以 `Events()` 作为返回表达式——底层那条 raw channel 才是真正的契约，示例见 [examples/simple](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/simple)。

## API 映射

### processor 签名

| v0.x | v1.0 |
| --- | --- |
| `ProcessMessage(ctx, message, options, handler) (*MessageProcessingResult, error)` | `ProcessMessage(ctx, ec *ExecContext) (<-chan protocol.StreamEvent, error)` |
| `message protocol.Message`（参数） | `ec.Message` |
| `MessageProcessingResult{Result: &msg}` | 发出该消息：`handle.Reply(&msg)`（或在 raw channel 上 `out <- &msg`） |
| `MessageProcessingResult{StreamingEvents: subscriber}` | 返回的 channel **就是**流：`return handle.Events(), nil` |
| `MessageProcessingResult`（类型） | 移除——关闭 channel 是唯一的出口；每种响应形状由框架推导 |

### `ProcessOptions`

| v0.x | v1.0 |
| --- | --- |
| `ProcessOptions`（类型） | 移除——其字段挪到了 `ExecContext` 上 |
| `ProcessOptions.Streaming` | 移除——一份 processor 同时服务 `message/send` 与 `message/stream`；框架推导各自结果 |
| `ProcessOptions.Blocking` | 移除——client 用 `returnImmediately` 控制时机，框架据此应用（注意默认值反转，见下文「响应时机」） |
| `ProcessOptions.HistoryLength` | 移除——框架把它应用到响应任务上 |
| `ProcessOptions.PushNotificationConfig` | `ec.PushConfig` |
| `ProcessOptions.AcceptedOutputModes` | `ec.AcceptedOutputModes` |
| `ProcessOptions.Tenant` | `ec.Tenant` |

### `TaskHandler` 动词

| v0.x | v1.0 |
| --- | --- |
| `TaskHandler`（接口） | 移除——读取来自 `ec`，写入走返回的 channel（或 `TaskHandle`） |
| `BuildTask(...)` | 移除——任务在首个任务事件时懒创建；ID 是 `ec.TaskID` / `handle.TaskID()` |
| `UpdateTaskState(taskID, state, msg)` | `handle.UpdateTaskState(state, msg)`（或在 raw channel 上发 `*protocol.TaskStatusUpdateEvent`）——不带 `taskID` 参数 |
| `AddArtifact(taskID, artifact, isFinal, needMoreData)` | `needMoreData=false` 时调用 `handle.AddArtifact(artifact, isFinal)`，`needMoreData=true` 时调用 `handle.AppendArtifact(artifact, isFinal)`；续块必须复用同一个 `ArtifactID` |
| `SubscribeTask(taskID)` | 移除——返回的 channel 就是流；订阅者扇出归框架所有 |
| `GetTask(taskID)` | `handle.GetTask()`——仅本轮的续跑快照，首轮为 `nil`；任意任务读取已取消 |
| `CleanTask(taskID)` | 移除——框架持有任务生命周期；默认不删除任何任务（设 `memory.WithTaskTTL` 以回收终态任务） |
| `GetContextID()` | `handle.GetContextID()` / `ec.ContextID` |
| `GetMessageHistory()` | `handle.GetMessageHistory()` / `ec.History`——一份按 manager 的 `MaxHistoryLength` 截断的**轮前快照**，不是实时存储读取 |
| `GetMetadata()` | 移除（在 v0.x memory 实现里它总是返回错误）；入站消息的 metadata 是 `ec.Message.Metadata` |

### 移除的类型与构造器

| v0.x | v1.0 |
| --- | --- |
| `taskmanager.TaskSubscriber` | 随回调设计一并移除 |
| `taskmanager.CancellableTask`（及 `CancellableTask.Cancel`） | 移除——取消即 `CancelTask` 取消 processor 的 `ctx` |
| `memory.NewTaskManager(processor, opts...)` | 形状不变 |
| `redis.NewTaskManager(...)` | `redis.NewTaskManager(processor, rdb, opts...)`——**注意参数顺序**为 `(processor, rdb)` |
| redis `NewTaskSubscriber` / `WithSubscriberSendHook` / `WithSubscriberBlockingSend` | 移除——fan-out 由 manager 管理；Redis 跨节点 `SubscribeToTask` 可通过 `redis.WithCrossNodeResubscribe(true)` 显式开启 |

### wire 方法名

v1.0 的 JSON-RPC 绑定使用 PascalCase 方法名。斜杠分隔的名字是 v0.2.x wire，仍由 `compat/v0` 服务。这影响的是 wire 上的客户端，不是 agent 代码。

| v0.2.x wire | v1.0 wire |
| --- | --- |
| `message/send` | `SendMessage` |
| `message/stream` | `SendStreamingMessage` |
| `tasks/get` | `GetTask` |
| `tasks/cancel` | `CancelTask` |
| `tasks/resubscribe` | `SubscribeToTask` |
| `tasks/pushNotificationConfig/set` / `get` / `list` / `delete` | `CreateTaskPushNotificationConfig` / `GetTaskPushNotificationConfig` / `ListTaskPushNotificationConfigs` / `DeleteTaskPushNotificationConfig` |
| `agent/getAuthenticatedExtendedCard` | `GetExtendedAgentCard` |
| _（v1.0 新增）_ | `ListTasks` |

### A2A 错误码

框架映射为 JSON-RPC 错误的业务失败：

| 错误码 | 名称 | 触发时机 |
| --- | --- | --- |
| `-32001` | TaskNotFound | 对未知或已回收任务的 `GetTask`/`CancelTask` |
| `-32002` | TaskNotCancelable | 对已处于终态的任务 `CancelTask` |
| `-32003` | PushNotificationNotSupported | 对不支持推送的 agent 设置 push config |
| `-32004` | UnsupportedOperation | 本 agent 不支持该操作 |
| `-32005` | ContentTypeNotSupported | 内容类型不兼容 |
| `-32006` | InvalidAgentResponse | agent 产出了无效响应 |
| `-32603` | InternalError | 空轮次（未发任何事件）返回的就是它 |

## 需要核对的行为变化

以下都能顺利编译，但运行行为与 v0.x 不同。每条末尾给出一句**该怎么做**。

### 响应时机

- **阻塞默认值反转了。** 在 v1.0 中，不带 `returnImmediately` 的 `message/send` 会**等该轮结束**；v0.x 的 `blocking:false`（或缺省）是立即应答。→ *该怎么做：*若你依赖提前返回，就在那些调用上设 `returnImmediately=true`。经 `compat/v0` 服务的 legacy 客户端会自动保留 v0.x 默认。
- **空轮次是 bug。** 一个事件都不发的轮次会让 `message/send` 以 `-32603`（InternalError）失败。→ *该怎么做：* 关闭前至少发一个事件（一条 `Message`，或一个任务 status/artifact）。
- **内联 push config 是透传，不代注册。** 请求的 `configuration.pushNotificationConfig` 现在以 `ec.PushConfig` 到达；框架**不会**自动注册它。→ *该怎么做：* 若你的 agent 要发 webhook，自行兑现或注册它。

### 生命周期与轮次

- **只有关闭 channel 才结束轮次。** 永不关闭的轮次会永久占用任务的执行槽——后续请求被拒（"already has an active execution"）——并阻塞 manager `Close()` /server `Stop()`。→ *该怎么做：* 由发事件的 goroutine 负责关闭 channel（或 `TaskHandle`）；同步写法就是 `defer handle.Close()`。
- **在 `submitted`/`working` 状态关闭会把任务打成 `FAILED`。** 没有结论就收尾会被当作 processor 的 bug。→ *该怎么做：* 每个轮次都在终态（`completed`/`failed`/`canceled`/`rejected`）或挂起态（`input-required`/`auth-required`）下有意地结束。
- **纯消息回复不留任务。** `message/send` 不再总是产出任务；对该轮预分配 ID 的 `tasks/get` 返回 not-found。→ *该怎么做：* 别指望纯消息回复留下任务；若 client 必须追踪任务，先发一个任务事件。
- **一个轮次恰好驱动一个任务**（`ec.TaskID`）。携带其他任务 ID 的事件是违规，会让该轮任务失败。v0.x 一次调用可 `BuildTask` 多个任务。→ *该怎么做：* 让一个轮次守着它自己的任务；不同任务用不同的 send。
- **发 `*protocol.Task` 现在是违规。** 把任务快照作为流事件发出在 v0.x 是合法的；现在框架自己物化快照。→ *该怎么做：* 发 `TaskStatusUpdateEvent` /`TaskArtifactUpdateEvent`，绝不发 `*protocol.Task`。
- **`tasks/cancel` 返回取消前的快照**（可能仍是 `working`）；终态 `CANCELED` 在该轮收尾时落库。对已终态任务取消返回 `-32002`。→ *该怎么做：* 别假设 cancel 响应就是终态；再读一次任务、或观察流，以获取尘埃落定后的状态。
- **追加 artifact 现在使用语义明确的 `AppendArtifact`。** `AddArtifact(artifact, lastChunk)` 新增或替换 artifact，`AppendArtifact(artifact, lastChunk)` 追加续块。→ *该怎么做：* `needMoreData=false` 时调用 `AddArtifact`，`needMoreData=true` 时调用 `AppendArtifact`，并让所有续块复用同一个 `ArtifactID`。

### 多轮与续跑

- **`input-required`/`auth-required` 会让出轮次。** 发出挂起 status 会立刻释放任务以便续跑开始；老轮之后再发的事件一律**丢弃**。→ *该怎么做：* 完成由续跑轮交付；挂起后立即关闭 channel。
- **不带 `taskId` 的后续消息会开一个新任务。** 只按 `contextId` 建 session 会把挂起的任务搁死（memory 后端上它永不被回收）。→ *该怎么做：* 回应 `input-required` 提示时回带 `taskId`，让后续消息落到同一个任务上。

### 留存与读取

- **`GetMessageHistory` 是轮前快照**（`ec.History`），不是实时存储读取，且按 manager 的 `MaxHistoryLength` 截断。→ *该怎么做：* 把它当作轮次开始前捕获的一次性状态；别指望它反映轮次进行中发生的写入。
- **`GetTask()` 不带参数，只返回本轮的任务**（首轮为 `nil`）。从 processor 内部读取任意任务的能力已取消。→ *该怎么做：* 续跑快照用 `ec.Task` / `handle.GetTask()`；要读其他任务，从 processor 外部经 `TaskManager` API 读取。

这些规则背后的完整运行时规则见 [服务端](server.md)。

## 保持 v0.x 客户端可用

迁移服务端无需动你的客户端。用 `server.WithCompatHandler` 把 [compat/v0](https://github.com/trpc-group/trpc-a2a-go/tree/v2/compat/v0) handler 挂到同一个 JSON-RPC 端点上，未经改动的 v0.2.x 客户端就照常工作：

```go
import (
    v0 "trpc.group/trpc-go/trpc-a2a-go/v2/compat/v0"
    "trpc.group/trpc-go/trpc-a2a-go/v2/server"
    "trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

tm, err := memory.NewTaskManager(&myProcessor{})
if err != nil {
    log.Fatalf("Failed to create task manager: %v", err)
}

// One TaskManager, two wire protocols. The legacy v0.2.x method names are
// disjoint from the v1.0 names, so both client generations are served on the
// same endpoint, inside the same authentication chain.
srv, err := server.NewA2AServer(tm,
    server.WithAgentCard(agentCard),
    server.WithCompatHandler(v0.NewJSONRPCHandler(tm)),
)
```

agent 本身只写一次，基于 v1.0 的 `MessageProcessor` 契约；compat handler 负责在 legacy wire 与它之间来回翻译。关键在于，**legacy 路径上保留了 v0.x 的非阻塞默认**：不带 configuration 的 legacy `message/send` 仍然立即返回，尽管原生 v1.0 的 `message/send` 现在默认阻塞。可运行的服务端与 legacy-wire 客户端见 [examples/compat](https://github.com/trpc-group/trpc-a2a-go/tree/v2/examples/compat)。

## 迁移清单

- [ ] 把 `ProcessMessage` 签名改为 `(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error)`。
- [ ] 从 `ec` 上读消息与选项（`ec.Message`、`ec.AcceptedOutputModes`、`ec.Tenant`、`ec.PushConfig`、`ec.History`）。
- [ ] 去掉 `BuildTask`——任务在首个任务事件时懒创建；用 `ec.TaskID` /`handle.TaskID()`。
- [ ] 从 `UpdateTaskState` / `AddArtifact` 去掉 `taskID` 参数；`needMoreData=false` 时调用 `AddArtifact(artifact, isFinal)`，`needMoreData=true` 时调用 `AppendArtifact(artifact, isFinal)`，续块复用同一个 `ArtifactID`。
- [ ] 用 `return handle.Events(), nil` 取代 `SubscribeTask` +`MessageProcessingResult{StreamingEvents}`；用 `handle.Reply(&msg)` 取代 `MessageProcessingResult{Result: &msg}`。
- [ ] 由发事件的 goroutine 关闭 channel（`defer handle.Close()`），且始终在终态或挂起态下关闭——绝不把轮次停在 `submitted`/`working`。
- [ ] 回应 `input-required` 时回带 `taskId`，并由续跑轮交付完成。
- [ ] 删除 `GetMetadata` 与 `CleanTask`；核查 `GetMessageHistory`（现为快照）与 `GetTask`（仅本轮任务）的用法。
- [ ] 若构建 Redis manager，改用 `redis.NewTaskManager(processor, rdb, ...)`（注意参数顺序）。
- [ ] 复核响应时机：原生 v1.0 `message/send` 默认阻塞——在你依赖提前返回的地方传 `returnImmediately`。
- [ ] 若有 v0.2.x 客户端必须继续可用，加上 `server.WithCompatHandler(v0.NewJSONRPCHandler(tm))`。
- [ ] 设置 `memory.WithTaskTTL`（或 Redis TTL），让终态任务在生产环境被回收。
