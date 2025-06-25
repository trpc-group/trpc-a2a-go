# Python A2A Client Example

This example demonstrates how to use the **official A2A Python SDK** to communicate with the Simple Text Reversal A2A Go server.

## Features

- ✅ **Simple & Clean**: Simplified codebase focused on core functionality
- ✅ **Official A2A SDK**: Uses the official A2A Python SDK
- ✅ **Text Reversal**: Send text and receive reversed responses
- ✅ **Test Suite**: Automated testing with verification
- ✅ **Streaming Support**: Test streaming functionality if supported
- ✅ **Agent Info**: Display server capabilities and metadata

## Server Functionality

The Go server provides a **text reversal service**:
- **Input**: "Hello World" → **Output**: "dlroW olleH"

This makes it easy to verify A2A communication is working correctly.

## Installation

1. **Install Python dependencies**:
   ```bash
   cd examples/simple/python_client
   pip install -r requirements.txt
   ```

2. **Start the Go server** (in another terminal):
   ```bash
   cd examples/simple/server
   go run main.go
   ```

## Usage

### Send Single Message

```bash
python official_a2a_client.py --message "Hello World"
```

**Output:**
```
🐍 Simple A2A Text Reversal Client
========================================
🔗 Connected to: http://localhost:8080
📤 Sending: 'Hello World'
📥 Received: 'Processed result: dlroW olleH'
✅ Client completed
```

### Test Suite

Run automated tests with verification:
```bash
python official_a2a_client.py --mode test
```

Tests 5 different messages and automatically verifies that the server correctly reverses each input.

### Streaming Mode

Test streaming functionality:
```bash
python official_a2a_client.py --mode streaming
```

This mode tests the `message/stream` endpoint if supported by the server. If streaming is not available, it falls back to regular messages.

**Example Output:**
```
🌊 Testing streaming functionality...

--- Streaming Test 1/3 ---
📤 Sending streaming message: 'Stream test: Hello World'
⚠️  Streaming not supported by SDK, using regular message
📥 Response: 'Processed result: dlroW olleH :tset maertS'
```

### Agent Information

```bash
python official_a2a_client.py --mode info
```

Display the agent card with server capabilities and metadata.

### Custom Server

```bash
python official_a2a_client.py --server http://localhost:8080 --message "Hello"
```

## Text Reversal Examples

| Your Input | Server Response |
|------------|----------------|
| "Python" | "nohtyP" |
| "12345" | "54321" |

## Code Structure

The simplified client consists of:

- **SimpleA2AClient**: Main client class with essential methods
  - `connect()`: Connect to server
  - `send_message()`: Send text and get reversed response
  - `send_streaming_message()`: Send streaming message with fallback
  - `get_agent_info()`: Fetch agent card
  - `close()`: Clean up connections

- **Helper Functions**:
  - `run_single_message()`: Send one message
  - `run_test_suite()`: Run automated tests
  - `run_streaming_test()`: Test streaming functionality
  - `show_agent_info()`: Display agent information

## Dependencies

- **a2a-sdk**: Official A2A Python SDK
- **httpx**: Modern async HTTP client

## Troubleshooting

### Connection Issues
```bash
# Make sure server is running
cd examples/simple/server && go run main.go
```

### Import Errors
```bash
pip install a2a-sdk httpx
```

### Streaming Issues
If streaming doesn't work:
- The client automatically falls back to regular messages
- Check if the server supports `message/stream` endpoint
- Verify the Agent Card shows `"streaming": true`

### Unexpected Output
If text isn't being reversed:
- Check server logs show "Processing message with input: [your text]"
- Verify response format starts with "Processed result: "
- Test with simple ASCII text first

## Streaming vs Regular Messages

- **Regular Messages** (`message/send`): Single request-response
- **Streaming Messages** (`message/stream`): Server can send multiple chunks
- **Fallback**: If streaming fails, automatically uses regular messages

The client demonstrates both approaches and handles graceful fallback. 