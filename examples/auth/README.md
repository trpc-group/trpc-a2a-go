# Authentication example

This example runs one A2A echo agent whose JSON-RPC endpoint accepts either a
JWT bearer token or an API key. The public AgentCard advertises both alternatives
using the A2A v1.0 security model. Authenticated callers can fetch the extended
AgentCard and call `SendMessage`.

The example does not configure OAuth2 or push notifications.

## Run

Run these commands from `examples/auth`.

Start the server:

```bash
go run ./server
```

On first start, the server creates `jwt-secret.key` with mode `0600`. It logs the
path, but never prints the secret or a complete JWT.

In another terminal, use the generated secret to run the JWT client:

```bash
go run ./client -auth jwt
```

Or use the demo API key:

```bash
go run ./client -auth apikey
```

Both clients first fetch the authenticated extended AgentCard, then send an echo
request. Useful overrides include:

```bash
go run ./server -port 9000 -agent-url http://localhost:9000
go run ./client -auth jwt -url http://localhost:9000 -message "hello"
go run ./client -auth apikey -api-key test-api-key
```

## Security boundary

- The shared HMAC secret makes the JWT flow easy to run locally, but it also lets
  the example client mint tokens. Production clients should receive credentials
  from a trusted identity system instead of receiving an issuer signing secret.
- `test-api-key` is a local demonstration credential. Production API keys need
  secure storage, rotation, revocation, and per-client identity.
- The default endpoint is plain HTTP on localhost. Use TLS and appropriate
  network controls outside a local development environment.
- The AgentCard endpoint is public by design so clients can discover the required
  authentication schemes. JSON-RPC methods, including the authenticated extended
  card operation, are protected by the configured auth provider.
