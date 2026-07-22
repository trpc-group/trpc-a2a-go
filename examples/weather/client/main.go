// Package main implements the weather client.
// This is the client-side counterpart to the Weather Agent server.
// It demonstrates how to use the trpc-a2a-go client library to interact with an A2A-compliant agent.
package main

import (
	"context"
	"flag"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/client"
	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

func main() {
	// --- 1. Parse Command-Line Arguments ---
	// The 'flag' package provides a simple way to create command-line tools.
	// We define flags for the agent's URL, a request timeout, and the city to query.
	agentURL := flag.String("agent", "http://localhost:8080/", "Target A2A agent URL")
	timeout := flag.Duration("timeout", 30*time.Second, "Request timeout (e.g., 30s, 1m)")
	city := flag.String("city", "shenzhen", "City name to get weather for")
	// flag.Parse() must be called to process the command-line arguments.
	flag.Parse()

	// --- 2. Create A2A Client ---
	// NewA2AClient is the factory function for creating a client instance.
	// We pass the agent's URL and configure a timeout using the WithTimeout option.
	a2aClient, err := client.NewA2AClient(*agentURL, client.WithTimeout(*timeout))
	if err != nil {
		log.Fatalf("Failed to create A2A client: %v", err)
	}

	log.Infof("Connecting to agent: %s (Timeout: %v)", *agentURL, *timeout)

	// --- 3. Construct the Message to Send ---
	// According to the A2A protocol, a message consists of a role and a list of parts.
	// Here, we send the city name as a single text part from the 'user'.
	userMessage := protocol.NewMessage(
		protocol.MessageRoleUser,
		[]protocol.Part{protocol.NewTextPart(*city)},
	)

	// --- 4. Construct Request Parameters ---
	// SendMessageParams bundles the message and its configuration.
	params := protocol.SendMessageParams{
		Message: userMessage,
		Configuration: &protocol.SendMessageConfiguration{
			// 'Blocking: true' means this is a standard, synchronous request.
			// The client will wait for the final result from the agent.
			Blocking: boolPtr(true),
			// 'AcceptedOutputModes' tells the agent what kind of content we can handle.
			// For this simple client, we only expect plain text.
			AcceptedOutputModes: []string{"text"},
		},
	}

	log.Infof("Requesting weather for city: %s", *city)

	// --- 5. Create a Context with Timeout ---
	// Context is crucial for controlling request lifecycle, especially for cancellation and deadlines.
	// context.WithTimeout creates a context that will be automatically cancelled after the timeout duration.
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	// 'defer cancel()' is a critical pattern in Go. It ensures that the context's resources
	// are released when the function returns, preventing resource leaks.
	defer cancel()

	// --- 6. Send the Message ---
	// a2aClient.SendMessage performs the actual JSON-RPC call to the agent.
	// It blocks until a response is received, an error occurs, or the context is cancelled.
	messageResult, err := a2aClient.SendMessage(ctx, params)
	if err != nil {
		log.Fatalf("Failed to send message: %v", err)
	}

	log.Infof("Message sent successfully!")

	// --- 7. Process and Print the Result ---
	// The result from a non-blocking call can be of various types (e.g., Message, Task).
	// We use a type switch to safely check what kind of result we received.
	switch result := messageResult.Result.(type) {
	case *protocol.Message:
		// This is the expected case for our simple weather agent.
		log.Infof("Received weather report:")
		// A message can have multiple parts, so we iterate through them.
		for _, part := range result.Parts {
			// We use a type assertion to check if the part is a TextPart.
			if textPart, ok := part.(*protocol.TextPart); ok {
				log.Infof(">> %s", textPart.Text)
			}
		}
	default:
		// If the agent returns something unexpected, we log its type.
		log.Infof("Received unexpected result type: %T", result)
	}
}

// boolPtr is a helper function to get a pointer to a boolean value.
// This is needed because many fields in the protocol structs are pointers
// to distinguish between a value being false and a value not being set at all.
func boolPtr(b bool) *bool {
	return &b
}