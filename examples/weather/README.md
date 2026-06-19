# Weather Agent Example

This example demonstrates a simple, non-streaming A2A (Agent-to-Agent) agent that provides real-time weather information.

It consists of two parts:
1.  `server`: The A2A agent that listens for requests.
2.  `client`: A command-line client to interact with the agent.

The agent fetches weather data from the public API at [wttr.in](https://wttr.in/).

## How to Run

You will need two separate terminal windows to run the server and the client.

### 1. Run the Server

In your first terminal, navigate to the server directory and run the `main.go` file:

```bash
cd examples/weather/server
go run .
```

The server will start and listen on `http://localhost:8080`. You should see the following output:

```
INFO[YYYY-MM-DD HH:MM:SS] Starting Weather Agent server on localhost:8080...
```

### 2. Run the Client

In your second terminal, navigate to the client directory. You can run the client to get the weather for a specific city.

**To get the weather for the default city (shenzhen):**

```bash
cd examples/weather/client
go run .
```

**To get the weather for a different city (e.g., London):**

Use the `-city` command-line flag:

```bash
cd examples/weather/client
go run . -city=London
```

### Expected Output

If successful, the client will print output similar to this:

```
INFO[YYYY-MM-DD HH:MM:SS] Connecting to agent: http://localhost:8080/ (Timeout: 30s)
INFO[YYYY-MM-DD HH:MM:SS] Requesting weather for city: London
INFO[YYYY-MM-DD HH:MM:SS] Message sent successfully!
INFO[YYYY-MM-DD HH:MM:SS] Received weather report:
INFO[YYYY-MM-DD HH:MM:SS] >> London: ⛅️  +18°C
```
