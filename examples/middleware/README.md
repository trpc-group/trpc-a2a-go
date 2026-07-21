# Middleware → ProcessMessage headers

Shows how `server.WithMiddleware` can copy HTTP headers into the request
context, and how `MessageProcessor.ProcessMessage` reads them with
`ctx.Value`.

The TaskManager detaches cancellation from the HTTP request (so a client
disconnect does not abort work) but **keeps context values**, so this pattern
is safe.

## Flow

```
client  --X-Request-ID / X-User-ID-->  headerMiddleware
                                          |
                                          v  context.WithValue
                                       JSON-RPC → TaskManager → ProcessMessage
                                          |
                                          v  ctx.Value(...)
                                       echo reply includes both headers
```

## Run

```bash
# terminal 1
go run ./server

# terminal 2
go run ./client -request-id req-42 -user-id bob -text "hi"
```

Expected client output:

```
reply: echo="hi" requestID="req-42" userID="bob"
```

Server log should show the same header values.
