# Multi-Agent Server Example

This example demonstrates how to run **multiple agents in a single process** using different URL paths to define separate endpoints for each agent.

## Architecture Overview

**Single Process, Multiple Agents**: This implementation runs all agents within one server process, using URL path routing to distinguish between different agents. Each agent gets its own endpoint path but shares the same server resources.

```
Single Server Process (localhost:8080)
├── /api/v1/agent/chatAgent/     → Chat Agent
└── /api/v1/agent/workerAgent/   → Worker Agent  
```

## Key Features

- **Single Process Architecture**: All agents run in one server process
- **Path-Based Routing**: Each agent has a unique URL path endpoint
- **Dynamic Agent Discovery**: URL path parameter `{agentName}` determines which agent handles the request
- **Shared Resources**: All agents share the same server process, memory, and resources
- **Unified Protocol**: All agents follow the same A2A protocol interface

## Available Agents

### Chat Agent (`chatAgent`)
- **Function**: Provides chat conversation capabilities
- **Description**: "I am a chatbot"
- **Endpoints**: 
  - Agent Card: `GET /api/v1/agent/chatAgent/.well-known/agent.json`
  - JSON-RPC: `POST /api/v1/agent/chatAgent/`

### Worker Agent (`workerAgent`)
- **Function**: Provides worker task processing
- **Description**: "I am a worker"
- **Endpoints**:
  - Agent Card: `GET /api/v1/agent/workerAgent/.well-known/agent.json`
  - JSON-RPC: `POST /api/v1/agent/workerAgent/`

## Running the Example

### 1. Start the Server
```bash
cd examples/multiagent/server
go run main.go
```

The server will start on `localhost:8080` and display available endpoints:
```
Starting A2A server listening on localhost:8080
Chat agent card url : http://localhost:8080/api/v1/agent/chatAgent/.well-known/agent.json:
Chat agent interfaces: http://localhost:8080/api/v1/agent/chatAgent/
Worker agent card url: http://localhost:8080/api/v1/agent/workerAgent/.well-known/agent.json
Worker agent interfaces: http://localhost:8080/api/v1/agent/workerAgent/
```

### 2. Run the Test Client
In another terminal:
```bash
cd examples/multiagent/client
go run main.go
```

Or with custom message:
```bash
go run main.go -message="How are you today?"
```

### 3. Manual Testing

#### Get Agent Cards
```bash
# Chat Agent
curl http://localhost:8080/api/v1/agent/chatAgent/.well-known/agent.json

# Worker Agent
curl http://localhost:8080/api/v1/agent/workerAgent/.well-known/agent.json
```

#### Send Messages to Agents
```bash
# Send message to Chat Agent
curl -X POST http://localhost:8080/api/v1/agent/chatAgent/ \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"message/send","params":{"message":{"role":"user","kind":"message","messageId":"test-123","parts":[{"kind":"text","text":"Hello!"}]}},"id":1}'

# Send message to Worker Agent
curl -X POST http://localhost:8080/api/v1/agent/workerAgent/ \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","method":"message/send","params":{"message":{"role":"user","kind":"message","messageId":"test-456","parts":[{"kind":"text","text":"What can you do?"}]}},"id":1}'
```

## Core Implementation

### 1. HTTPRouter Interface
```go
type HTTPRouter interface {
    Handle(pattern string, handler http.Handler)
    ServeHTTP(w http.ResponseWriter, r *http.Request)
}
```

### 2. Using Chi Router with Path Parameters
```go
// Create Chi router
router := chi.NewMux()

// Register routes with path parameters using Chi's Route method
router.Route("/api/v1/agent/{agentName}", func(r chi.Router) {
    // Create A2A server for this route group
    a2aServer, err := server.NewA2AServer(
        agentCard,
        taskManager,
        server.WithMiddleWare(&middleWare{}),
        server.WithHTTPRouter(r),
        server.WithAgentCardHandler(&multiAgentCardHandler{}),
    )
    if err != nil {
        log.Fatalf("Failed to create A2A server: %v", err)
    }
    
    // Mount the A2A server handler to the subrouter
    r.Mount("/", a2aServer.Handler())
})
```

### 3. Middleware for Agent Name Extraction
```go
type middleWare struct{}

func (m *middleWare) Wrap(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        ctx := r.Context()
        ctx = context.WithValue(ctx, ctxAgentNameKey, chi.URLParam(r, "agentName"))
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

### 4. Dynamic Agent Card Handler
```go
type multiAgentCardHandler struct{}

func (h *multiAgentCardHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    agentName := chi.URLParam(r, "agentName")
    var agentCard server.AgentCard
    
    switch agentName {
    case "chatAgent":
        agentCard = server.AgentCard{
            Name:        "ChatAgent",
            Description: "I am a chatbot",
            // ... other fields
        }
    case "workerAgent":
        agentCard = server.AgentCard{
            Name:        "WorkerAgent",
            Description: "I am a worker",
            // ... other fields
        }
    default:
        w.WriteHeader(http.StatusNotFound)
        return
    }
    
    w.Header().Set("Content-Type", "application/json; charset=utf-8")
    json.NewEncoder(w).Encode(agentCard)
}
```

## Extension Example

You can easily add more agents by extending the switch cases in the handlers:

```go
// Add a new agent in multiAgentCardHandler
case "calculatorAgent":
    agentCard = server.AgentCard{
        Name:        "CalculatorAgent",
        Description: "I can perform mathematical calculations",
        URL:         fmt.Sprintf("http://%s/api/v1/agent/calculatorAgent/", *host),
        Skills: []server.AgentSkill{
            {
                Name:        "calculate",
                Description: stringPtr("Perform mathematical operations"),
                InputModes:  []string{"text"},
                OutputModes: []string{"text"},
                Tags:        []string{"math", "calculator"},
                Examples:    []string{"2 + 2", "10 * 5"},
            },
        },
        // ... other fields
    }

// Add corresponding case in multiAgentProcessor
case "calculatorAgent":
    return u.calculatorAgentProcessMessage(ctx, message, options, taskHandler)
```

## Client Usage

The client automatically tests both agents and displays their responses:

```bash
=== Multi-Agent tRPC Client Demo ===
Server: http://localhost:8080
Testing both agents...

--- Conversation with chatAgent ---
Agent: ChatAgent - I am a chatbot
User: Hello, how are you?
Response: Hello from chat agent!

--- Conversation with workerAgent ---
Agent: WorkerAgent - I am a worker
User: Hello, how are you?
Response: Hello from worker agent!

=== Demo completed ===
```
