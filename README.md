# tRPC-A2A-Go

[![Go Reference](https://pkg.go.dev/badge/trpc.group/trpc-go/trpc-a2a-go/v2.svg)](https://pkg.go.dev/trpc.group/trpc-go/trpc-a2a-go/v2)
[![Go Report Card](https://goreportcard.com/badge/github.com/trpc-group/trpc-a2a-go)](https://goreportcard.com/report/github.com/trpc-group/trpc-a2a-go)
[![LICENSE](https://img.shields.io/badge/license-Apache--2.0-green.svg)](https://github.com/trpc-group/trpc-a2a-go/blob/main/LICENSE)
[![Releases](https://img.shields.io/github/release/trpc-group/trpc-a2a-go.svg?style=flat-square)](https://github.com/trpc-group/trpc-a2a-go/releases)
[![Tests](https://github.com/trpc-group/trpc-a2a-go/actions/workflows/prc.yml/badge.svg)](https://github.com/trpc-group/trpc-a2a-go/actions/workflows/prc.yml)
[![Coverage](https://codecov.io/gh/trpc-group/trpc-a2a-go/branch/main/graph/badge.svg)](https://app.codecov.io/gh/trpc-group/trpc-a2a-go/tree/main)

This is tRPC group's Go implementation of the [A2A protocol](https://google.github.io/A2A/), enabling different AI agents to discover and collaborate with each other.

## Related Projects

tRPC AI ecosystem

+ [**trpc-agent-go**](https://github.com/trpc-group/trpc-agent-go) : A powerful Go framework for building intelligent agent systems with large language models (LLMs), hierarchical planners, memory, telemetry and a rich tool ecosystem. If you want to create autonomous or semi-autonomous agents that reason, call tools, collaborate with sub-agents and keep long-term state, [**tRPC-Agent-Go**](https://github.com/trpc-group/trpc-agent-go) has you covered.

+ [**trpc-mcp-go**](https://github.com/trpc-group/trpc-mcp-go) : If you're interested in **streamable MCP development**, check out [**trpc-mcp-go**](https://github.com/trpc-group/trpc-mcp-go) - our Go implementation of the Model Context Protocol (MCP) with streaming capabilities.

## Table of Contents

- [Quick Start](#quick-start)
- [Examples](#examples)
  - [Simple Example](#1-simple-example-examplessimple)
  - [Streaming Examples](#2-streaming-examples-examplesstreaming)
  - [Basic Example](#3-basic-example-examplesbasic)
  - [Authentication Examples](#4-authentication-examples-examplesauth)
- [Creating Your Own Agent](#creating-your-own-agent)
- [Migrating from v0.x](#migrating-from-v0x)
- [Authentication](#authentication)
- [Session Management](#session-management)
- [Telemetry Metrics](#telemetry-metrics)
- [Future Enhancements](#future-enhancements)
- [Contributing](#contributing)
- [Acknowledgements](#acknowledgements)
- [Copyright](#copyright)

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

### 1. Implement the MessageProcessor Interface

This interface defines how your agent processes incoming messages:

```go
import (
    "context"

    "trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
    "trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

// Implement the MessageProcessor interface: process one message and report
// progress by emitting events. The framework owns the task lifecycle (lazy
// creation, persistence, subscriber fan-out) and derives both the message/send
// result and the message/stream feed from these events — there is no
// streaming/non-streaming branch in your code.
//
// TaskHandle carries the familiar verbs over the event stream. A synchronous
// body works as-is (emits never block before Events()); for live streaming,
// run the same body in a goroutine. The raw channel underneath is the actual
// contract — see examples/simple-v2 for that style.
type myMessageProcessor struct {
    // Add your custom fields here
}

func (p *myMessageProcessor) ProcessMessage(
    ctx context.Context,
    ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
    handle := taskmanager.NewTaskHandle(ctx, ec)
    defer handle.Close()

    text := extractTextFromMessage(ec.Message)
    if text == "" {
        // A pure-message reply: no task comes into existence this round.
        handle.Reply(taskmanager.ReplyText("input message must contain text."))
        return handle.Events(), nil
    }

    // The framework creates the task lazily on this first task event and
    // stamps the event IDs from the ExecContext.
    handle.UpdateTaskState(protocol.TaskStateWorking, nil)

    result := reverseString(text)
    handle.AddArtifact(*protocol.NewArtifactWithID(
        stringPtr("Reversed Text"), nil,
        []*protocol.Part{protocol.NewTextPart(result)},
    ), true)

    // A terminal status ends the round; the message/send caller receives
    // this final task snapshot (with its artifacts).
    handle.UpdateTaskState(protocol.TaskStateCompleted, taskmanager.ReplyText("Processed: "+result))
    return handle.Events(), nil
}

func extractTextFromMessage(message protocol.Message) string {
    for _, part := range message.Parts {
        if text := part.TextContent(); text != "" {
            return text
        }
    }
    return ""
}

func reverseString(s string) string {
    runes := []rune(s)
    for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
        runes[i], runes[j] = runes[j], runes[i]
    }
    return string(runes)
}
```

### 2. Create an Agent Card

The agent card describes your agent's capabilities:

```go
import (
    "trpc.group/trpc-go/trpc-a2a-go/v2/server"
    "trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

// Helper function to create string pointers
func stringPtr(s string) *string {
    return &s
}

// Helper function to create bool pointers
func boolPtr(b bool) *bool {
    return &b
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
        Streaming: boolPtr(true),
    },
    DefaultInputModes:  []string{protocol.KindText},
    DefaultOutputModes: []string{protocol.KindText},
    Skills: []server.AgentSkill{
        {
            ID:          "text_processing",
            Name:        "Text Processing",
            Description: stringPtr("Process and transform text input"),
            InputModes:  []string{protocol.KindText},
            OutputModes: []string{protocol.KindText},
        },
    },
}
```

### 3. Create and Start the Server

Initialize the server with your task processor and agent card:

```go
import (
    "log"

    "trpc.group/trpc-go/trpc-a2a-go/v2/server"
    "trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

// Create the task processor
processor := &myMessageProcessor{}

// Create task manager, inject processor. For persistent storage, swap in
// redis.NewTaskManager(processor, redisClient) — the same MessageProcessor.
taskManager, err := memory.NewTaskManager(processor)
if err != nil {
    log.Fatalf("Failed to create task manager: %v", err)
}

// Create the server
srv, err := server.NewA2AServer(taskManager, server.WithAgentCard(agentCard))
if err != nil {
    log.Fatalf("Failed to create server: %v", err)
}

// Start the server
log.Printf("Agent server started on :8080")
if err := srv.Start(":8080"); err != nil {
    log.Fatalf("Server start failed: %v", err)
}
```

## Migrating from v0.x

The v1.0 (`/v2`) release replaces the multi-outcome `MessageProcessor` +
`TaskHandler` callback with the single event-stream contract shown above. The
familiar names survive: you still implement `MessageProcessor.ProcessMessage`,
and the former `TaskHandler` verbs live on as the `TaskHandle` compatibility
layer, so a v0.x processor body ports with minimal edits — including fully
synchronous bodies, which were the common v0.x style:

```go
func (p *myProcessor) ProcessMessage(
    ctx context.Context,
    ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
    handle := taskmanager.NewTaskHandle(ctx, ec)
    defer handle.Close()

    // The v0.x body, minus the taskID arguments. Emits made before
    // handle.Events() never block, so no goroutine is required.
    handle.UpdateTaskState(protocol.TaskStateWorking, nil)
    result := doWork(ec.Message)
    handle.AddArtifact(result.Artifact, true)
    handle.UpdateTaskState(protocol.TaskStateCompleted, taskmanager.ReplyText("done"))

    return handle.Events(), nil
}
```

Two reference examples: [examples/basic](examples/basic) is the minimal-edit
`TaskHandle` port of a v0.x processor; [examples/simple-v2](examples/simple-v2)
is the native channel style recommended for new code.

### API mapping

| v0.x | v1.0 |
| --- | --- |
| `ProcessMessage(ctx, message, options, handler)` | `ProcessMessage(ctx, execContext)` — the message, options and reads are all on `ExecContext` |
| `MessageProcessingResult{Result: &message}` | emit the message: `handle.Reply(&msg)` / `out <- &msg` |
| `MessageProcessingResult{StreamingEvents: subscriber}` | the returned channel is the stream |
| `ProcessOptions.Streaming` / `.Blocking` | gone — one processor serves `message/send` and `message/stream`; the framework derives each result |
| `ProcessOptions.HistoryLength` | gone — the framework applies it to responses |
| `ProcessOptions.PushNotificationConfig` | `ExecContext.PushConfig` |
| `ProcessOptions.AcceptedOutputModes` / `.Tenant` | `ExecContext.AcceptedOutputModes` / `.Tenant` |
| `TaskHandler.BuildTask` | gone — tasks are created lazily on the first task event; the ID is `ExecContext.TaskID` / `TaskHandle.TaskID()` |
| `TaskHandler.UpdateTaskState(taskID, state, msg)` | `TaskHandle.UpdateTaskState(state, msg)` (or emit a `protocol.TaskStatusUpdateEvent` on the raw channel) |
| `TaskHandler.AddArtifact(taskID, artifact, isFinal, needMoreData)` | `TaskHandle.AddArtifact(artifact, lastChunk)` — `needMoreData` had no v1.0 wire meaning and is dropped |
| `TaskHandler.SubscribeTask` / `.CleanTask` | removed — the framework owns fan-out and task lifecycle |
| `TaskHandler.GetContextID/GetMessageHistory/GetTask` | same names on `TaskHandle` (fields on `ExecContext`) |
| `TaskHandler.GetMetadata` | removed (it always returned an error in v0.x); message metadata is `ExecContext.Message.Metadata` |
| `taskmanager.TaskSubscriber` / `CancellableTask` | removed with the callback design |
| redis `NewTaskSubscriber` / `WithSubscriberSendHook` / `WithSubscriberBlockingSend` | removed — cross-replica streaming is out of scope for the built-in managers |

### Behavior changes to check

These compile fine but behave differently from v0.x:

- `message/send` no longer always materializes a task: a pure message reply
  leaves no task behind, and `tasks/get` for that round's ID returns not-found.
- A round that produces no event at all is treated as a processor bug:
  `message/send` fails with `-32603`.
- Closing the event channel while the task is `submitted`/`working` marks it
  `FAILED` — end a round deliberately, in a terminal or
  `input-required`/`auth-required` state.
- `tasks/cancel` returns the snapshot taken when cancellation was requested
  (possibly still `working`); the terminal `CANCELED` state is persisted when
  the processor's round winds down.

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
    
    "trpc.group/trpc-go/trpc-a2a-go/v2/auth"
    "trpc.group/trpc-go/trpc-a2a-go/v2/server"
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
    taskManager,
    server.WithAgentCard(agentCard),
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
    "trpc.group/trpc-go/trpc-a2a-go/v2/client"
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
    taskManager,
    server.WithAgentCard(agentCard),
    server.WithJWKSEndpoint(true, "/.well-known/jwks.json"),
)
```

## Session Management

The A2A protocol supports session management to group related messages and tasks:

```go
// Client-side: Sending a message with session ID
sessionID := "your-session-id" // Or generate one with uuid.New().String()
message := protocol.NewMessage(
    protocol.MessageRoleUser,
    []protocol.Part{protocol.NewTextPart("Hello, agent!")},
)
message.SessionID = &sessionID

// Send the message with the session ID
result, err := client.SendMessage(ctx, message, nil)
if err != nil {
    log.Fatalf("Failed to send message: %v", err)
}

// Server-side: Messages with the same sessionID are recognized
// as belonging to the same conversation or workflow
```

This allows for:
- Grouping related messages under a single session
- Multi-turn conversations across different message exchanges
- Better organization and retrieval of conversation history

## Telemetry Metrics

The server can emit OpenTelemetry metrics for each A2A request lifecycle. This helps you observe throughput, latency, and streaming first-token responsiveness.

### Built-in Metrics

- `a2a.server.request_cnt` (`Counter`)
- `a2a.server.operation.duration` (`Histogram`, seconds)
- `a2a.server.time_to_first_token` (`Histogram`, seconds)

Default attributes:
- `a2a.method` (JSON-RPC method, e.g. `SendMessage`, `SendStreamingMessage`)
- `a2a.is_stream` (whether request is streaming)
- `error.type` (set only when request handling fails)

### Option 1: Let Server Build OTLP Meter Provider

```go
import (
    "trpc.group/trpc-go/trpc-a2a-go/v2/server"
    "trpc.group/trpc-go/trpc-a2a-go/v2/telemetry/metrics"
)

srv, err := server.NewA2AServer(
    taskManager,
    server.WithAgentCard(agentCard),
    server.WithTelemetryMeterProviderOptions(
        metrics.WithProtocol("grpc"),            // "grpc" (default) or "http"
        metrics.WithEndpoint("localhost:4317"), // OTLP collector endpoint
        metrics.WithServiceName("my-a2a-server"),
        metrics.WithServiceVersion("1.0.0"),
        metrics.WithServiceNamespace("my-team"),
    ),
)
if err != nil {
    panic(err)
}
```

Notes:
- `Start()` calls telemetry initialization automatically.
- If telemetry initialization fails, `Start()` returns an error immediately.
- Endpoint can also come from environment variables:
  - `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` (higher priority)
  - `OTEL_EXPORTER_OTLP_ENDPOINT`

### Option 2: Inject an Existing Meter Provider

```go
import (
    "context"

    "go.opentelemetry.io/otel/metric/noop"
    "trpc.group/trpc-go/trpc-a2a-go/v2/server"
)

provider := noop.NewMeterProvider()

srv, err := server.NewA2AServer(
    taskManager,
    server.WithAgentCard(agentCard),
    server.WithTelemetryMeterProvider(provider),
)
if err != nil {
    panic(err)
}

// You can also inject later:
srv.SetTelemetryMeterProvider(provider)
if err := srv.InitTelemetry(context.Background()); err != nil {
    panic(err)
}
```

### Customize TTFT Detection

You can override first-token matching logic for `time_to_first_token` using `WithFirstTokenPolicy`.

```go
import (
    "trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
    "trpc.group/trpc-go/trpc-a2a-go/v2/server"
)

type customFirstTokenPolicy struct{}

func (customFirstTokenPolicy) IsStreamingFirstToken(event protocol.StreamingMessageResult) bool {
    e, ok := event.(*protocol.TaskStatusUpdateEvent)
    return ok && e.Status.State == protocol.TaskStateWorking && e.Status.Message != nil
}

func (customFirstTokenPolicy) IsNonStreamingFirstToken(_ *protocol.MessageResult) bool {
    return true
}

srv, err := server.NewA2AServer(
    taskManager,
    server.WithAgentCard(agentCard),
    server.WithFirstTokenPolicy(customFirstTokenPolicy{}),
)
if err != nil {
    panic(err)
}
```

`WithFirstTokenMatcher` is still available for backward compatibility.

## Future Enhancements

- Persistent storage options for message history
- More utilities and helper functions for message processing
- More telemetry integrations (dashboards, alerts, and presets)
- Comprehensive test suite
- Advanced session management capabilities

## Contributing

Contributions and improvement suggestions are welcome! Please ensure your code follows Go coding standards and includes appropriate tests. See the [CONTRIBUTING.md](CONTRIBUTING.md) file for more details.

## Acknowledgements

This project's protocol design is based on Google's open-source A2A protocol ([original repository](https://github.com/google/A2A)), following the Apache 2.0 license. This is an unofficial implementation.

## Copyright

The copyright notice pertaining to the Tencent code in this repo was previously in the name of “THL A29 Limited.”  That entity has now been de-registered.  You should treat all previously distributed copies of the code as if the copyright notice was in the name of “Tencent.”
