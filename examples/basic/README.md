# A2A Basic Example

This example demonstrates a basic implementation of the Agent-to-Agent (A2A) protocol using the trpc-a2a-go library. It consists of:

1. A versatile text processing agent server that supports multiple operations
2. A feature-rich CLI client that demonstrates all the core A2A protocol APIs

The server is written in the `taskmanager.TaskHandle` style: the minimal-edit
port of a v0.x TaskHandler processor, with a fully synchronous body and one
code path serving both `message/send` and `message/stream` (see "Migrating
from v0.x" in the repository README). For the native channel style, see
`examples/simple`.

## Server Features

The server is a text processing agent capable of:

- Processing text in various modes: reverse, uppercase, lowercase, word count
- Supporting both streaming and non-streaming responses
- Demonstrating multi-turn conversations with the `input-required` state
- Handling task cancellation
- Creating and streaming artifacts

### Running the Server

```bash
cd server
go run main.go [options]
```

Server options:
- `--host`: Host address (default: localhost)
- `--port`: Port number (default: 8080)
- `--desc`: Custom agent description
- `--no-cors`: Disable CORS headers
- `--no-stream`: Disable streaming capability

## Client Features

The client is a CLI application that connects to an A2A agent and provides:

- Support for both streaming and non-streaming modes
- Interactive CLI with command history
- Session management for contextual conversations
- Task management (create, cancel, get)
- Agent capability discovery

### Running the Client

```bash
cd client
go run main.go [options]
```

Client options:
- `--agent`: Agent URL (default: http://localhost:8080/)
- `--timeout`: Request timeout (default: 60s)
- `--no-stream`: Disable streaming mode
- `--context`: Use specific context ID (generate new if empty)
- `--history`: Number of history messages to request (default: 0)

### Client Commands

Once the client is running, you can use the following commands:

- `help`: Show help message
- `exit`: Exit the program
- `context [id]` / `new`: Set or generate a new context ID
- `mode [stream|sync]`: Set interaction mode (streaming or standard)
- `cancel [task-id]`: Cancel a task
- `get [task-id] [history]`: Get task details
- `card`: Fetch and display the agent's capabilities card

For normal interaction, type your message and press Enter. After the agent
suspends in `input-required`, the next message continues the same `taskId`.

### Text Processing Commands

The server understands the following text processing commands:

- `reverse <text>`: Reverses the input text
- `uppercase <text>`: Converts text to uppercase
- `lowercase <text>`: Converts text to lowercase
- `count <text>`: Counts words and characters in text
- `multi`: Start a multi-step interaction (client continues with the same `taskId`)
- `example`: Demonstrates input-required state
- `help`: Shows the help message

## Usage Example

1. Start the server:
   ```bash
   cd server
   go run main.go
   ```

2. In another terminal, start the client:
   ```bash
   cd client
   go run main.go
   ```

3. Try some commands:
   ```
   > help
   > reverse hello world
   > uppercase the quick brown fox
   > multi
   > card
   > mode sync
   > lowercase TESTING LOWERCASE
   ```

## A2A Protocol Implementation

This example demonstrates the following A2A protocol features:

- Agent discovery via Agent Cards (/.well-known/agent-card.json)
- Task creation using message/send and message/stream
- Task state retrieval using tasks/get
- Task cancellation using tasks/cancel
- Streaming updates for long-running tasks
- Multi-turn conversations using the `input-required` state
- Artifact generation and streaming

Multi-turn progress is stored on the suspended status message's `Metadata`
(`step` / `mode`). On continuation the framework rolls that message into
`ExecContext.History`, so the processor needs no process-local session map.

For push notifications, see [`examples/notify`](../notify) or [`examples/jwks`](../jwks)
(`memory.WithPushNotifications` + `push.Sender`). This basic example keeps push off
so it stays focused on the TaskHandle processor style.
