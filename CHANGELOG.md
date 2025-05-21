# Changelog

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


