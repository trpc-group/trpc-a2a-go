// Package main implements a simple A2A client example.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/client"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
)

var (
	host    = flag.String("host", "localhost:8080", "server host")
	message = flag.String("message", "Hello!", "message to send")
)

func main() {
	flag.Parse()

	fmt.Printf("=== Multi-Agent tRPC Client Demo ===\n")
	fmt.Printf("Server: http://%s\n", *host)
	fmt.Printf("Testing both agents...\n\n")

	// Test both agents
	agents := []string{"chatAgent", "workerAgent"}
	for _, agentName := range agents {
		fmt.Printf("--- Conversation with %s ---\n", agentName)

		// Create agent URL
		agentURL := fmt.Sprintf("http://%s/api/v1/agent/%s/", *host, agentName)

		// Get agent card first
		agentCard, err := getAgentCard(agentURL)
		if err != nil {
			log.Printf("Failed to get agent card for %s: %v", agentName, err)
			continue
		}
		fmt.Printf("Agent: %s - %s\n", agentCard.Name, agentCard.Description)

		// Create A2A client for this agent
		a2aClient, err := client.NewA2AClient(agentURL, client.WithTimeout(30*time.Second))
		if err != nil {
			log.Printf("Failed to create A2A client for %s: %v", agentName, err)
			continue
		}

		// Send message using A2A client
		response, err := sendMessageToAgent(context.Background(), a2aClient, *message)
		if err != nil {
			log.Printf("Failed to send message to %s: %v", agentName, err)
			continue
		}
		fmt.Printf("Response: %s\n\n", response)
	}

	fmt.Println("=== Demo completed ===")
}

// getAgentCard retrieves the agent card for the specified agent
func getAgentCard(agentURL string) (*server.AgentCard, error) {
	cardURL := fmt.Sprintf("%s.well-known/agent.json", agentURL)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(cardURL)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: failed to get agent card", resp.StatusCode)
	}

	var agentCard server.AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&agentCard); err != nil {
		return nil, fmt.Errorf("failed to parse agent card: %w", err)
	}

	return &agentCard, nil
}

// sendMessageToAgent sends a message to the specified agent using A2A client
func sendMessageToAgent(ctx context.Context, a2aClient *client.A2AClient, messageText string) (string, error) {

	fmt.Printf("User: %s\n", messageText)
	// Create the message to send
	userMessage := protocol.NewMessage(
		protocol.MessageRoleUser,
		[]protocol.Part{protocol.NewTextPart(messageText)},
	)

	// Create message parameters
	params := protocol.SendMessageParams{
		Message: userMessage,
		Configuration: &protocol.SendMessageConfiguration{
			Blocking:            boolPtr(true), // Use blocking mode for simplicity
			AcceptedOutputModes: []string{"text"},
		},
	}

	// Send message using A2A client
	messageResult, err := a2aClient.SendMessage(ctx, params)
	if err != nil {
		return "", fmt.Errorf("failed to send message: %w", err)
	}

	// Extract text from the response
	switch result := messageResult.Result.(type) {
	case *protocol.Message:
		return extractTextFromMessage(result), nil
	case *protocol.Task:
		if result.Status.Message != nil {
			return extractTextFromMessage(result.Status.Message), nil
		}
		return fmt.Sprintf("Task %s - State: %s", result.ID, result.Status.State), nil
	default:
		return fmt.Sprintf("Unknown result type: %T", result), nil
	}
}

// extractTextFromMessage extracts text content from a message
func extractTextFromMessage(msg *protocol.Message) string {
	if msg == nil {
		return ""
	}

	for _, part := range msg.Parts {
		if textPart, ok := part.(*protocol.TextPart); ok {
			return textPart.Text
		}
	}

	return "(no text content)"
}

// boolPtr returns a pointer to a boolean value
func boolPtr(b bool) *bool {
	return &b
}
