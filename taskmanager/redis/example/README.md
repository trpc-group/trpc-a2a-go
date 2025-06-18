# Redis TaskManager Example - Text Case Converter

A simple example demonstrating Redis TaskManager with a **Text to Lowercase Converter** service.

## Overview

- **Server**: Converts text to lowercase using Redis for storage
- **Client**: Tests both streaming and non-streaming modes

## Prerequisites

1. **Go 1.23.0+**
2. **Redis Server** running on localhost:6379

### Quick Redis Setup

**Using Docker:**
```bash
docker run -d -p 6379:6379 redis:7-alpine
```

**Using Docker Compose:**
```bash
cd examples/redis
docker-compose up -d
```

## Running the Example

### 1. Start Redis

Make sure Redis is running:
```bash
redis-cli ping
# Should return: PONG
```

### 2. Start the Server

```bash
cd taskmanager/redis/example/server
go run main.go
```

You should see:
```
Connected to Redis at localhost:6379 successfully
Starting Text Case Converter server on port 8080
```

### 3. Run the Client

In another terminal:
```bash
cd taskmanager/redis/example/client
go run main.go
```

Or with custom text:
```bash
go run main.go "Hello World! CONVERT THIS TEXT"
```

## Sample Output

### Non-streaming Mode
```
Test 1: Non-streaming conversion
Processing time: 45.123ms
Result 1: 'hello world! convert this text'
```

### Streaming Mode
```
Test 2: Streaming conversion with task updates
Streaming events:
Task msg-a1b2c3d4...: State=working
Artifact: processed-text-msg-a1b2c3d4...
   Name: Lowercase Text
   Content: 'hello world! convert this text'
Task completed! (Total time: 1.234s)
```

## What This Demonstrates

- **Redis Storage**: Messages, tasks, and artifacts persisted in Redis
- **Task Management**: Real-time task state transitions
- **Streaming Events**: Live updates via Server-Sent Events
- **Artifact Creation**: Generated content with metadata

## Command Line Options

**Server:**
```bash
go run main.go [redis_address] [server_port]
# Examples:
go run main.go                        # Default: localhost:6379, port 8080
go run main.go localhost:6380         # Custom Redis port
go run main.go localhost:6379 9000    # Custom server port
```

**Client:**
```bash
go run main.go [input_text] [server_url]
# Examples:
go run main.go                                    # Default text
go run main.go "Custom Text"                      # Custom input
go run main.go "Text" "http://localhost:9000/"    # Custom server
```

## Troubleshooting

**Redis Connection Failed:**
- Check if Redis is running: `redis-cli ping`
- Verify port 6379 is accessible

**Server Port in Use:**
- Use different port: `go run main.go localhost:6379 8081`

**Client Connection Failed:**
- Ensure server is running
- Check server URL and port 