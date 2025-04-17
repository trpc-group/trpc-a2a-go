# tRPC-tRPC-A2A-go

[![Go Reference](https://pkg.go.dev/badge/github.com/trpc-group/trpc-a2a-go.svg)](https://pkg.go.dev/github.com/trpc-group/trpc-a2a-go)
[![Go Report Card](https://goreportcard.com/badge/github.com/trpc-group/trpc-a2a-go)](https://goreportcard.com/report/github.com/trpc-group/trpc-a2a-go)
[![LICENSE](https://img.shields.io/badge/license-Apache--2.0-green.svg)](https://github.com/trpc-group/trpc-a2a-go/blob/main/LICENSE)
[![Releases](https://img.shields.io/github/release/trpc-group/trpc-a2a-go.svg?style=flat-square)](https://github.com/trpc-group/trpc-a2a-go/releases)
[![Tests](https://github.com/trpc-group/trpc-a2a-go/actions/workflows/prc.yml/badge.svg)](https://github.com/trpc-group/trpc-a2a-go/actions/workflows/prc.yml)
[![Coverage](https://codecov.io/gh/trpc-group/trpc-a2a-go/branch/main/graph/badge.svg)](https://app.codecov.io/gh/trpc-group/trpc-a2a-go/tree/main)

This is tRPC group's Go implementation of the [A2A protocol](https://google.github.io/A2A/), enabling different AI agents to discover and collaborate with each other.

## Core Components

Implemented core components include:

### 1. Protocol Support

- Complete implementation of the A2A protocol specification
- Includes JSON-RPC message structures (`jsonrpc`) and protocol types (`protocol`)
- Provides data types for task states, parts, artifacts, and more

### 2. Client (`client`)

- Implements interaction with A2A servers
- Supports sending tasks, getting task status, and canceling tasks
- Handles JSON-RPC request/response formatting
- Supports streaming task updates (SSE)

### 3. Server (`server`)

- Provides an HTTP server based on the standard library
- Handles JSON-RPC request routing
- Supports agent card discovery (`/.well-known/agent.json`)
- Implements Server-Sent Events (SSE) for streaming updates
- Includes authentication middleware support

### 4. Task Management (`taskmanager`)

- Defines interface for task lifecycle management
- Provides default in-memory implementation (`MemoryTaskManager`)
- Supports task status updates and notifications
- Provides `TaskProcessor` interface for custom processing logic

### 5. Authentication (`auth`)

- Flexible authentication middleware
- Supports JWT and API key authentication methods
- Chain authentication provider for multiple auth methods
- JWT-based push notification authentication
- JWKS endpoint for public key distribution

## Examples

The repository includes several examples demonstrating different aspects of the A2A protocol:

### 1. Basic Example (`examples/basic`)

A comprehensive example showcasing:
- A versatile text processing server with multiple operations
- A feature-rich CLI client with support for all core A2A protocol APIs
- Streaming and non-streaming modes
- Multi-turn conversations
- Task management (create, cancel, get)
- Agent capability discovery

### 2. Authentication Examples (`examples/auth`)

Complete examples demonstrating authentication:
- Server implementation with various authentication methods
- Client examples showing how to connect with different auth methods
- JWT, API key, and OAuth2 implementations

### 3. Streaming Examples (`examples/streaming`)

Examples focused on streaming capabilities:
- Server implementation with streaming response support
- Client implementation for handling streaming data

## Usage

### Running the Basic Server Example

```bash
# Start the example server on default port 8080
cd examples/basic/server
go run main.go

# Specify different host and port
go run main.go --host 0.0.0.0 --port 9000

# Disable streaming capability
go run main.go --no-stream

# Disable CORS headers
go run main.go --no-cors
```

### Using the Basic CLI Client

```bash
# Connect to a local agent
cd examples/basic/client
go run main.go

# Connect to a specific agent
go run main.go --agent http://localhost:9000/

# Specify request timeout
go run main.go --timeout 30s

# Disable streaming mode
go run main.go --no-stream
```

### Running Authentication Examples

```bash
# Start the authentication server
cd examples/auth/server
go run main.go

# Run authentication client examples
cd examples/auth/client
go run main.go
```

### Running Streaming Examples

```bash
# Start the streaming server
cd examples/streaming/server
go run main.go

# Run the streaming client
cd examples/streaming/client
go run main.go
```

## Creating Your Own Agent

1. Implement the `TaskProcessor` interface:

```go
import (
    "context"

    "trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// Implement the TaskProcessor interface
type myTaskProcessor struct {
    // Optional: add your custom fields
}

func (p *myTaskProcessor) Process(
	ctx context.Context,
	taskID string,
	message taskmanager.Message,
	handle taskmanager.TaskHandle,
) error {
	// 1. Extract input data from message
	// 2. Process data, generate results
	// 3. Use handle to update task status and add artifacts

    // Processing complete, return nil for success
    return nil
}
```

2. Create an agent card, describing your agent's capabilities:

```go
import (
    "trpc.group/trpc-go/trpc-a2a-go/server"
    "trpc.group/trpc-go/trpc-a2a-go/protocol"
)

// Helper function to create string pointers
func stringPtr(s string) *string {
    return &s
}

agentCard := server.AgentCard{
    Name: "My Agent",
    Description: stringPtr("Agent description"),
    URL: "http://localhost:8080/",
    Version: "1.0.0",
    Provider: &server.AgentProvider{
        Name: "Provider name",
    },
    Capabilities: server.AgentCapabilities{
        Streaming: true,
        StateTransitionHistory: true,
    },
    DefaultInputModes: []string{string(protocol.PartTypeText)},
    DefaultOutputModes: []string{string(protocol.PartTypeText)},
    Skills: []server.AgentSkill{
        {
            ID: "my_skill",
            Name: "Skill name",
            Description: stringPtr("Skill description"),
            Tags: []string{"tag1", "tag2"},
            Examples: []string{"Example input"},
            InputModes: []string{string(protocol.PartTypeText)},
            OutputModes: []string{string(protocol.PartTypeText)},
        },
    },
}
```

3. Create and start the server:

```go
import (
    "log"

    "trpc.group/trpc-go/trpc-a2a-go/server"
    "trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// Create the task processor
processor := &myTaskProcessor{}

// Create task manager, inject processor
taskManager, err := taskmanager.NewMemoryTaskManager(processor)
if err != nil {
    log.Fatalf("Failed to create task manager: %v", err)
}

// Create the server
srv, err := server.NewA2AServer(agentCard, taskManager)
if err != nil {
    log.Fatalf("Failed to create server: %v", err)
}

// Start the server
log.Printf("Agent server started on :8080")
if err := srv.Start(":8080"); err != nil {
    log.Fatalf("Server start failed: %v", err)
}
```

## Adding Authentication

To secure your A2A server, you can use the authentication providers:

```go
import (
    "trpc.group/trpc-go/trpc-a2a-go/auth"
    "trpc.group/trpc-go/trpc-a2a-go/server"
)

// Create a JWT authentication provider
jwtSecret := []byte("your-secret-key")
jwtProvider := auth.NewJWTAuthProvider(
    jwtSecret,
    "your-audience",
    "your-issuer",
    24*time.Hour, // token lifetime
)

// Or create an API key authentication provider
apiKeys := map[string]string{
    "api-key-1": "user1",
    "api-key-2": "user2",
}
apiKeyProvider := auth.NewAPIKeyAuthProvider(apiKeys, "X-API-Key")

// Create the server with authentication
srv, err := server.NewA2AServer(
    agentCard,
    taskManager,
    server.WithAuthProvider(jwtProvider), // or apiKeyProvider
)
```

## Push Notification Authentication

A2A supports authenticated push notifications:

```go
// Enable JWKS endpoint for push notifications
srv, err := server.NewA2AServer(
    agentCard,
    taskManager,
    server.WithJWKSEndpoint(true, "/.well-known/jwks.json"),
)
```

## Authentication

The trpc-a2a-go framework supports multiple authentication methods for securing communication between agents and clients:

### Supported Authentication Methods

- **JWT (JSON Web Tokens)**: Secure token-based authentication with support for audience and issuer validation.
- **API Keys**: Simple key-based authentication using custom headers.
- **OAuth 2.0**: Support for various OAuth2 flows, including:
  - Client Credentials flow
  - Password Credentials flow
  - Custom token sources

### Client Authentication

To create an authenticated client, use one of the authentication options:

```go
// JWT Authentication
client, err := client.NewA2AClient(
    "https://agent.example.com/",
    client.WithJWTAuth(secretKey, audience, issuer, tokenLifetime),
)

// API Key Authentication
client, err := client.NewA2AClient(
    "https://agent.example.com/",
    client.WithAPIKeyAuth("your-api-key", "X-API-Key"),
)

// OAuth2 Client Credentials
client, err := client.NewA2AClient(
    "https://agent.example.com/",
    client.WithOAuth2ClientCredentials(
        "client-id",
        "client-secret",
        "https://auth.example.com/token",
        []string{"scope1", "scope2"},
    ),
)
```

See the `examples/auth/client` directory for complete examples of using different authentication methods.

### Server Authentication

On the server side, implement authentication using middleware:

```go
// Create an authentication provider
jwtProvider := auth.NewJWTAuthProvider(secretKey, audience, issuer, tokenLifetime)

// Create middleware
authMiddleware := auth.NewMiddleware(jwtProvider)

// Wrap your handler
http.Handle("/protected", authMiddleware.Wrap(yourHandler))
```

For chaining multiple authentication methods:

```go
chainProvider := auth.NewChainAuthProvider(
    jwtProvider,
    apiKeyProvider,
    oauth2Provider,
)
```

### Push Notification Authentication

The framework supports JWT-based authentication for push notification endpoints with automatic key generation and JWKS publishing:

```go
// Create an authenticator
notifAuth := auth.NewPushNotificationAuthenticator()

// Generate a key pair
if err := notifAuth.GenerateKeyPair(); err != nil {
    // Handle error
}

// Expose JWKS endpoint
http.HandleFunc("/.well-known/jwks.json", notifAuth.HandleJWKS)
```

## Future Enhancements

- Persistent storage
- More utilities and helper functions
- Metrics and logging
- Comprehensive test suite

## Contributing

Contributions and improvement suggestions are welcome! Please ensure your code follows Go coding standards and includes appropriate tests. See the [CONTRIBUTING.md](CONTRIBUTING.md) file for more details.

## Acknowledgements

This project's protocol design is based on Google's open-source A2A protocol ([original repository](https://github.com/google/A2A)), following the Apache 2.0 license. This is an unofficial implementation.
