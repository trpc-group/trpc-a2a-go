# Push notifications

Two examples show the ways an agent can deliver push notifications. Each keeps
the A2A server and webhook client in separate programs so their responsibilities
and logs are easy to follow.

Both wire a signing `pushauth.SignedSender` into the TaskManager and pass its
`JWKSHandler()` to the server with `WithPushNotificationJWKSHandler`, so the
webhook can verify every notification. The only difference is who decides when
to deliver.

| | wiring | who delivers |
| --- | --- | --- |
| [`auto/`](auto) | `memory.WithPushNotifications(sender)` | the framework, on each significant task state |
| [`manual/`](manual) | `memory.WithPushConfig(push.Config{Sender: sender, ManualDelivery: true})` | the agent, from inside the processor, on its own schedule |

- **auto** — the processor just completes the task; the framework POSTs the
  terminal `StreamResponse` to every registered webhook.
- **manual** — automatic dispatch is off; the processor calls the `SignedSender`
  itself, here pushing a mid-task milestone and the final result. Registration,
  the JWKS endpoint, and the advertised capability all still work. Caveat: the
  processor only sees
  the webhook sent inline with the message (`ExecContext.PushConfig`); configs
  registered afterwards via `tasks/pushNotificationConfig/set` live in the
  manager's store, so an agent that must honor those too needs the TaskManager
  reference (`OnPushNotificationList`).

## Run

Start one mode's server first, then its client in another terminal:

```bash
# Automatic delivery
go run ./auto/server
go run ./auto/client

# Manual delivery
go run ./manual/server
go run ./manual/client
```

Both modes use the agent at `http://localhost:8000` and the client webhook at
`http://localhost:8001/notify` by default. Use `-port` on a server or
`-agent-url`, `-webhook-listen`, and `-webhook-url` on a client to override them.

See [`../jwks`](../jwks) for a larger JWT/JWKS example, and
`docs/mkdocs/*/server.md` for the full wiring reference.

For unsigned delivery, use `push.NewHTTPSender()` and omit
`WithPushNotificationJWKSHandler`.
