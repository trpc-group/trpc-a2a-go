# Multi-Agent Server Example (tenant-native)

This example runs **multiple agents in a single process** using the A2A **v1.0
tenant** model: all agents share **one** endpoint, and each request names the
agent it wants via the `tenant` field in the request body. The agent-card
endpoint serves the right card for `?tenant=<tenant>`.

This replaces the older v0.x approach that distinguished agents by **URL path**
(`/api/v1/agent/{agentName}/`), which required a placeholder card, a custom
`AgentCardHandler`, a `{agentName}` path template, request middleware and a chi
router. None of that is needed anymore.

## Architecture Overview

```
Single Server Process (localhost:8080)
└── /                          → JSON-RPC for every agent
    └── request.tenant selects the agent ("chatAgent" | "workerAgent")
    /.well-known/agent-card.json?tenant=chatAgent   → Chat Agent card
    /.well-known/agent-card.json?tenant=workerAgent → Worker Agent card
    /.well-known/agent-card.json                    → 404 (no default card here;
                                                       add one with WithAgentCard)
```

## Key Points

- **One endpoint, many agents**: routing is by the `tenant` field, not the URL.
- **Static or dynamic registry**: register known agents with
  `server.WithTenantCard(tenant, card)`; for tenants not known at startup use
  `server.WithTenantCardProvider(func(ctx, tenant) (AgentCard, error))`.
- **Processor dispatch**: the processor switches on `options.Tenant`.
- **No router/middleware/placeholder card** needed.

## Available Agents

| Tenant        | Card name   | Reply                      |
|---------------|-------------|----------------------------|
| `chatAgent`   | ChatAgent   | `Hello from chat agent!`   |
| `workerAgent` | WorkerAgent | `Hello from worker agent!` |

## Running the Example

### 1. Start the Server
```bash
cd examples/multi_endpoint/server
go run main.go
```

The server starts on `localhost:8080`:
```
Starting A2A server on localhost:8080
  Per-tenant card: http://localhost:8080/.well-known/agent-card.json?tenant=chatAgent|workerAgent
  JSON-RPC: http://localhost:8080/  (set the "tenant" field in the request to pick an agent)
```

### 2. Run the Test Client
```bash
cd examples/multi_endpoint/client
go run main.go            # or: go run main.go -message="How are you today?"
```

### 3. Manual Testing

```bash
# Per-tenant agent card
curl "http://localhost:8080/.well-known/agent-card.json?tenant=chatAgent"
curl "http://localhost:8080/.well-known/agent-card.json?tenant=workerAgent"

# Send a message — "tenant" in params selects the agent
curl -X POST http://localhost:8080/ \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"SendMessage","id":1,"params":{
        "tenant":"chatAgent",
        "message":{"role":"ROLE_USER","messageId":"test-123",
                   "content":[{"text":"Hello!"}]}}}'
```

## Core Implementation

### 1. Register a card per tenant
```go
a2aServer, _ := server.NewA2AServer(taskManager,
    server.WithTenantCard("chatAgent", chatCard),
    server.WithTenantCard("workerAgent", workerCard),
)
```
The agent card is no longer a required positional argument: a multi-tenant server
needs no default card. Add an optional default ("directory") card served when no
`?tenant=` is given with `server.WithAgentCard(card)`.

For a dynamic set of agents instead of (or alongside) the static registry:
```go
server.WithTenantCardProvider(func(ctx context.Context, tenant string) (server.AgentCard, error) {
    return loadCardFromDB(ctx, tenant) // return an error for an unknown tenant
})
```

### 2. Dispatch on the tenant in the processor
```go
func (p *multiAgentProcessor) ProcessMessage(
    _ context.Context, _ protocol.Message,
    options taskmanager.ProcessOptions, _ taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
    switch options.Tenant {
    case "chatAgent":
        return reply("Hello from chat agent!"), nil
    case "workerAgent":
        return reply("Hello from worker agent!"), nil
    default:
        return nil, fmt.Errorf("no such tenant %q", options.Tenant)
    }
}
```

### 3. Client sends the tenant
```go
params := protocol.SendMessageParams{
    Tenant:  "chatAgent", // route to the agent registered under this tenant
    Message: userMessage,
}
resp, _ := a2aClient.SendMessage(ctx, params)
```

## Adding an Agent

1. `server.WithTenantCard("calculatorAgent", calcCard)` (or return it from the provider).
2. Add a `case "calculatorAgent":` to the processor.

That's it — no new route, middleware, or card handler.

## Expected Client Output

```
=== Multi-Agent tRPC Client Demo ===
Server: http://localhost:8080/ (agents addressed by tenant)

--- Conversation with chatAgent ---
Agent: ChatAgent - I am a chatbot
User: Hello!
Response: Hello from chat agent!

--- Conversation with workerAgent ---
Agent: WorkerAgent - I am a worker
User: Hello!
Response: Hello from worker agent!

=== Demo completed ===
```
