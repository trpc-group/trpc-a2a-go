# tRPC-A2A-Go

[![Go Reference](https://pkg.go.dev/badge/trpc.group/trpc-go/trpc-a2a-go.svg)](https://pkg.go.dev/trpc.group/trpc-go/trpc-a2a-go)
[![Go Report Card](https://goreportcard.com/badge/trpc.group/trpc-go/trpc-a2a-go)](https://goreportcard.com/report/trpc.group/trpc-go/trpc-a2a-go)
[![LICENSE](https://img.shields.io/badge/license-Apache--2.0-green.svg)](https://github.com/trpc-group/trpc-a2a-go/blob/main/LICENSE)
[![Releases](https://img.shields.io/github/release/trpc-group/trpc-a2a-go.svg?style=flat-square)](https://github.com/trpc-group/trpc-a2a-go/releases)
[![Tests](https://github.com/trpc-group/trpc-a2a-go/actions/workflows/prc.yml/badge.svg)](https://github.com/trpc-group/trpc-a2a-go/actions/workflows/prc.yml)
[![Coverage](https://codecov.io/gh/trpc-group/trpc-a2a-go/branch/main/graph/badge.svg)](https://app.codecov.io/gh/trpc-group/trpc-a2a-go/tree/main)

This is tRPC group's Go implementation of the [A2A protocol](https://google.github.io/A2A/), enabling different AI agents to discover and collaborate with each other.

## Table of Contents

- [Quick Start](#quick-start)
- [Examples](#examples)
  - [Simple Example](#1-simple-example-examplessimple)
  - [Streaming Examples](#2-streaming-examples-examplesstreaming)
  - [Basic Example](#3-basic-example-examplesbasic)
  - [Authentication Examples](#4-authentication-examples-examplesauth)
- [Creating Your Own Agent](#creating-your-own-agent)
- [Authentication](#authentication)
- [Session Management](#session-management)
- [Future Enhancements](#future-enhancements)
- [Contributing](#contributing)
- [Acknowledgements](#acknowledgements)

## Quick Start

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

# Use a specific session ID
go run main.go --session "your-session-id"
```

## Examples

The repository includes several examples demonstrating different aspects of the A2A protocol:

### 1. Simple Example ([examples/simple](examples/simple))

A minimal example demonstrating the core A2A functionality:
- Simple server that reverses text input
- Simple client that sends non-streaming requests
- Basic task lifecycle (submission, processing, completion)
- Text processing with artifacts

```bash
# Start the simple server
cd examples/simple/server
go run main.go

# Run the simple client
cd examples/simple/client
go run main.go

# Send a custom message
go run main.go --message "Text to be reversed"
```

### 2. Streaming Examples ([examples/streaming](examples/streaming))

Examples focused on streaming capabilities:
- Server implementation with streaming response support
- Client implementation for handling streaming data

```bash
# Start the streaming server
cd examples/streaming/server
go run main.go

# Run the streaming client
cd examples/streaming/client
go run main.go
```

### 3. Basic Example ([examples/basic](examples/basic))

A comprehensive example showcasing:
- A versatile text processing server with multiple operations
- A feature-rich CLI client with support for all core A2A protocol APIs
- Streaming and non-streaming modes
- Multi-turn conversations with session management
- Task management (create, cancel, get)
- Agent capability discovery

### 4. Authentication Examples ([examples/auth](examples/auth))

Complete examples demonstrating authentication:
- Server implementation with various authentication methods
- Client examples showing how to connect with different auth methods
- JWT, API key, and OAuth2 implementations
- Command-line options for all authentication parameters

```bash
# Start the authentication server with OAuth2 support enabled
cd examples/auth/server
go run main.go --enable-oauth true

# Run client with JWT authentication
cd examples/auth/client
go run main.go --auth jwt --jwt-secret "your-secret-key"

# Run client with API key authentication
go run main.go --auth apikey --api-key "test-api-key"

# Run client with OAuth2 authentication
go run main.go --auth oauth2 \
  --oauth2-client-id "my-client-id" \
  --oauth2-client-secret "my-client-secret"

# Run client with JWT from a file
go run main.go --auth jwt --jwt-secret-file "path/to/jwt-secret.key"

# Specify custom message and session ID
go run main.go --auth jwt --message "Custom message" --session-id "session123"
```

## Creating Your Own Agent

### 1. Implement the TaskProcessor Interface

This interface defines how your agent processes incoming tasks:

```go
import (
    "context"

    "trpc.group/trpc-go/trpc-a2a-go/protocol"
    "trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// Implement the TaskProcessor interface
type myTaskProcessor struct {
    // Optional: add your custom fields
}

func (p *myTaskProcessor) Process(
    ctx context.Context,
    taskID string,
    message protocol.Message,
    handle taskmanager.TaskHandle,
) error {
    // 1. Extract input data from message
    // 2. Process data, generate results
    // 3. Use handle to update task status and add artifacts

    // Processing complete, return nil for success
    return nil
}
```

### 2. Create an Agent Card

The agent card describes your agent's capabilities:

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

### 3. Create and Start the Server

Initialize the server with your task processor and agent card:

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

## Authentication

The tRPC-A2A-Go framework supports multiple authentication methods for securing communication between agents and clients:

### Supported Authentication Methods

- **JWT (JSON Web Tokens)**: Secure token-based authentication with support for audience and issuer validation
- **API Keys**: Simple key-based authentication using custom headers
- **OAuth 2.0**: Support for various OAuth2 flows, including:
  - Client Credentials flow
  - Password Credentials flow
  - Custom token sources
  - Token validation

### Server-Side Authentication

#### Adding Authentication to Your Server

```go
import (
    "time"
    
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

// OAuth2 token validation provider
oauth2Provider := auth.NewOAuth2AuthProviderWithConfig(
    nil,   // No config needed for simple validation
    "",    // No userinfo endpoint for this example
    "sub", // Default subject field for identifying users
)

// Chain multiple authentication methods
chainProvider := auth.NewChainAuthProvider(
    jwtProvider, 
    apiKeyProvider,
    oauth2Provider,
)

// Create the server with authentication
srv, err := server.NewA2AServer(
    agentCard,
    taskManager,
    server.WithAuthProvider(chainProvider),
)
```

#### Using Authentication Middleware

```go
// Create an authentication provider
jwtProvider := auth.NewJWTAuthProvider(secretKey, audience, issuer, tokenLifetime)

// Create middleware
authMiddleware := auth.NewMiddleware(jwtProvider)

// Wrap your handler
http.Handle("/protected", authMiddleware.Wrap(yourHandler))
```

### Client-Side Authentication

Create authenticated clients using the appropriate options:

```go
import (
    "trpc.group/trpc-go/trpc-a2a-go/client"
)

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

See the [examples/auth/client](examples/auth/client) directory for complete examples of using different authentication methods.

### Push Notification Authentication

The framework includes support for secure push notifications:

```go
// Create an authenticator for push notifications
notifAuth := auth.NewPushNotificationAuthenticator()

// Generate a key pair
if err := notifAuth.GenerateKeyPair(); err != nil {
    // Handle error
}

// Expose JWKS endpoint
http.HandleFunc("/.well-known/jwks.json", notifAuth.HandleJWKS)

// Enable JWKS endpoint when creating the server
srv, err := server.NewA2AServer(
    agentCard,
    taskManager,
    server.WithJWKSEndpoint(true, "/.well-known/jwks.json"),
)
```

## Session Management

The A2A protocol supports session management to group related tasks:

```go
// Client-side: Creating a task with session ID
sessionID := "your-session-id" // Or generate one with uuid.New().String()
taskParams := protocol.SendTaskParams{
    ID:        "task-123",
    SessionID: &sessionID,
    Message:   message,
}

// Server-side: Tasks with the same sessionID are recognized
// as belonging to the same conversation or workflow
```

This allows for:
- Grouping related tasks under a single session
- Multi-turn conversations across different task IDs
- Better organization and retrieval of task history

## Future Enhancements

- Persistent storage options for task history
- More utilities and helper functions
- Metrics and logging integrations
- Comprehensive test suite
- Advanced session management capabilities

## Contributing

Contributions and improvement suggestions are welcome! Please ensure your code follows Go coding standards and includes appropriate tests. See the [CONTRIBUTING.md](CONTRIBUTING.md) file for more details.

## Acknowledgements

This project's protocol design is based on Google's open-source A2A protocol ([original repository](https://github.com/google/A2A)), following the Apache 2.0 license. This is an unofficial implementation.
