# Redis TaskManager Example - Text Case Converter

A simple example demonstrating Redis TaskManager with a **Text to Lowercase Converter** service.

## Overview

- **Server**: Converts text to lowercase using Redis for storage
- **Client**: Tests both streaming and non-streaming modes with enhanced visual feedback

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
Starting Text Case Converter server on :8080
```

With custom parameters:
```bash
# Custom Redis address
go run main.go --redis_addr localhost:6380

# Custom server address
go run main.go --addr :9000

# Both custom
go run main.go --redis_addr redis.example.com:6379 --addr :8080

# Show help
go run main.go --help
```

### 3. Run the Client

In another terminal:
```bash
cd taskmanager/redis/example/client
go run main.go
```

With custom parameters:
```bash
# Custom text input
go run main.go --text "Hello World! CONVERT THIS TEXT"

# Custom server URL
go run main.go --addr http://localhost:9000/

# Only test streaming mode (enhanced visual effects)
go run main.go --streaming --verbose

# Only test non-streaming mode
go run main.go --non-streaming

# Enable verbose output for detailed information
go run main.go --verbose

# Show help
go run main.go --help
```

## Sample Output

### Non-streaming Mode
```
=== Text Case Converter Client ===
Server: http://localhost:8080/
Input text: 'Hello World! THIS IS A TEST MESSAGE.'

Test 1: Non-streaming conversion
→ Sending non-streaming request...
✓ Processing time: 45.123ms
📄 Result 1: 'hello world! this is a test message.'
```

### Streaming Mode (Enhanced Display)
```
Test 2: Streaming conversion with task updates
-> Starting streaming request...
[STREAMING] Processing events:
[TASK] ID: msg-a1b2c3d4...
[WORKING] Task State: working (Event #1)
   [MESSAGE] [STARTING] Initializing text conversion process...
[WORKING] Task State: working (Event #2)
   [MESSAGE] [ANALYZING] Processing input text (37 characters)...
[WORKING] Task State: working (Event #3)
   [MESSAGE] [PROCESSING] Converting text to lowercase...
[WORKING] Task State: working (Event #4)
   [MESSAGE] [ARTIFACT] Creating result artifact...
[ARTIFACT] ID: processed-text-msg-a1b2c3d4...
   [NAME] Text to Lowercase
   [DESC] Convert any text to lowercase
   [CONTENT] 'hello world! this is a test message.'
   [METADATA]
      operation: text_to_lower
      originalText: Hello World! THIS IS A TEST MESSAGE.
      originalLength: 37
      resultLength: 37
      processedAt: 2025-01-02T10:30:45Z
      processingTime: 1.7s
[SUCCESS] Task State: completed
   [MESSAGE] [COMPLETED] Text processing finished! Original: 'Hello World! THIS IS A TEST MESSAGE.' -> Lowercase: 'hello world! this is a test message.'
[FINISHED] Task completed! (Total time: 2.1s)
[COMPLETED] Stream finished (5 events, 2.1s total)
```

## What This Demonstrates

- **Redis Storage**: Messages, tasks, and artifacts persisted in Redis
- **Task Management**: Real-time task state transitions with multiple processing steps
- **Streaming Events**: Live updates via Server-Sent Events with clear text indicators
- **Artifact Creation**: Generated content with rich metadata
- **Command Line Interface**: Modern flag-based parameter handling
- **Code Standards**: Clean code structure with constants and proper naming conventions

## Command Line Options

**Server Options:**
```bash
go run main.go [OPTIONS]

Options:
  --redis_addr string    Redis server address (default "localhost:6379")
  --addr string         Server listen address (default ":8080")
  --help               Show help message
  --version            Show version information

Examples:
  go run main.go                                # Use default settings
  go run main.go --redis_addr localhost:6380   # Custom Redis port
  go run main.go --addr :9000                  # Custom server port
  go run main.go --redis_addr redis.example.com:6379 --addr :8080
```

**Client Options:**
```bash
go run main.go [OPTIONS]

Options:
  --addr string          Server URL (default "http://localhost:8080/")
  --text string          Input text to process (default "Hello World! THIS IS A TEST MESSAGE.")
  --streaming           Only test streaming mode
  --non-streaming       Only test non-streaming mode
  --verbose             Enable verbose output
  --help                Show help message
  --version             Show version information

Examples:
  go run main.go                                        # Use default settings
  go run main.go --text "Custom Text"                   # Custom input text
  go run main.go --addr http://localhost:9000/          # Custom server URL
  go run main.go --streaming                            # Only test streaming mode
  go run main.go --non-streaming                        # Only test non-streaming mode
  go run main.go --verbose                              # Enable verbose output
```

## Enhanced Features

### Visual Improvements
- **Text Indicators**: Clear prefixed labels for different event types
- **Progress Animation**: Text-based progress indicators during processing (verbose mode)
- **Structured Output**: Consistent formatting with bracketed prefixes
- **Detailed Metadata**: Rich information about processing steps (verbose mode)

### Streaming Enhancements
- **Multi-step Processing**: Server shows detailed progress through multiple phases:
  1. [STARTING] Initialize process
  2. [ANALYZING] Process input
  3. [PROCESSING] Convert text
  4. [ARTIFACT] Create artifact
  5. [COMPLETED] Finish processing
- **Rich Artifacts**: Detailed metadata including processing time and statistics
- **Event Counting**: Track number of streaming events received

### Code Quality Improvements
- **Constants Usage**: All hardcoded strings replaced with named constants
- **Function Separation**: Logical separation of concerns in processing functions
- **Type Safety**: Proper handling of pointer types and interfaces
- **Error Handling**: Comprehensive error checking and reporting

## Troubleshooting

**Redis Connection Failed:**
- Check if Redis is running: `redis-cli ping`
- Verify the Redis address: `--redis_addr localhost:6379`

**Server Port in Use:**
- Use different port: `--addr :8081`

**Client Connection Failed:**
- Ensure server is running
- Check server URL: `--addr http://localhost:8080/`

**Streaming Effects Not Visible:**
- Use `--streaming --verbose` for maximum visual effect
- Ensure you're testing the streaming mode only 