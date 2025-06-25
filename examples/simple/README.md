# Simple A2A Example - Text Reverser

This is a simple example demonstrating the tRPC A2A Go implementation with a **text reversal server** and both Go and Python clients.

## Overview

The Simple A2A Example includes:

- **Server**: A text reversal service that **reverses input text character by character**
- **Go Client**: A command-line client written in Go
- **Python Client**: A comprehensive client using the official A2A Python SDK

### Server Functionality

The server implements a simple but clear demonstration of A2A protocol by providing a **text reversal service**:

- **Input**: Any text string (e.g., "Hello World")
- **Processing**: Reverses the character order
- **Output**: Reversed text string (e.g., "dlroW olleH")

This simple functionality makes it easy to verify that the A2A communication is working correctly - you can immediately see if your input was processed by checking if the output is the reverse of your input.

## Directory Structure

```
examples/simple/
├── server/           # Go server implementation
│   └── main.go       # Text reversal A2A server
├── client/           # Go client implementation  
│   └── main.go       # Command-line Go client
├── python_client/    # Python client implementation
│   ├── official_a2a_client.py  # Official A2A SDK client
│   ├── requirements.txt        # Python dependencies
│   ├── run_demo.sh            # Demo script
│   └── README.md              # Python client documentation
├── simple-server     # Compiled server binary
└── simple-client     # Compiled client binary
```

## Quick Start

### 1. Start the Server

```bash
cd examples/simple/server
go run main.go
```

The server will start on `http://localhost:8080` and provide:
- **Text reversal service** via A2A protocol
- Agent Card at `/.well-known/agent.json`
- JSON-RPC 2.0 endpoint at `/jsonrpc`

### 2. Test with Go Client

```bash
cd examples/simple/client
go run main.go --message "Hello World"
# Expected output: "dlroW olleH"
```

### 3. Test with Python Client

```bash
cd examples/simple/python_client
pip install -r requirements.txt
python official_a2a_client.py --message "Hello World"
# Expected output: "Processed result: dlroW olleH"
```

## Server Features

The Simple A2A Server demonstrates:

- ✅ **A2A Protocol Compliance**: Full implementation of A2A specification
- ✅ **JSON-RPC 2.0**: Standard JSON-RPC request/response handling
- ✅ **Agent Card**: Metadata discovery via `/.well-known/agent.json`
- ✅ **Text Reversal Processing**: **Reverses input text character by character**
- ✅ **Streaming Support**: Supports `message/stream` for streaming responses
- ✅ **Error Handling**: Comprehensive error handling and validation

### Text Reversal Examples

| Input | Output |
|-------|--------|
| "Hello" | "olleH" |
| "A2A Protocol" | "locotorP A2A" |
| "12345" | "54321" |
| "Hello, 世界!" | "!界世 ,olleH" |

## Client Features

### Go Client
- Command-line interface
- Direct A2A protocol communication
- JSON-RPC 2.0 support

### Python Client (Official SDK)
- Uses official A2A Python SDK
- Multiple run modes (test, interactive, streaming)
- Comprehensive error handling
- Agent Card discovery
- Full type safety

## Testing

Both clients can be used to test the text reversal server:

```bash
# Test with Go client
cd client && go run main.go --message "test message"
# Expected: "egassem tset"

# Test with Python client  
cd python_client && python official_a2a_client.py --mode test
# Runs 5 different text reversal tests
```

## Agent Card

The server provides an Agent Card at `/.well-known/agent.json`:

```json
{
  "name": "Simple A2A Example Server",
  "description": "A simple example A2A server that reverses text character by character",
  "url": "http://localhost:8080/",
  "provider": {
    "organization": "tRPC-A2A-Go Examples"
  },
  "version": "1.0.0",
  "capabilities": {
    "streaming": true,
    "pushNotifications": false,
    "stateTransitionHistory": true
  },
  "skills": [
    {
      "id": "text_reversal",
      "name": "Text Reversal",
      "description": "Reverses the input text character by character",
      "examples": [
        "Input: 'Hello World' → Output: 'dlroW olleH'"
      ]
    }
  ]
}
```

## Use Cases

This example is perfect for:

- **Learning A2A protocol basics** with a simple, verifiable function
- **Testing A2A client implementations** with predictable output
- **Understanding JSON-RPC 2.0 communication** through text reversal
- **Developing A2A-compatible applications** using a clear example
- **Prototyping text processing agents** with simple string manipulation 