# JWT-Based Push Notifications with JWKS Example

This example demonstrates how to implement secure push notifications using JWT (JSON Web Tokens) with JWKS (JSON Web Key Set) in an A2A application, specifically for handling asynchronous task processing.

## Overview

The example showcases a robust approach for long-running tasks with secure notifications:

1. Client sends a task via the non-streaming API (`tasks/send`)
2. Client registers a webhook URL to receive push notifications
3. Server processes the task asynchronously in the background
4. When the task completes, the server sends a cryptographically signed push notification
5. Client verifies the notification's authenticity using JWKS before processing it

This pattern is ideal for:
- Long-running tasks that would exceed typical HTTP request timeouts
- Situations where maintaining persistent connections is impractical
- Asynchronous workflows requiring secure completion notifications
- Scenarios where notification authenticity must be cryptographically verified

## Components

The example consists of two primary components:

### Server Component
- Generates RSA key pairs for JWT signing
- Exposes a JWKS endpoint (`.well-known/jwks.json`) to share public keys
- Processes tasks asynchronously in separate goroutines
- Signs notifications with JWT (includes task ID, timestamp, and payload hash)
- Sends authenticated push notifications for task status changes

### Client Component
- Hosts a webhook server to receive push notifications
- Fetches and caches public keys from the server's JWKS endpoint
- Verifies JWT signatures on incoming notifications
- Includes fallback verification for improved compatibility
- Validates payload hash to prevent tampering
- Tracks and displays task status changes

## Security Features

- **RSA-Based Cryptographic Signatures**: Uses RS256 algorithm for secure signing
- **JWKS for Key Distribution**: Standardized method to share public keys
- **Key ID Support**: Allows for seamless key rotation
- **Payload Hash Verification**: Prevents notification content tampering
- **Token Expiration Checking**: Prevents replay attacks
- **Automatic Key Refresh**: Periodically updates JWKS from the server

## Running the Example

### Start the Server

```bash
go run server/main.go
```

Configure with optional flags:
```bash
go run server/main.go -port 8000 -notify-host localhost
```

### Start the Client

```bash
go run client/main.go
```

Configure with optional flags:
```bash
go run client/main.go -server-host localhost -server-port 8000 -webhook-host localhost -webhook-port 8001 -webhook-path /webhook
```

## Authentication Flow

1. **Key Generation**: Server generates RSA key pairs and assigns key IDs
2. **JWKS Publication**: Server exposes public keys via JWKS endpoint
3. **Task Registration**: Client registers webhook URL and sends a task
4. **Task Processing**: Server processes task asynchronously
5. **Notification Signing**: Server signs notification with private key and includes:
   - Task ID and status
   - Timestamp
   - Payload hash (SHA-256)
   - Key ID in JWT header
6. **JWT Verification**: Client verifies signature using public key from JWKS
7. **Payload Verification**: Client verifies payload hash matches content

## Advanced Features

- **Flexible Verification**: Multiple verification methods for compatibility
- **Debug Logging**: Comprehensive logging for debugging JWT issues
- **Periodic Key Refresh**: Background refresh of JWKS to handle key rotation
- **Task Status Tracking**: Client-side tracking of task state transitions
- **Enhanced Error Handling**: Detailed error reporting for authentication issues

## API Usage

The example demonstrates these A2A API features:

- `server.NewA2AServer()` - Create an A2A server
- `server.WithJWKSEndpoint()` - Enable JWKS endpoint
- `server.WithPushNotificationAuthenticator()` - Configure JWT authentication
- `a2aClient.SendTasks()` - Send task via non-streaming API
- `a2aClient.SetPushNotification()` - Register webhook for notifications

## License

This example is released under the Apache License Version 2.0, the same license as the trpc-a2a-go project. 