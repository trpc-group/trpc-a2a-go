[English](README.md) | 中文

# tRPC-A2A-Go

[![Go Reference](https://pkg.go.dev/badge/trpc.group/trpc-go/trpc-a2a-go/v2.svg)](https://pkg.go.dev/trpc.group/trpc-go/trpc-a2a-go/v2) [![Go Report Card](https://goreportcard.com/badge/github.com/trpc-group/trpc-a2a-go)](https://goreportcard.com/report/github.com/trpc-group/trpc-a2a-go) [![LICENSE](https://img.shields.io/badge/license-Apache--2.0-green.svg)](https://github.com/trpc-group/trpc-a2a-go/blob/main/LICENSE) [![Releases](https://img.shields.io/github/release/trpc-group/trpc-a2a-go.svg?style=flat-square)](https://github.com/trpc-group/trpc-a2a-go/releases) [![Tests](https://github.com/trpc-group/trpc-a2a-go/actions/workflows/prc.yml/badge.svg)](https://github.com/trpc-group/trpc-a2a-go/actions/workflows/prc.yml) [![Coverage](https://codecov.io/gh/trpc-group/trpc-a2a-go/branch/main/graph/badge.svg)](https://app.codecov.io/gh/trpc-group/trpc-a2a-go/tree/main)

这是 tRPC group 对 [A2A 协议](https://google.github.io/A2A/) 的 Go 实现，让不同的 AI agent 能够相互发现并协作。

## 相关项目

tRPC AI 生态

+ [**trpc-agent-go**](https://github.com/trpc-group/trpc-agent-go) ：一个强大的 Go 框架，用于构建基于大语言模型（LLM）、分层 planner、记忆、遥测与丰富工具生态的智能 agent 系统。如果你想创建能够推理、调用工具、与 sub-agent 协作并保持长期状态的自主或半自主 agent，[**tRPC-Agent-Go**](https://github.com/trpc-group/trpc-agent-go) 都能满足你。

+ [**trpc-mcp-go**](https://github.com/trpc-group/trpc-mcp-go) ：如果你对 **streamable MCP 开发**感兴趣，可以看看 [**trpc-mcp-go**](https://github.com/trpc-group/trpc-mcp-go)——我们对 Model Context Protocol（MCP）的 Go 实现，具备流式能力。

## 目录

- [快速开始](#快速开始)
- [文档](#文档)
- [示例](#示例)
  - [简单示例](#1-简单示例-examplessimple)
  - [流式示例](#2-流式示例-examplesstreaming)
  - [基础示例](#3-基础示例-examplesbasic)
  - [鉴权示例](#4-鉴权示例-examplesauth)
  - [v0 兼容示例](#5-v0-兼容示例-examplescompat)
- [创建你自己的 Agent](#创建你自己的-agent)
- [从 v0.x 迁移](#从-v0x-迁移)
- [鉴权](#鉴权)
- [会话管理](#会话管理)
- [遥测指标](#遥测指标)
- [未来规划](#未来规划)
- [贡献](#贡献)
- [致谢](#致谢)
- [许可证](#许可证)

## 快速开始

### 运行 Basic Server 示例

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

### 使用 Basic CLI 客户端

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

# Use a specific context ID (conversation)
go run main.go --context "your-context-id"
```

## 文档

[docs/](docs/mkdocs/en/index.md) 目录深入讲解了协议与框架（英文，以及 [中文](docs/mkdocs/zh/index.md)）：

- [overview.md](docs/mkdocs/en/overview.md) —— 框架是什么、它的架构，以及核心的 event-stream 思路。
- [protocol.md](docs/mkdocs/en/protocol.md) —— A2A 协议：agent card、wire 对象、task 状态机，以及交互流程。
- [server.md](docs/mkdocs/en/server.md) —— 构建 agent：server、processor、运行时契约（round 生命周期、取消、history、保留策略），以及所有服务端能力。
- [client.md](docs/mkdocs/en/client.md) —— 调用 agent：消费模式、task 管理与编排。
- [migration.md](docs/mkdocs/en/migration.md) —— 把已有的 v0.x agent 迁移到 v1.0（下方 [从 v0.x 迁移](#从-v0x-迁移) 也有摘要）。

## 示例

本仓库包含若干示例，演示 A2A 协议的不同方面：

### 1. 简单示例 ([examples/simple](examples/simple))

一个最小示例，以原生 channel 风格（即裸 `MessageProcessor` 契约）演示 A2A 的核心功能：
- 一个简单 server，将文本输入反转，并在裸 channel 上发出 event
- 客户端演示三种消费模式：阻塞式 send、`returnImmediately`、以及 streaming——全部由同一个 processor 提供服务
- 基本的 task 生命周期（惰性创建、处理、完成）与 artifact

```bash
# Start the simple server
cd examples/simple/server
go run main.go

# Run the simple client (runs the blocking / returnImmediately / streaming demos)
cd examples/simple/client
go run main.go

# Point the client at a different server
go run main.go -host localhost:8080
```

### 2. 流式示例 ([examples/streaming](examples/streaming))

聚焦 streaming 能力的示例：
- 支持流式响应的 server 实现
- 处理流式数据的 client 实现

```bash
# Start the streaming server
cd examples/streaming/server
go run main.go

# Run the streaming client
cd examples/streaming/client
go run main.go
```

### 3. 基础示例 ([examples/basic](examples/basic))

一个综合示例，展示：
- 一个多功能文本处理 server，支持多种操作
- 一个功能丰富的 CLI 客户端，支持所有核心 A2A 协议 API
- 流式与非流式模式
- 带 session 管理的多轮对话
- task 管理（创建、取消、获取）
- agent 能力发现

### 4. 鉴权示例 ([examples/auth](examples/auth))

演示鉴权的完整示例：
- 支持多种鉴权方式的 server 实现
- 展示如何用不同鉴权方式连接的 client 示例
- JWT、API key 与 OAuth2 实现
- 所有鉴权参数的命令行选项

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

### 5. v0 兼容示例 ([examples/compat](examples/compat))

一个 server，同时服务两代协议：v1.0 客户端走标准 wire，未经改动的 v0.2.x 客户端通过 [compat/v0](compat/v0) 接入——同一个 endpoint、同一条鉴权链。该客户端演示了被保留的 legacy 默认行为（无任何配置的 `message/send` 会立即应答），以及在 legacy wire 上的阻塞式与流式调用。

```bash
# Start the v0-compatible server
cd examples/compat/server
go run main.go

# Run the legacy-wire client
cd examples/compat/client
go run main.go --message "hello legacy world"
```

## 创建你自己的 Agent

### 1. 实现 MessageProcessor 接口

该接口定义了你的 agent 如何处理收到的消息：

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

### 2. 创建 Agent Card

agent card 描述你的 agent 的能力：

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

### 3. 创建并启动 Server

用你的 task processor 和 agent card 初始化 server：

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

## 从 v0.x 迁移

v1.0（`/v2`）版本用上文所示的单一 event-stream 契约，替换了原来多结果返回的 `MessageProcessor` + `TaskHandler` 回调。熟悉的名字得以保留：你依然实现 `MessageProcessor.ProcessMessage`，原来的 `TaskHandler` 动词以 `TaskHandle` 兼容层的形式延续，因此 v0.x 的 processor 函数体只需极少改动即可迁移——包括完全同步的函数体，而它正是 v0.x 常见的写法。（wire 说明：v1.0 的 JSON-RPC 绑定将操作命名为 `SendMessage`、`SendStreamingMessage`、`GetTask`、`ListTasks`、`CancelTask`、`SubscribeToTask` 以及 `*TaskPushNotificationConfig` 的 CRUD；带斜杠的名字——`message/send`、`tasks/get`……——是 v0.2.x 的 wire，仍由 `compat/v0` 提供服务。本指南沿用迁移读者已经熟悉的 v0.x 名称来指代这些操作。）

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

两个参考示例：[examples/basic](examples/basic) 是对 v0.x processor 做最小改动的 `TaskHandle` 迁移版；[examples/simple](examples/simple) 则是推荐给新代码的原生 channel 风格。

### API 映射

| v0.x | v1.0 |
| --- | --- |
| `ProcessMessage(ctx, message, options, handler)` | `ProcessMessage(ctx, execContext)` —— message、options 和各种读取都在 `ExecContext` 上 |
| `MessageProcessingResult{Result: &message}` | 发出该 message：`handle.Reply(&msg)` / `out <- &msg` |
| `MessageProcessingResult{StreamingEvents: subscriber}` | 返回的 channel 就是 stream |
| `ProcessOptions.Streaming` / `.Blocking` | 已移除——同一个 processor 同时服务 `message/send` 与 `message/stream`；框架自行推导各自的结果 |
| `ProcessOptions.HistoryLength` | 已移除——由框架在响应中应用 |
| `ProcessOptions.PushNotificationConfig` | `ExecContext.PushConfig` |
| `ProcessOptions.AcceptedOutputModes` / `.Tenant` | `ExecContext.AcceptedOutputModes` / `.Tenant` |
| `TaskHandler.BuildTask` | 已移除——task 在第一个 task event 时惰性创建；ID 为 `ExecContext.TaskID` / `TaskHandle.TaskID()` |
| `TaskHandler.UpdateTaskState(taskID, state, msg)` | `TaskHandle.UpdateTaskState(state, msg)`（或在裸 channel 上发出 `protocol.TaskStatusUpdateEvent`） |
| `TaskHandler.AddArtifact(taskID, artifact, isFinal, needMoreData)` | `needMoreData=false` 时调用 `TaskHandle.AddArtifact(artifact, isFinal)`，`needMoreData=true` 时调用 `TaskHandle.UpdateArtifact(artifact, isFinal)`；续块必须复用同一个 `ArtifactID` |
| `TaskHandler.SubscribeTask` / `.CleanTask` | 已移除——框架自行负责 fan-out 与 task 生命周期；默认不会删除任何 task——设置 `memory.WithTaskTTL` 以回收终态 task |
| 在 goroutine 驱动 task 的同时返回一个临时的 `Message` 结果（v0 非阻塞） | 从 goroutine 发出 event；一元调用方通过 `returnImmediately=true` 选择加入——第一个被持久化的 event 应答该调用；多轮场景以 `input-required` 挂起 |
| `TaskHandler.GetContextID` / `.GetMessageHistory` | `TaskHandle` 上同名；`History` 是本轮开始前拍下的快照，并截断到 manager 的 `MaxHistoryLength`——不是实时读取 |
| `TaskHandler.GetTask(taskID)` | `TaskHandle.GetTask()` —— 仅本轮的续跑快照，全新一轮时为 `nil`；任意 task 读取与 `CancellableTask.Cancel` 均已移除 |
| `TaskHandler.GetMetadata` | 已移除（它在 v0.x 中总是返回 error）；message 的 metadata 为 `ExecContext.Message.Metadata` |
| `taskmanager.TaskSubscriber` / `CancellableTask` | 随回调式设计一并移除 |
| redis `NewTaskSubscriber` / `WithSubscriberSendHook` / `WithSubscriberBlockingSend` | 已移除——跨副本 streaming 不在内置 manager 的范围内 |

### 需要注意的行为变化

以下代码能正常编译，但行为与 v0.x 不同：

- **v1.0 反转了阻塞的默认行为**：不带 `returnImmediately` 的 `message/send` 会等待本轮结束（v0.x 在 `blocking:false`/缺省时立即应答）。通过 `compat/v0` 服务的 legacy endpoint 客户端仍保持 v0 的默认行为。
- **只有当 processor 关闭其 event channel 时本轮才结束**：永不关闭的一轮会占住该 task 的执行槽（后续请求会被以 “already has an active execution” 拒绝），并阻塞 manager 的 Close / server 的 Stop——请在发出 event 的那个 goroutine 里关闭 channel（或 `TaskHandle`）。
- **`input-required`/`auth-required` 会让出本轮**：挂起之后再发出的 event 会被丢弃——请在续跑的那一轮里交付完成结果。
- **一轮恰好驱动一个 task**（`ExecContext.TaskID`）：携带任何其他 task ID 的 event 属于违反契约，会使本轮的 task 失败。
- **把 `*protocol.Task` 作为 stream event 发送（在 v0.x 合法）现在属于违反契约**——框架会自行物化 task 快照；请改为发出 status 与 artifact event。
- **不带 `taskId` 的后续请求会以一个全新的 task 开启新一轮**：仅以 contextID 作为键的 session 会把被挂起的 task 搁置（在 memory 后端上它永远不会被回收）。在应答 `input-required` 时请回带 `taskId`。
- `message/send` 不再总是物化一个 task：纯 message 应答不会留下任何 task，对该轮 ID 调用 `tasks/get` 会返回 not-found。
- 完全不产生任何 event 的一轮会被当作 processor 的 bug：`message/send` 以 `-32603` 失败。
- 在 task 处于 `submitted`/`working` 时关闭 event channel 会把它标记为 `FAILED`——请有意识地结束一轮，处于终态或 `input-required`/`auth-required` 状态。
- `tasks/cancel` 返回请求取消时拍下的快照（可能仍是 `working`）；终态 `CANCELED` 会在 processor 那一轮收尾时被持久化。

## 鉴权

tRPC-A2A-Go 框架支持多种鉴权方式，用于保护 agent 与 client 之间的通信安全：

### 支持的鉴权方式

- **JWT（JSON Web Tokens）**：基于 token 的安全鉴权，支持 audience 与 issuer 校验
- **API Keys**：使用自定义 header 的简单 key 鉴权
- **OAuth 2.0**：支持多种 OAuth2 流程，包括：
  - Client Credentials 流程
  - Password Credentials 流程
  - 自定义 token 来源
  - Token 校验

### 服务端鉴权

#### 为你的 server 添加鉴权

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

#### 使用鉴权 middleware

```go
// Create an authentication provider
jwtProvider := auth.NewJWTAuthProvider(secretKey, audience, issuer, tokenLifetime)

// Create middleware
authMiddleware := auth.NewMiddleware(jwtProvider)

// Wrap your handler
http.Handle("/protected", authMiddleware.Wrap(yourHandler))
```

### 客户端鉴权

用相应的 option 创建带鉴权的 client：

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

不同鉴权方式的完整示例见 [examples/auth/client](examples/auth/client) 目录。

### 推送通知鉴权

框架内置了对安全推送通知的支持：

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

## 会话管理

A2A 协议支持 session 管理，用于把相关的 message 与 task 归组：

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

这带来：
- 把相关 message 归并到同一个 session 下
- 跨多次 message 交换的多轮对话
- 更好地组织与检索会话历史

## 遥测指标

server 可以为每个 A2A 请求的生命周期发出 OpenTelemetry 指标。这有助于你观测吞吐、延迟，以及流式首 token 的响应性。

### 内置指标

- `a2a.server.request_cnt` (`Counter`)
- `a2a.server.operation.duration` (`Histogram`, seconds)
- `a2a.server.time_to_first_token` (`Histogram`, seconds)

默认属性：
- `a2a.method`（JSON-RPC 方法，例如 `SendMessage`、`SendStreamingMessage`）
- `a2a.is_stream`（请求是否为流式）
- `error.type`（仅在请求处理失败时设置）

### 方式 1：让 Server 构建 OTLP Meter Provider

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

说明：
- `Start()` 会自动调用遥测初始化。
- 若遥测初始化失败，`Start()` 会立即返回 error。
- Endpoint 也可以来自环境变量：
  - `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`（优先级更高）
  - `OTEL_EXPORTER_OTLP_ENDPOINT`

### 方式 2：注入已有的 Meter Provider

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

### 自定义 TTFT 检测

你可以用 `WithFirstTokenPolicy` 覆盖 `time_to_first_token` 的首 token 匹配逻辑。

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

为了向后兼容，`WithFirstTokenMatcher` 仍然可用。

## 未来规划

- message 历史的持久化存储选项
- 更多用于 message 处理的工具与辅助函数
- 更多遥测集成（仪表盘、告警与预设）
- 更完善的测试套件
- 更高级的 session 管理能力

## 贡献

欢迎贡献代码与改进建议！请确保你的代码遵循 Go 编码规范并包含适当的测试。更多细节见 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 致谢

本项目的协议设计基于 Google 开源的 A2A 协议（[原始仓库](https://github.com/google/A2A)），遵循 Apache 2.0 许可证。这是一个非官方实现。

## 许可证

遵循 **Apache 2.0 许可证** - 详见 [LICENSE](LICENSE) 文件。
