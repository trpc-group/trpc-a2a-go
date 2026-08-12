# Subpath Example

This example demonstrates how to serve an A2A agent under a custom path with
`server.WithBasePath`. Agent Card `supportedInterfaces` URLs are discovery
metadata for clients and do not drive server mounting.

## Quick Start

```bash
go run main.go
```

Endpoints are mounted at `/api/v1/agent/*` via `WithBasePath("/api/v1/agent")`.

## Code Usage

```go
package main

import (
    "context"

    "trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
    "trpc.group/trpc-go/trpc-a2a-go/v2/server"
    "trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
    "trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

type simpleProcessor struct{}

func (p *simpleProcessor) ProcessMessage(
    ctx context.Context,
    ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
    handle := taskmanager.NewTaskHandle(ctx, ec)
    defer handle.Close()
    handle.Reply(protocol.NewAgentText("Hello from subpath agent!"))
    return handle.Events(), nil
}

func main() {
    agentCard := server.AgentCard{
        Name: "My Agent",
        // The advertised URL is the complete JSON-RPC endpoint.
        SupportedInterfaces: []server.AgentInterface{{
            URL:             "http://localhost:8080/api/v1/agent/",
            ProtocolBinding: protocol.ProtocolBindingJSONRPC,
            ProtocolVersion: protocol.ProtocolVersionV1,
        }},
    }

    taskManager, _ := memory.NewTaskManager(&simpleProcessor{})
    a2aServer, _ := server.NewA2AServer(
        taskManager,
        server.WithAgentCard(agentCard),
        server.WithBasePath("/api/v1/agent"),
    )
    a2aServer.Start(":8080")
}
```

**Result**: Endpoints available at:
- Agent Card: `http://localhost:8080/api/v1/agent/.well-known/agent-card.json`
- JSON-RPC: `http://localhost:8080/api/v1/agent/`
- JWKS (when enabled): `http://localhost:8080/api/v1/agent/.well-known/jwks.json`

The trailing slash in the advertised JSON-RPC interface is significant: `WithBasePath` mounts JSON-RPC at that exact path, and v2 clients use the interface URL as declared.

### External URL differs from internal route

```go
agentCard := server.AgentCard{
    Name: "My Agent",
    SupportedInterfaces: []server.AgentInterface{{
        // The complete public endpoint clients use; it may differ from the listen path.
        URL:             "https://example.com/external/path/",
        ProtocolBinding: protocol.ProtocolBindingJSONRPC,
        ProtocolVersion: protocol.ProtocolVersionV1,
    }},
}

a2aServer, _ := server.NewA2AServer(
    taskManager,
    server.WithAgentCard(agentCard),
    server.WithBasePath("/internal/api"), // Where this process listens
)
```

Without `WithBasePath`, the server keeps the root defaults
(`/`, `/.well-known/agent-card.json`, …).

## Testing

```bash
curl http://localhost:8080/api/v1/agent/.well-known/agent-card.json

curl -X POST http://localhost:8080/api/v1/agent/ \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "SendMessage",
    "params": {
      "message": {
        "role": "ROLE_USER",
        "messageId": "test-1",
        "parts": [{"text": "Hello"}]
      }
    },
    "id": "1"
  }'
```
