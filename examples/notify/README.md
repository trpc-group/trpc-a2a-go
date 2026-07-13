# Push notifications

Two self-contained programs showing the two ways an agent delivers push
notifications. Each runs server, webhook receiver, and client in one process —
just `go run .` in the subdirectory.

Both wire a signing `pushauth.Notifier` into the TaskManager (as the Sender) and
pass its identity to the server (`WithPushNotificationAuthenticator`), which then
publishes the JWKS; the webhook verifies every notification against it. The only
difference is who decides when to deliver.

| | wiring | who delivers |
| --- | --- | --- |
| [`auto/`](auto) | `memory.WithPushNotifications(notifier)` | the framework, on each significant task state |
| [`manual/`](manual) | `memory.WithPushNotificationsConfig(push.Config{Sender: notifier, ManualDelivery: true})` | the agent, from inside the processor, on its own schedule |

- **auto** — the processor just completes the task; the framework POSTs the
  terminal `StreamResponse` to every registered webhook.
- **manual** — automatic dispatch is off; the processor calls the Notifier
  itself, here pushing a mid-task milestone and the final result. Registration,
  the JWKS endpoint, and the advertised capability all still work (the identity
  is passed to the server the same way). Caveat: the processor only sees
  the webhook sent inline with the message (`ExecContext.PushConfig`); configs
  registered afterwards via `tasks/pushNotificationConfig/set` live in the
  manager's store, so an agent that must honor those too needs the TaskManager
  reference (`OnPushNotificationList`).

See [`../jwks`](../jwks) for a two-process (separate client) variant, and
`docs/mkdocs/*/server.md` for the full wiring reference.
