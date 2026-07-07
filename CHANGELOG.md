# Changelog

## 2.0.0-alpha.1 (2026-07-07)

### A2A Specification Upgrade ([a2a spec v0.2.x](https://github.com/a2aproject/A2A/releases/tag/v0.2.0) -> [a2a spec v1.0](https://github.com/a2aproject/A2A/releases/tag/v1.0.0))

This is a breaking release: the module path moves to `/v2`, the JSON-RPC wire moves from slash-form v0.2.x method names to the v1.0 PascalCase methods, and the agent-authoring interface is redesigned around a single event-stream contract. Existing v0.2.x **clients** keep working unchanged via an in-tree compatibility layer. Full walkthrough: [Migrating from v0.x](README.md#migrating-from-v0x) (README) and the [docs site](docs/) (`docs/mkdocs/{en,zh}/migration.md`).

**v0.x stays maintained.** The v0.2.x line continues on the `main` branch, while v1.0 / `/v2` development lives on the `v2` branch. We will keep maintaining v0.x on `main` — bug fixes and compatible improvements — until the v1.x protocol is broadly adopted, so existing users are not forced to migrate on this release's schedule.

#### Highlights

- **Module path bumped to `trpc.group/trpc-go/trpc-a2a-go/v2`.** Every import needs the `/v2` suffix.
- **`MessageProcessor` redesigned around one method:** `ProcessMessage(ctx, *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error)`. One event stream now serves `SendMessage` (blocking or `returnImmediately`) and `SendStreamingMessage` alike — the framework derives each response shape instead of the processor branching on streaming vs. blocking. `taskmanager.NewTaskHandle(ctx, ec)` is the recommended way to write one: a helper over the same channel with familiar verbs (`UpdateTaskState`, `AddArtifact`, `Reply`, `Close`, `Events()`).
- **New v1.0 operations:** `ListTasks`, `ListTaskPushNotificationConfigs`, `DeleteTaskPushNotificationConfig`, and authenticated extended agent cards (`GetExtendedAgentCard` / `server.WithAuthenticatedExtendedCardHandler` / `client.GetAuthenticatedExtendedCard`).
- **`TaskPushNotificationConfig` flattened** onto one struct — v0.2.x nested the delivery details under a `pushNotificationConfig` object.
- **Spec-shaped agent cards and results:** `AgentCard.SupportedInterfaces` ([]AgentInterface, multi-transport declaration) alongside deprecated v0.2.x-mirror fields for old clients; `SendMessage` now returns a sealed `Task | Message` union (`SendMessageResponse`), while `SendStreamingMessage` returns sealed stream events (`StreamResponse`: status/artifact/task/message).
- **Legacy v0.2.x wire compatibility ships in-tree:** `compat/v0` mounts on the same server and auth chain (`server.WithCompatHandler(v0.NewJSONRPCHandler(tm))`) or is used standalone as a client (`v0.NewClient(...)`), preserving the old non-blocking default. See [examples/compat](examples/compat).
- **Tenant-native multi-agent hosting:** one process can host multiple agents routed by a `tenant` field on the request body rather than by URL path (`server.WithTenantCard` / `WithTenantCardProvider`). See [examples/tenant](examples/tenant) (renamed from `multi_endpoint`).
- **OpenTelemetry metrics and time-to-first-token (TTFT) tracking** (`server.WithTelemetryMeterProvider` / `WithTelemetryMeterProviderOptions` / `WithFirstTokenPolicy`).
- **All examples ported to the new contract**, plus a new [examples/compat](examples/compat) (dual v1.0/v0.2.x wire) example; `examples/simple/python_client` removed.
- **New bilingual documentation site** under `docs/` (English + Chinese): protocol semantics, server/client guides, and the migration guide, cross-linked to the examples.

#### Breaking Changes

+ trpc.group/trpc-go/trpc-a2a-go (module path)
    + Import path changed to trpc.group/trpc-go/trpc-a2a-go/v2 for every package.

+ trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager
    + MessageProcessor.ProcessMessage(ctx, message, options, handler) (*MessageProcessingResult, error): removed -> ProcessMessage(ctx, *ExecContext) (<-chan protocol.StreamEvent, error)
    + ProcessOptions: removed — fields moved onto ExecContext (Message, TaskID, ContextID, Task, History, Tenant, AcceptedOutputModes, PushConfig)
    + MessageProcessingResult: removed — closing the channel is the only outcome; the framework derives every response shape
    + TaskHandler (interface): removed — replaced by TaskHandle, a concrete helper over the returned channel (NewTaskHandle(ctx, ec))
    + TaskHandler.BuildTask / .SubscribeTask / .CleanTask / .GetMetadata: removed
    + TaskHandler.UpdateTaskState(taskID, state, msg): now TaskHandle.UpdateTaskState(state, msg) — no taskID argument
    + TaskHandler.AddArtifact(taskID, artifact, isFinal, needMoreData): now TaskHandle.AddArtifact(artifact, lastChunk) — needMoreData/append dropped
    + TaskHandler.GetTask(taskID): now TaskHandle.GetTask() — this round's continuation snapshot only, arbitrary-task reads removed
    + TaskSubscriber, CancellableTask (and .Cancel): removed
    + redis.NewTaskManager: argument order is now (processor, rdb, opts...)
    + redis NewTaskSubscriber / WithSubscriberSendHook / WithSubscriberBlockingSend: removed

+ trpc.group/trpc-go/trpc-a2a-go/v2/protocol
    + JSON-RPC method names: message/send -> SendMessage, message/stream -> SendStreamingMessage, tasks/get -> GetTask, tasks/cancel -> CancelTask, tasks/resubscribe -> SubscribeToTask, tasks/pushNotificationConfig/set -> CreateTaskPushNotificationConfig, tasks/pushNotificationConfig/get -> GetTaskPushNotificationConfig, agent/getAuthenticatedExtendedCard -> GetExtendedAgentCard
    + TaskPushNotificationConfig: flattened — URL/Token/Authentication/Metadata moved directly onto the struct (previously nested under PushNotificationConfig)
    + Blocking default inverted: SendMessage without configuration now waits for a terminal or interrupted state by default; the old fire-and-forget behavior is configuration.returnImmediately = true
    + TaskStatusUpdateEvent.Final: no longer a wire field (json:"-"); stream closure is signaled by terminal/interrupted state, not an explicit flag

+ trpc.group/trpc-go/trpc-a2a-go/v2/server
    + WithAgentCardHandler: removed — use WithAgentCard / WithTenantCard / WithTenantCardProvider
    + WithHTTPRouter, customRouter, the HTTPRouter interface: removed — integrate multi-agent routing via the tenant field, or mount Handler() into your own mux
    + WithMiddleWare: renamed to WithMiddleware

#### New (non-breaking)

- `server.WithAuthenticatedExtendedCardHandler`, `client.GetAuthenticatedExtendedCard`
- `server.WithJSONRPCEndpoint`, `server.WithTelemetryMeterProviderOptions`, `server.WithFirstTokenPolicy`
- `client.ListTasks`, `client.ListPushNotifications`, `client.DeletePushNotification`
- `taskmanager.OnListTasks`, `.OnPushNotificationList`, `.OnPushNotificationDelete`
- OpenTelemetry request-count/duration/TTFT metrics (#103)
- Hardened terminal-state subscriber cleanup and manager `Close` in the memory `TaskManager` (#105)

## 0.2.5 (2025-10-27)

- Support get agent card interface (#96)
- Support header config of each call (#95)
- Improve error code definition and error handling of server (#94)
- Fix type of RpcID changed in streaming handling (#92)
- Fix data race issue (#86)
- Support a2a extendedAgentCard (#81)

## 0.2.4 (2025-09-17)

- Add tunnel to optimize sse sending (#74)
- Enlarge channel size (#79)
- Set no default client timeout (#80)
- Downgrade golang version to 1.20 (#82)

## 0.2.3 (2025-08-15)

- Add metadata to TaskHandler interfaces (#70)
- Remove deprecated field (#66)
- Make bufio scanner buffer configurable (#73)

Breaking Changes:
+ trpc.group/trpc-go/trpc-a2a-go/protocol
    + MethodTasksPushNotificationGet: removed
    + MethodTasksPushNotificationSet: removed
    + MethodTasksSend: removed
    + MethodTasksSendSubscribe: removed
    + SendTaskParams: removed
    + TaskEvent: removed

+ trpc.group/trpc-go/trpc-a2a-go/client
    + (*A2AClient).SendTasks: removed
    + (*A2AClient).StreamTask: removed

+ trpc.group/trpc-go/trpc-a2a-go/taskmanager
    + (*MemoryTaskManager).OnSendTask: removed
    + (*MemoryTaskManager).OnSendTaskSubscribe: removed
    + TaskManager.OnSendTask: removed
    + TaskManager.OnSendTaskSubscribe: removed

+ trpc.group/trpc-go/trpc-a2a-go/taskmanager/redis
    + (*TaskManager).OnSendTask: removed
    + (*TaskManager).OnSendTaskSubscribe: removed


## 0.2.2 (2025-07-28)

- Redis TaskManager use UniversalClient interface (#59)
- Add client rpc method tasks/resubscribe (#61)
- Optimize middleware And Support multi a2a endpoint in one process (#64)

## 0.2.1 (2025-07-14)

- Fix jsonrpc ID generation（#41）
- Add official python sdk client example in examples/simple (#42)
- Fix issue on context id generation (#45)
- Fix task subscriber buf size option (#50)
- Add bloing option for task subscriber (#50)
- Fix typo of SubscribeTask (#54)
- Synchronize with latest a2a spec (#57)

Breaking Changes:
- Change the filed `Final` of  TaskStatusUpdateEvent from *bool to bool(#42)
- Task Subscriber Constructor like `NewMemoryTaskSubscriber` and `NewTaskSubscriber` add a option param (#45)
- Fix typo of  taskmanager.TaskHandler.Subscriber,  taskmanager.TaskHandler.SubScribeTask -> taskmanager.TaskHandler.SubscribeTask ($54)

## 0.2.0 (2025-06-24)

- Add MCP information to the README for improved documentation (#38)
- Add support for subpaths in the a2a-go server, enabling more flexible routing (#37)
- The status enum for status_update should be status-update (#36)

## 0.2.0-beta (2025-06-24)

### A2A Specification Upgrade ([a2a spec v0.1.0](https://github.com/google-a2a/A2A/releases/tag/v0.1.0) -> [a2a spec v0.2.0](https://github.com/google-a2a/A2A/releases/tag/v0.2.0))

#### Protocol Specification Changes

##### 1. Protocol Method Names

- `tasks/send` → `message/send`
- `tasks/sendSubscribe` → `message/stream`
- `tasks/pushNotification/set` → `tasks/pushNotificationConfig/set`
- `tasks/pushNotification/get` → `tasks/pushNotificationConfig/get`
- Added `agent/authenticatedExtendedCard` method
- Legacy methods retained for backward compatibility but deprecated

##### 2. A2A Core Data Structure Updates

Following the A2A specification upgrade, core data structures have been updated to align with the new protocol requirements. These changes include modifications to Task, Message, Part structures, file handling mechanisms, task states, agent card configurations, artifacts, and streaming events.

For detailed specification changes, refer to the official A2A specification comparison:
- **Previous Specification**: [A2A v0.1.0 JSON Schema](https://github.com/google-a2a/A2A/blob/v0.1.0/specification/json/a2a.json)
- **Current Specification**: [A2A v0.2.0 JSON Schema](https://github.com/google-a2a/A2A/blob/v0.2.0/specification/json/a2a.json)

#### Interface Evolution

TaskProcessor → MessageProcessor:
```go
// Old interface
type TaskProcessor interface {
    Process(ctx context.Context, taskID string, initialMsg protocol.Message, handle TaskHandle) error
}

// New interface  
type MessageProcessor interface {
    ProcessMessage(ctx context.Context, message protocol.Message, options ProcessOptions, taskHandler TaskHandler) (*MessageProcessingResult, error)
}
```

Key Changes:
- **Processing Model**: Task-driven → Message-driven processing
- **Parameters**: Removed `taskID`, added `ProcessOptions` for configuration
- **Return Type**: Simple `error` → Structured `*MessageProcessingResult` 
- **Handler Interface**: `TaskHandle` → `TaskHandler` (enhanced capabilities)

TaskHandler Interface Enhancement:
- **Method Evolution**: `GetSessionID()` → `GetContextID()` (A2A spec compliance)
- **New Capabilities**: Added `BuildTask()`, `GetTask()`, `SubScribeTask()`, `GetMessageHistory()`
- **Enhanced Parameters**: `AddArtifact()` now supports `taskID`, `isFinal`, `needMoreData`

TaskManager Interface Updates:
- **New Methods**: Added `OnSendMessage()`, `OnSendMessageStream()` (A2A 0.2.0 methods)
- **Updated Returns**: `OnResubscribe()` now returns `<-chan protocol.StreamingMessageEvent`
- **Backward Compatibility**: Legacy methods (`OnSendTask`, `OnSendTaskSubscribe`) deprecated but retained

Constructor Changes:
- `NewMemoryTaskManager(TaskProcessor)` → `NewMemoryTaskManager(MessageProcessor, ...MemoryTaskManagerOption)`

#### Implementation Updates

- **Memory TaskManager**: Completely restructured for specification compliance
  - Constructor signature changed: `NewMemoryTaskManager(TaskProcessor)` → `NewMemoryTaskManager(MessageProcessor, ...MemoryTaskManagerOption)`
  - Internal data structures reorganized:
    - `Messages` field: `map[string][]Message` → `map[string]Message`
    - `Tasks` field: `map[string]*Task` → `map[string]*MemoryCancellableTask`
    - `Subscribers` field: `map[string][]chan<- TaskEvent` → `map[string][]*MemoryTaskSubscriber`
  - Removed internal mutex fields (`MessagesMutex`, `TasksMutex`, etc.) for simplified synchronization
  - Added new types: `MemoryCancellableTask`, `MemoryTaskSubscriber`
  - Added configuration options: `MemoryTaskManagerOption`, `WithConversationTTL`, `WithMaxHistoryLength`

- **Redis TaskManager**: Restructured implementation to support specification requirements
  - Split into focused modules:
    - `redis_manager.go` - main TaskManager implementation
    - `redis_types.go` - Redis-specific type definitions
    - `redis_options.go` - configuration options
    - `redis_task_handle.go` - task handle implementation
  - Removed legacy files: `options.go`, `push_notification.go`, `task.go`, `redis.go`
  - Fixed `GetSessionID()` method support as required by specification (#30)

- **Interface Updates**: Updated interfaces to match specification requirements
  - **TaskManager Interface**: Added `OnSendMessage()` and `OnSendMessageStream()` methods
  - **TaskHandler Interface**: Renamed `GetSessionID()` to `GetContextID()` per specification
  - **MessageProcessor Interface**: Updated to support new processing requirements

- **Client & Server**: Updated implementations to support new protocol methods
  - Client updated for new endpoint support
  - Server route handlers updated for new methods
  - Request/response handling updated per specification


##### Client Code Updates

- Replace `client.SendTask()` calls with `client.SendMessage()`
- Replace `client.SendTaskSubscribe()` calls with `client.StreamMessage()`
- Update request/response structures for new Message-based APIs

##### Server Code Updates

- Update route handlers for new method names
- Implement new `OnSendMessage()` and `OnSendMessageStream()` handlers
- Update AgentCard configuration for new security fields

#### Deprecated Methods

##### Legacy TaskManager Methods

**OnSendTask()** (Deprecated → Use OnSendMessage()):
- Legacy method for `tasks/send` protocol method
- Replaced by `OnSendMessage()` for `message/send` method

**OnSendTaskSubscribe()** (Deprecated → Use OnSendMessageStream()):
- Legacy method for `tasks/sendSubscribe` protocol method  
- Replaced by `OnSendMessageStream()` for `message/stream` method

These methods remain functional for backward compatibility but are deprecated in favor of the new A2A 0.2.0 specification methods.

## 0.0.3 (2025-05-21)

- Add `GetSessionId` to `TaskHandle` (#27)

## 0.0.2 (2025-04-18)

- Change agent card provider `name` to `organization` 

## 0.0.1 (2025-04-18)

- Initial release

### Features

- Implemented A2A protocol core components:
  - Complete type system with JSON-RPC message structures.
  - Client implementation for interacting with A2A servers.
  - Server implementation with HTTP endpoints handler.
  - In-memory task manager for task lifecycle management.
  - Redis-based task manager for persistent storage.
  - Flexible authentication system with JWT and API key support.

### Client Features

- Agent discovery capabilities.
- Task management (send, get status, cancel).
- Streaming updates subscription.
- Push notification configuration.
- Authentication support for secure connections.

### Server Features

- HTTP endpoint handlers for A2A protocol.
- Request validation and routing.
- Streaming response support.
- Agent card configuration for capability discovery.
- CORS support for cross-origin requests.
- Authentication middleware integration.

### Task Management

- Task lifecycle management.
- Status transition tracking.
- Resource management for running tasks.
- Memory-based implementation for development.
- Redis-based implementation for production use.

### Authentication

- Multiple authentication scheme support.
- JWT authentication with JWKS endpoint.
- API key authentication.
- OAuth2 integration.
- Chain authentication for multiple auth methods.

### Examples

- Basic text processing agent example.
- Interactive CLI client.
- Streaming data client sample.
- Authentication server demonstration.
- Redis task management implementation.

