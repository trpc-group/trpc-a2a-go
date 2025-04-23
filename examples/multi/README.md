# Multi-Agent System Example

This example demonstrates a multi-agent system built with the A2A framework. The system consists of a root agent that routes requests to specialized sub-agents based on the content of the requests.

## System Architecture

The multi-agent system consists of:

*   A `Root Agent`: Listens on port 8080 and routes incoming requests to appropriate sub-agents based on content analysis.
*   Three specialized `Sub-Agents`:
    *   `Exchange Agent`: Provides currency exchange information. Listens on port 8081.
    *   `Creative Agent`: Handles creative writing requests (stories, poems, etc.). Listens on port 8082.
    *   `Reimbursement Agent`: Processes expense reimbursement requests. Listens on port 8083.

The system also includes a CLI client for interacting with the agents.

## Prerequisites

*   Go (version 1.21 or later recommended)
*   Google API key (Obtain for free from https://ai.google.dev/gemini-api/docs/api-key and set it as `GOOGLE_API_KEY` environment variable)

## Directory Structure

```
multi/
├── go.mod                  # Module dependencies
├── go.sum                  # Dependency checksums
├── README.md               # This documentation
├── root/                   # Root agent implementation
│   └── main.go
├── creative/               # Creative writing agent
│   ├── main.go
│   └── test.sh             # Test script
├── exchange/               # Currency exchange agent
│   ├── main.go
│   └── test.sh             # Test script
├── reimbursement/          # Reimbursement processing agent
│   ├── main.go  
│   └── test.sh             # Test script
└── cli/                    # Command-line interface
    └── main.go
```

## Building and Running the System

### Building the Agents

Navigate to the example directory and build each component:

```bash
cd a2a-go-github/examples/multi

# Build the root agent
go build -o root/root ./root

# Build the sub-agents
go build -o creative/creative ./creative
go build -o exchange/exchange ./exchange
go build -o reimbursement/reimbursement ./reimbursement

# Build the CLI client
go build -o cli/cli ./cli
```

### Running the Agents

Run each agent in a separate terminal window:

#### Terminal 1: Exchange Agent
```bash
cd a2a-go-github/examples/multi
./exchange/exchange -port 8081
```

#### Terminal 2: Creative Agent
```bash
cd a2a-go-github/examples/multi
./creative/creative -port 8082
```

#### Terminal 3: Reimbursement Agent
```bash
cd a2a-go-github/examples/multi
./reimbursement/reimbursement -port 8083
```

#### Terminal 4: Root Agent
```bash
cd a2a-go-github/examples/multi
./root/root -port 8080
```

Remember to set the `GOOGLE_API_KEY` environment variable to use Gemini model:

```bash
export GOOGLE_API_KEY=your_api_key
./root/root -port 8080
```

### Running in Background (Alternative)

Alternatively, you can run all agents in the background:

```bash
cd a2a-go-github/examples/multi
./exchange/exchange -port 8081 &
./creative/creative -port 8082 &
./reimbursement/reimbursement -port 8083 &
./root/root -port 8080 &
```

## Using the CLI to Interact with the System

Once all agents are running, use the CLI client to interact with the system:

```bash
cd a2a-go-github/examples/multi
./cli/cli
```

This will connect to the root agent at http://localhost:8080 by default. You can specify a different URL with the `-url` flag:

```bash
./cli/cli -url http://localhost:8080
```

After starting the CLI, you'll see a prompt where you can type requests:

```
Connected to root agent at http://localhost:8080
Type your requests and press Enter. Type 'exit' to quit.
> 
```

## Example Interactions

Try these example interactions:

### Creative Writing Requests
```
> Write a short poem about autumn
> Create a story about a space adventure
```

### Currency Exchange Requests
```
> What's the exchange rate from USD to EUR?
> Convert 100 USD to JPY
```

### Reimbursement Requests
```
> I need to get reimbursed for a $50 business lunch
> Submit a receipt for my office supplies purchase
```

The root agent will analyze your request and route it to the appropriate specialized agent, then return the response.

## How It Works

1. The CLI client sends your request to the root agent.
2. The root agent analyzes the content of your request to determine which specialized agent should handle it.
3. The root agent forwards your request to the appropriate sub-agent.
4. The sub-agent processes your request and sends back a response.
5. The root agent returns this response to the CLI client.
6. The CLI displays the response.

## Stopping the Agents

If you ran the agents in separate terminal windows, use Ctrl+C to stop each one.

If you ran them in the background, find and kill the processes:

```bash
# Find the PIDs
pgrep -f "root|creative|exchange|reimbursement"

# Kill the processes
kill $(pgrep -f "root|creative|exchange|reimbursement")
``` 
 