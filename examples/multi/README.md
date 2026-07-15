# Multi-agent example

This example demonstrates A2A-to-A2A orchestration. A root agent classifies a
request with simple deterministic rules, calls one of three sub-agents through
an `A2AClient`, and returns that agent's response.

The business responses are deliberately deterministic. No model API key or
external service is required, so the example stays focused on routing and A2A
client/server composition rather than an LLM or third-party API.

## Components

| Component | Default address | Purpose |
| --- | --- | --- |
| `root` | `localhost:8080` | Routes requests and delegates over A2A |
| `exchange` | `localhost:8081` | Handles currency-related requests |
| `creative` | `localhost:8082` | Handles creative-writing requests |
| `reimbursement` | `localhost:8083` | Handles expense requests |
| `cli` | connects to `localhost:8080` | Interactive client |

## Run

From the repository's `examples` directory, start each agent in a separate
terminal:

```bash
go run ./multi/exchange
go run ./multi/creative
go run ./multi/reimbursement
go run ./multi/root
```

Then start the client:

```bash
go run ./multi/cli
```

Try requests such as:

```text
Write a short poem about autumn
Convert USD to EUR
Submit an expense receipt
```

The root response identifies the selected sub-agent. Unknown requests receive
a short message listing the three supported request categories.

All addresses are configurable. For example:

```bash
go run ./multi/root \
  -port 9080 \
  -exchange-url http://localhost:9081/ \
  -creative-url http://localhost:9082/ \
  -reimbursement-url http://localhost:9083/
go run ./multi/cli -url http://localhost:9080/
```
