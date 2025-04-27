# Push Notification with JWKS Authentication Example

This example demonstrates how to implement authenticated push notifications using JWKS (JSON Web Key Set) in an A2A application, specifically for asynchronous task processing.

## Overview

The example showcases the recommended approach for long-running tasks:

1. Client sends a task using the non-streaming API (`tasks/send`)
2. Client registers a webhook for push notifications 
3. Client disconnects or performs other work
4. Server processes the task asynchronously in the background
5. When task completes, server sends a push notification to the registered webhook
6. Client receives and verifies the notification

This pattern is ideal for:
- Long-running tasks that exceed typical request timeouts
- Scenarios where the client doesn't want to maintain a persistent connection
- Asynchronous workflows where clients need to be notified of completion

## Components

The example consists of two components:

1. **Server**: 
   - Generates RSA keys for JWT signing and exposes a JWKS endpoint
   - Processes tasks asynchronously in separate goroutines
   - Sends authenticated push notifications when tasks reach terminal states (completed/failed/canceled)

2. **Client**: 
   - Sets up a webhook server to receive push notifications
   - Verifies JWT signatures using the server's JWKS endpoint
   - Processes the notifications
   - Tracks task status changes

## Features

- RSA key generation and management for JWT signing
- JWKS endpoint for public key distribution
- JWT-based push notification authentication
- Automatic JWKS rotation and refresh
- Webhook server for receiving push notifications
- Asynchronous task processing
- Task status tracking

## Prerequisites

- Go 1.16 or higher
- The following Go packages:
  - `github.com/lestrrat-go/jwx/jwk`
  - `github.com/lestrrat-go/jwx/jws`
  - `github.com/lestrrat-go/jwx/jwt`
  - `github.com/tencent/trpc-a2a-go/a2a`

## Installation

1. Install required dependencies:

```bash
go get github.com/lestrrat-go/jwx
go get github.com/tencent/trpc-a2a-go
```

## Running the Example

### Start the Server

Run the server with default configuration:

```bash
go run server/main.go
```

Or customize the port:

```bash
go run server/main.go -port 8080
```

### Start the Client

Run the client in a separate terminal:

```bash
go run client/main.go
```

Or customize server and client addresses:

```bash
go run client/main.go -server-host localhost -server-port 8080 -client-host localhost -client-port 8081
```

## How It Works

1. The server generates RSA key pairs for JWT signing and exposes them via a JWKS endpoint.
2. The client sets up a webhook server to receive notifications.
3. The client registers for push notifications and sends a task to the server.
4. The server processes the task asynchronously and sends a push notification when complete.
5. The notification is signed with a JWT containing the task ID.
6. The client verifies the JWT signature using the server's JWKS endpoint.
7. If verification succeeds, the client processes the notification and updates its task status.

## Security Considerations

- The JWKS endpoint should be secured in production environments (HTTPS)
- Keys should be rotated regularly (this example rotates keys every hour)
- In production, implement proper error handling and retry logic

## API Usage

The example demonstrates these key A2A API features:

- `a2a.NewServer()` - Create an A2A server
- `server.WithJWKSEndpoint()` - Enable JWKS endpoint
- `a2aClient.SendTask()` - Send a task to the server (non-streaming API)
- `a2aClient.RegisterPushNotification()` - Register for push notifications

## License

This example is released under the same license as the A2A Go SDK. 