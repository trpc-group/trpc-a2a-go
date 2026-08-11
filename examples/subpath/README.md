# Subpath Example

This example demonstrates how to serve an A2A agent under a custom path with
`server.WithBasePath`. Agent Card URLs are discovery metadata for clients and
do not drive server mounting.

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
        // Discovery URL advertised to clients (may differ from listen path).
        URL: "http://localhost:8080/api/v1/agent",
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

### External URL differs from internal route

```go
agentCard := server.AgentCard{
    Name: "My Agent",
    URL:  "https://example.com/external/path", // What clients discover
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
