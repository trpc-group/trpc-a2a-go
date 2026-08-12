# Go/Python Multi-Tenant Interoperability

This example uses the official Python [`a2a-sdk`](https://github.com/a2aproject/a2a-python) together with `trpc-a2a-go`. The `server` and `client` directories each provide both Go and Python implementations, so either language can run against the other.

The dependency is pinned to `a2a-sdk[http-server]==1.1.2` in `pyproject.toml`. Python 3.10 or newer and Go 1.20 or newer are required.

## What it demonstrates

- Two tenants, `tenant-a` and `tenant-b`, share one server process and one JSON-RPC endpoint.
- The clients intentionally reuse the same context ID and message ID for both tenants.
- New tasks and task continuations use `SendStreamingMessage`; both clients print every status and artifact update.
- Each tenant can create, continue, get, list, subscribe to, and cancel its own tasks.
- Cross-tenant `GetTask` and `CancelTask` requests return task-not-found instead of exposing another tenant's task.
- The Python task store resolves ownership from the request tenant, and the Python cancellation path verifies tenant ownership before looking up an active task.
- Both clients understand the Go tenant-card URL (`/.well-known/agent-card.json?tenant=tenant-a`) and the Python tenant-card URL (`/tenant-a/.well-known/agent-card.json`).

## Main paths

The ordinary and continuation paths are streaming-first. The clients accept a full `Task` frame or a lifecycle update carrying the task ID, then fetch the aggregated result with `GetTask`:

```text
SendStreamingMessage -> Task or taskId-bearing update -> Status/Artifact updates -> terminal/interrupted Status -> GetTask
```

The cancellable path demonstrates the asynchronous lifecycle used by `examples/basic`:

```text
SendMessage(returnImmediately=true) -> SubscribeToTask -> Task -> CancelTask -> CANCELED update -> GetTask
```

After each tenant's positive path, the clients use `GetTask` and `ListTasks` to verify the stored result. They then repeat cross-tenant `GetTask` and `CancelTask` calls and require task-not-found, proving that identical client-supplied IDs do not cross the tenant boundary.

## Layout

```text
python-interop/
├── client/
│   ├── main.go
│   └── main.py
├── server/
│   ├── main.go
│   └── main.py
├── pyproject.toml
└── uv.lock
```

## Install the Python SDK

From the repository root:

```bash
uv sync --project examples/python-interop
```

## Run Go server with Python client

Start the Go server:

```bash
cd examples
GOWORK=off go run ./python-interop/server
```

In another shell, from the repository root, run the official Python SDK client:

```bash
uv run --project examples/python-interop python examples/python-interop/client/main.py
```

## Run Python server with Go client

Start the official Python SDK server from the repository root:

```bash
uv run --project examples/python-interop python examples/python-interop/server/main.py
```

In another shell:

```bash
cd examples
GOWORK=off go run ./python-interop/client
```

Use `--port` for the Python server, `-port` for the Go server, and `--agent` or `-agent` for the corresponding clients when changing the default `http://localhost:8080/` address.

The same-language combinations also work and are useful for comparison: Go server with Go client, or Python server with Python client.

## Security boundary

This example focuses on resource isolation after a tenant has been selected. In production, do not trust an arbitrary tenant value from the request body; authenticate the caller and derive or authorize the tenant before dispatching the A2A request.
