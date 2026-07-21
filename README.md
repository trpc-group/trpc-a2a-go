English | [中文](README_zh.md)

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
- [Documentation](#documentation)
- [Examples](#examples)
  - [Simple Example](#1-simple-example-examplessimple)
  - [Basic Example](#2-basic-example-examplesbasic)
  - [Authentication Examples](#3-authentication-examples-examplesauth)
  - [v0 Compatibility Example](#4-v0-compatibility-example-examplescompat)
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
go run main.go -host 0.0.0.0 -port 9000
```

### Using the Basic CLI Client

```bash
# Connect to a local agent
cd examples/basic/client
go run main.go

# Connect to a specific agent
go run main.go -host localhost:9000

# Consume ordinary messages through message/stream
go run main.go -stream
```

## Documentation

The [docs/](docs/mkdocs/en/index.md) directory covers the protocol and the
framework in depth (English and [中文](docs/mkdocs/zh/index.md)):

- [overview.md](docs/mkdocs/en/overview.md) — what the framework is, its
  architecture, and the core event-stream idea.
- [protocol.md](docs/mkdocs/en/protocol.md) — the A2A protocol: agent cards,
  the wire objects, the task state machine, and the interaction flows.
- [server.md](docs/mkdocs/en/server.md) — build an agent: the server, the
  processor, the runtime contract (round lifecycle, cancellation, history,
  retention), and every server-side capability.
- [client.md](docs/mkdocs/en/client.md) — call agents: the consumption modes,
  task management, and orchestration.
- [migration.md](docs/mkdocs/en/migration.md) — port an existing v0.x agent
  to v1.0 (a summary is also in [Migrating from v0.x](#migrating-from-v0x)
  below).

## Examples

The repository includes several examples demonstrating different aspects of the A2A protocol:

### 1. Simple Example ([examples/simple](examples/simple))

A minimal example of the native channel style (the raw `MessageProcessor`
contract). The server reverses text, while the client automatically exercises
blocking send, `returnImmediately` plus polling, streaming, and a pure-message
reply without creating a task.

```bash
# Start the simple server
cd examples/simple/server
go run main.go

# Run all four client demos
cd examples/simple/client
go run main.go
```

### 2. Basic Example ([examples/basic](examples/basic))

An interactive `TaskHandle`-based chat example showcasing:

- Blocking `message/send` and streaming `message/stream`
- Long-running tasks started with `returnImmediately`
- GetTask, SubscribeToTask, and CancelTask
- Conversation grouping and message history through `contextId`

See [examples/basic/README.md](examples/basic/README.md) for the REPL commands.

### 3. Authentication Examples ([examples/auth](examples/auth))

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

### 4. v0 Compatibility Example ([examples/compat](examples/compat))

One server, both protocol generations: v1.0 clients on the standard wire and
unmodified v0.2.x clients through [compat/v0](compat/v0) — same endpoint,
same authentication chain. The client demonstrates the preserved legacy
defaults (a configuration-less `message/send` answers immediately) plus
blocking and streaming over the legacy wire.

```bash
# Start the v0-compatible server
cd examples/compat/server
go run main.go

# Run the legacy-wire client
cd examples/compat/client
go run main.go --message "hello legacy world"
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
// contract — see examples/simple for that style.
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
        handle.Reply(protocol.NewAgentText("input message must contain text."))
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
    handle.UpdateTaskState(protocol.TaskStateCompleted, protocol.NewAgentText("Processed: "+result))
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
    Description: "Agent description",
    URL: "http://localhost:8080/",
    Version: "1.0.0",
    Provider: &server.AgentProvider{
        Organization: "Provider name",
    },
    Capabilities: server.AgentCapabilities{
        Streaming: boolPtr(true),
    },
    DefaultInputModes:  []string{"text"},
    DefaultOutputModes: []string{"text"},
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
synchronous bodies, which were the common v0.x style. (Wire note: the v1.0
JSON-RPC binding names operations `SendMessage`, `SendStreamingMessage`,
`GetTask`, `ListTasks`, `CancelTask`, `SubscribeToTask` and the
`*TaskPushNotificationConfig` CRUD; the slash-delimited names — `message/send`,
`tasks/get`, ... — are the v0.2.x wire, still served by `compat/v0`. This
guide refers to operations by the v0.x names migrating readers already know.)

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
    handle.UpdateTaskState(protocol.TaskStateCompleted, protocol.NewAgentText("done"))

    return handle.Events(), nil
}
```

Two reference examples: [examples/basic](examples/basic) is the minimal-edit
`TaskHandle` port of a v0.x processor; [examples/simple](examples/simple) is
the native channel style recommended for new code.

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
| `TaskHandler.AddArtifact(taskID, artifact, isFinal, needMoreData)` | call `TaskHandle.AddArtifact(artifact, isFinal)` when `needMoreData=false`, or `TaskHandle.AppendArtifact(artifact, isFinal)` when `needMoreData=true`; reuse the same `ArtifactID` for continuation chunks |
| `TaskHandler.SubscribeTask` / `.CleanTask` | removed — the framework owns fan-out and task lifecycle; nothing deletes tasks by default — set `memory.WithTaskTTL` to collect terminal tasks |
| return an interim `Message` result while a goroutine drives the task (v0 non-blocking) | emit events from a goroutine; unary callers opt in with `returnImmediately=true` — the first persisted event answers the call; multi-turn suspends with `input-required` |
| `TaskHandler.GetContextID` / `.GetMessageHistory` | same names on `TaskHandle`; `History` is a snapshot taken before the round and truncated to the manager's `MaxHistoryLength` — not a live read |
| `TaskHandler.GetTask(taskID)` | `TaskHandle.GetTask()` — this round's continuation snapshot only, `nil` on a fresh round; arbitrary-task reads and `CancellableTask.Cancel` are gone |
| `TaskHandler.GetMetadata` | removed (it always returned an error in v0.x); message metadata is `ExecContext.Message.Metadata` |
| `taskmanager.TaskSubscriber` / `CancellableTask` | removed with the callback design |
| redis `NewTaskSubscriber` / `WithSubscriberSendHook` / `WithSubscriberBlockingSend` | removed — the manager owns fan-out; Redis cross-node `SubscribeToTask` is opt-in with `redis.WithCrossNodeResubscribe(true)` |

### Behavior changes to check

These compile fine but behave differently from v0.x:

- **v1.0 inverted the blocking default**: `message/send` without
  `returnImmediately` waits for the round to end (v0.x `blocking:false`/absent
  answered immediately). Legacy-endpoint clients served via `compat/v0` keep
  the v0 default.
- **The round ends only when the processor closes its event channel**: a round
  that never closes pins the task's execution slot (follow-ups are rejected
  with "already has an active execution") and blocks manager Close / server
  Stop — close the channel (or `TaskHandle`) from the goroutine that emits.
- **`input-required`/`auth-required` yields the round**: events emitted after
  a suspend are discarded — deliver the completion from the continuation
  round.
- **One round drives exactly one task** (`ExecContext.TaskID`): an event
  carrying any other task ID is a contract violation that fails the round's
  task.
- **Sending `*protocol.Task` as a stream event (legal in v0.x) is now a
  contract violation** — the framework materializes task snapshots itself;
  emit status and artifact events instead.
- **A follow-up without `taskId` starts a new round with a fresh task**:
  sessions keyed by contextID alone strand the suspended task (it is never
  collected on the memory backend). Echo the `taskId` when answering
  `input-required`.
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

Use a `SignedSender` to deliver push notifications with JWT authentication and
publish its verification keys through the server:

```go
sender, err := pushauth.NewSignedSender()
if err != nil {
    // Handle error
}

taskManager, err := memory.NewTaskManager(
    processor,
    memory.WithPushNotifications(push.Config{Sender: sender}),
)

srv, err := server.NewA2AServer(
    taskManager,
    server.WithAgentCard(agentCard),
    server.WithPushNotificationJWKSHandler(sender.JWKSHandler()),
)
```

If a client declares an authentication scheme and credentials in its push
configuration (for example, Basic or Bearer), the sender uses them as requested.
The JWT identity is the fallback when the client does not declare credentials.
For unsigned delivery, use `push.NewHTTPSender()` and omit the JWKS handler option.

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
