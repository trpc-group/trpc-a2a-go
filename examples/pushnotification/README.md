# Push Notification with JWKS Authentication Example

This example demonstrates how to implement authenticated push notifications using JWKS (JSON Web Key Set) in an A2A application.

## Overview

The example consists of two components:

1. **Server**: Generates RSA keys for JWT signing, exposes a JWKS endpoint, and sends authenticated push notifications when tasks are processed.
2. **Client**: Receives push notifications, verifies JWT signatures using the server's JWKS endpoint, and processes the notifications.

## Features

- RSA key generation and management for JWT signing
- JWKS endpoint for public key distribution
- JWT-based push notification authentication
- Automatic JWKS rotation and refresh
- Webhook server for receiving push notifications

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
2. The client sends a task to the server and registers for push notifications.
3. The server processes the task and sends a push notification to the client's webhook.
4. The notification is signed with a JWT containing the task ID.
5. The client verifies the JWT signature using the server's JWKS endpoint.
6. If verification succeeds, the client processes the notification.

## Security Considerations

- The JWKS endpoint should be secured in production environments (HTTPS)
- Keys should be rotated regularly (this example rotates keys every hour)
- In production, implement proper error handling and retry logic

## API Usage

The example demonstrates these key A2A API features:

- `a2a.NewServer()` - Create an A2A server
- `server.WithJWKSEndpoint()` - Enable JWKS endpoint
- `a2aClient.SendTask()` - Send a task to the server
- `a2aClient.RegisterPushNotification()` - Register for push notifications

## License

This example is released under the same license as the A2A Go SDK. 