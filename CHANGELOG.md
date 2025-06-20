# Changelog

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


