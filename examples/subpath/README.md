# A2A Agent Sub-Path Integration Example

This example demonstrates how to serve an A2A agent behind a sub-path using the **`WithBasePath` option**. This is useful when integrating A2A agents into existing applications where you want the agent to be available under a specific URL structure.

## 🎯 The Problem

When integrating A2A agents into existing applications, you often want to serve the agent under a custom URL structure like `/agent/api/v2/myagent/`. The A2A server creates standard endpoints that need to be properly routed:

- `/.well-known/agent.json` (Agent metadata)
- `/` (JSON-RPC endpoint)
- `/.well-known/jwks.json` (JWKS endpoint for authentication)

When serving under a prefix, these become:
- `/agent/api/v2/myagent/.well-known/agent.json`
- `/agent/api/v2/myagent/`
- `/agent/api/v2/myagent/.well-known/jwks.json`

## ✨ The Solution: `WithBasePath` Option

The `WithBasePath` option automatically configures all endpoints with the base path:

```go
// Simple and clean!
a2aServer, err := server.NewA2AServer(agentCard, taskMgr,
    server.WithBasePath("/agent/api/v2/myagent"))

// Direct mounting
srv := &http.Server{
    Addr:    ":8080",
    Handler: a2aServer.Handler(),
}
```

**Benefits:**
- ✅ **Simple configuration** - one option configures everything
- ✅ **Framework agnostic** - works with any HTTP framework
- ✅ **Direct mounting** - clean integration
- ✅ **A2A spec compliant** - maintains standard endpoint structure
- ✅ **Less error-prone** - no manual path manipulation

## 🚀 Running the Example

```bash
# Install dependencies
go mod tidy

# Run with default settings
go run main.go

# Use custom port and sub-path
go run main.go -port=9090 -subPath="/my/custom/path"

# Show all available options
go run main.go -help
```

## 🧪 Testing the Integration

Once the server is running (default: http://localhost:8080):

### Get Agent Metadata
```bash
curl "http://localhost:8080/agent/api/v2/myagent/.well-known/agent.json"
```

### Send a Message
```bash
curl -X POST "http://localhost:8080/agent/api/v2/myagent/" \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "message/send",
    "params": {
      "message": {
        "role": "user",
        "parts": [{"kind": "text", "text": "hello world"}]
      }
    },
    "id": "1"
  }'
```

Expected response:
```json
{
  "jsonrpc": "2.0",
  "result": {
    "kind": "message",
    "messageId": "msg-...",
    "role": "agent",
    "parts": [{"kind": "text", "text": "Reversed: dlrow olleh"}]
  },
  "id": "1"
}
```

## 🔧 Framework Integration Examples

### Gin Framework
```go
r := gin.Default()

// Create A2A server with base path
a2aServer, err := server.NewA2AServer(agentCard, taskMgr,
    server.WithBasePath("/agent"))

// Mount directly - endpoints are pre-configured
r.Any("/*path", gin.WrapH(a2aServer.Handler()))
```

### Echo Framework
```go
e := echo.New()

// Create A2A server with base path
a2aServer, err := server.NewA2AServer(agentCard, taskMgr,
    server.WithBasePath("/agent"))

// Mount directly
e.Any("/*", echo.WrapHandler(a2aServer.Handler()))
```

### Chi Router
```go
r := chi.NewRouter()

// Create A2A server with base path
a2aServer, err := server.NewA2AServer(agentCard, taskMgr,
    server.WithBasePath("/agent"))

// Mount directly
r.Mount("/", a2aServer.Handler())
```

## 🔍 How It Works

### Automatic Endpoint Configuration
When you use `WithBasePath("/agent/api/v2/myagent")`, it automatically creates:
- **Agent Card**: `/agent/api/v2/myagent/.well-known/agent.json`
- **JSON-RPC**: `/agent/api/v2/myagent/`
- **JWKS**: `/agent/api/v2/myagent/.well-known/jwks.json`

### Path Normalization
The `WithBasePath` option automatically normalizes paths:
```go
// Input: "agent/api/v2/myagent/"
// Normalized: "/agent/api/v2/myagent"

// Input: "/agent/api/v2/myagent"
// Normalized: "/agent/api/v2/myagent" (no change)
```

## 💡 Best Practices

1. **Ensure AgentCard.URL matches** your actual server configuration
2. **Test all endpoints** after integration
3. **Use consistent base paths** across your application
4. **Include the base path in your AgentCard.URL** field

## 🔧 Configuration Examples

### Simple Sub-Path
```go
server.WithBasePath("/api/agent")
```
Creates endpoints:
- `/api/agent/.well-known/agent.json`
- `/api/agent/`

### Versioned API
```go
server.WithBasePath("/api/v2/agents/text-processor")
```
Creates endpoints:
- `/api/v2/agents/text-processor/.well-known/agent.json`
- `/api/v2/agents/text-processor/`

### Multi-Tenant
```go
server.WithBasePath("/tenants/acme/agents/assistant")
```
Creates endpoints:
- `/tenants/acme/agents/assistant/.well-known/agent.json`
- `/tenants/acme/agents/assistant/`
