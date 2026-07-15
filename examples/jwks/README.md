# JWT-Based Push Notifications with JWKS Example

This example demonstrates how to implement secure push notifications using JWT (JSON Web Tokens) with JWKS (JSON Web Key Set) in an A2A application, specifically for handling asynchronous task processing.

## Overview

The example showcases a robust approach for long-running tasks with secure notifications:

1. Client sends a task via the non-streaming API (`message/send` with
   `returnImmediately=true`, so the task ID is returned while the task runs)
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
- Creates a `SignedSender`, which generates an RSA signing key by default
- Exposes a JWKS endpoint (`.well-known/jwks.json`) to share public keys
- Processes tasks asynchronously in separate goroutines
- Signs notifications with a short-lived JWT that binds the request body
- Sends authenticated push notifications for task status changes

### Client Component
- Hosts a webhook server to receive push notifications
- Uses the SDK's ready-to-use, cached `pushauth.Verifier`
- Verifies JWT signatures, freshness, and the payload hash with
  `VerifyPushNotification`
- Tracks and displays task status changes

## Security Features

- **RSA-Based Cryptographic Signatures**: Uses RS256 algorithm for secure signing
- **JWKS for Key Distribution**: Standardized method to share public keys
- **Key ID Support**: Allows for seamless key rotation
- **Payload Hash Verification**: Prevents notification content tampering
- **Token Expiration Checking**: Prevents replay attacks
- **Cached Key Retrieval**: The SDK fetches and caches keys from the JWKS endpoint

## Running the Example

### Start the Server

```bash
go run server/main.go
```

Configure with optional flags:
```bash
go run server/main.go -port 8000
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
3. **Task Creation**: Client sends a task with `returnImmediately=true`
4. **Webhook Registration**: Client calls `SetPushNotification` with the task ID
5. **Task Processing**: Server processes the task asynchronously
6. **Notification Signing**: Server signs the serialized `StreamResponse` with
   an issued-at time, expiry, payload hash, and key ID
7. **Verification**: Client uses the matching JWKS key to verify the signature,
   token freshness, and payload hash

## API Usage

The example demonstrates these A2A API features:

- `server.NewA2AServer()` - Create an A2A server
- `pushauth.NewSignedSender()` - Create a signing sender with a generated key
- `memory.WithPushNotifications(push.Config{Sender: sender})` - Enable automatic delivery to registered webhooks
- `server.WithPushNotificationJWKSHandler()` - Publish the sender's verification keys
- `pushauth.NewVerifier()` - Configure webhook verification against JWKS
- `pushauth.Verifier.VerifyPushNotification()` - Verify a callback against JWKS
- `a2aClient.SendMessage()` - Send message via non-streaming API (with
  `returnImmediately=true` so the call returns before the task completes)
- `a2aClient.SetPushNotification()` - Explicitly register the webhook after the
  server returns a task ID

## License

This example is released under the Apache License Version 2.0, the same license as the trpc-a2a-go project.
