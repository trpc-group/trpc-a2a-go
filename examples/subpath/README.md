# Subpath Example

This example demonstrates how to serve an A2A agent at a custom path using automatic path extraction from `agentCard.URL`.

## Quick Start

```bash
go run main.go
```

The server will automatically configure endpoints at `/api/v1/agent/*` based on the agent card URL.

## Code Usage

### Method 1: Automatic Path Extraction (Recommended)

```go
package main

import (
    "trpc.group/trpc-go/trpc-a2a-go/server"
    "trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// Create a simple message processor
type simpleProcessor struct{}

func (p *simpleProcessor) ProcessMessage(
    ctx context.Context,
    message protocol.Message,
    options taskmanager.ProcessOptions,
    taskHandler taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
    response := &protocol.Message{
        Role: protocol.MessageRoleAgent,
        Kind: protocol.KindMessage,
        MessageID: protocol.GenerateMessageID(),
        Parts: []protocol.Part{
            &protocol.TextPart{
                Kind: protocol.KindText,
                Text: "Hello from subpath agent!",
            },
        },
    }
    return &taskmanager.MessageProcessingResult{Result: response}, nil
}

func main() {
    // 1. Create agent card with subpath URL
    agentCard := server.AgentCard{
        Name: "My Agent",
        URL:  "http://localhost:8080/api/v1/agent", // ← Path extracted automatically
    }

    // 2. Create task manager with your processor
    taskManager, _ := taskmanager.NewMemoryTaskManager(&simpleProcessor{})

    // 3. Create server (path automatically configured)
    a2aServer, _ := server.NewA2AServer(agentCard, taskManager)

    // 4. Start server
    a2aServer.Start(":8080")
}
```

**Result**: Endpoints available at:
- Agent Card: `http://localhost:8080/api/v1/agent/.well-known/agent.json`  
- JSON-RPC: `http://localhost:8080/api/v1/agent/`
- JWKS: `http://localhost:8080/api/v1/agent/.well-known/jwks.json`

### Method 2: Explicit Path Configuration (Advanced)

For cases where the external URL differs from internal routing:

```go
// Use WithBasePath option for explicit control
agentCard := server.AgentCard{
    Name: "My Agent", 
    URL:  "https://example.com/external/path", // External URL
}

a2aServer, _ := server.NewA2AServer(
    agentCard, 
    taskManager,
    server.WithBasePath("/internal/api"), // ← Internal routing path
)
```

**Result**: Endpoints served at `/internal/api/*` regardless of external URL.

## Configuration Examples

### Root Path
```go
agentCard.URL = "http://localhost:8080/"
// Endpoints: /, /.well-known/agent.json
```

### Single Level
```go
agentCard.URL = "http://localhost:8080/agent"  
// Endpoints: /agent/, /agent/.well-known/agent.json
```

### Multi Level
```go
agentCard.URL = "http://localhost:8080/api/v1/myagent"
// Endpoints: /api/v1/myagent/, /api/v1/myagent/.well-known/agent.json
```

### With Port
```go
agentCard.URL = "https://api.example.com:8443/agents/v2/chat"
// Endpoints: /agents/v2/chat/, /agents/v2/chat/.well-known/agent.json
```

## Framework Integration

### Gin Router
```go
r := gin.Default()
// Mount A2A server at extracted path
r.Any("/api/v1/agent/*path", gin.WrapH(a2aServer.Handler()))
```

### Echo Router  
```go
e := echo.New()
e.Any("/api/v1/agent/*", echo.WrapHandler(a2aServer.Handler()))
```

### Standard HTTP
```go
http.Handle("/api/v1/agent/", a2aServer.Handler())
http.ListenAndServe(":8080", nil)
```

## Important Notes

### ⚠️ URL Format Requirements
- **Must include scheme**: `http://` or `https://`
- **Must include host**: `localhost`, `example.com`, etc.
- **Path is optional**: Will default to root if omitted

```go
// ✅ Valid URLs
"http://localhost:8080/api/v1/agent"
"https://api.example.com/agents"  
"http://127.0.0.1:3000/"

// ❌ Invalid URLs  
"/api/v1/agent"           // Missing scheme and host
"localhost:8080/api"      // Missing scheme
"http:///api"             // Missing host
```

### 🔄 Priority System
1. **WithBasePath option** (highest priority)
2. **agentCard.URL path extraction** (fallback)  
3. **Default root paths** (if both above are empty)

### 📁 Path Normalization
The system automatically handles:
- **Trailing slashes**: `"/api/v1/"` → `"/api/v1"`
- **Leading slashes**: Ensured for all paths
- **Empty paths**: `"/"` or `""` → defaults to root
- **Invalid URLs**: Graceful fallback to defaults with warnings

### 🚀 Best Practices

1. **Use descriptive paths**: `/api/v1/myagent` vs `/agent`
2. **Match your API versioning**: Include version in path if applicable  
3. **Consider reverse proxy**: External vs internal path differences
4. **Test endpoint accessibility**: Verify all endpoints work after configuration

## Testing

```bash
# Get agent metadata
curl http://localhost:8080/api/v1/agent/.well-known/agent.json

# Send a message  
curl -X POST http://localhost:8080/api/v1/agent/ \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "message/send", 
    "params": {
      "message": {
        "role": "user",
        "parts": [{"kind": "text", "text": "Hello"}]
      }
    },
    "id": "1"
  }'
```
