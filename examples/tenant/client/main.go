// Package main implements a multi-agent A2A client using the v1.0 tenant model.
//
// All agents share one endpoint; the client picks an agent by setting the
// "tenant" field on each request (and on the agent-card lookup via "?tenant=").
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/client"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
)

var (
	host    = flag.String("host", "localhost:8080", "server host")
	message = flag.String("message", "Hello!", "message to send")
)

func main() {
	flag.Parse()

	baseURL := fmt.Sprintf("http://%s/", *host)
	fmt.Printf("=== Multi-Agent tRPC Client Demo ===\n")
	fmt.Printf("Server: %s (agents addressed by tenant)\n\n", baseURL)

	// One client for the shared endpoint; the tenant on each request selects the agent.
	a2aClient, err := client.NewA2AClient(baseURL, client.WithTimeout(30*time.Second))
	if err != nil {
		log.Fatalf("Failed to create A2A client: %v", err)
	}

	for _, tenant := range []string{"chatAgent", "workerAgent"} {
		fmt.Printf("--- Conversation with %s ---\n", tenant)

		agentCard, err := getAgentCard(baseURL, tenant)
		if err != nil {
			log.Printf("Failed to get agent card for %s: %v", tenant, err)
			continue
		}
		fmt.Printf("Agent: %s - %s\n", agentCard.Name, agentCard.Description)

		response, err := sendMessageToTenant(context.Background(), a2aClient, tenant, *message)
		if err != nil {
			log.Printf("Failed to send message to %s: %v", tenant, err)
			continue
		}
		fmt.Printf("Response: %s\n\n", response)
	}

	fmt.Println("=== Demo completed ===")
}

// getAgentCard retrieves a tenant's agent card from the shared endpoint via "?tenant=".
func getAgentCard(baseURL, tenant string) (*server.AgentCard, error) {
	cardURL := fmt.Sprintf("%s.well-known/agent-card.json?tenant=%s", baseURL, url.QueryEscape(tenant))

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Get(cardURL)
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

// sendMessageToTenant sends a message addressed to a specific tenant (agent).
func sendMessageToTenant(ctx context.Context, a2aClient *client.A2AClient, tenant, messageText string) (string, error) {
	fmt.Printf("User: %s\n", messageText)

	userMessage := protocol.NewMessage(
		protocol.MessageRoleUser,
		[]*protocol.Part{protocol.NewTextPart(messageText)},
	)

	params := protocol.SendMessageParams{
		Tenant:  tenant, // v1.0: route to the agent registered under this tenant.
		Message: userMessage,
		Configuration: &protocol.SendMessageConfiguration{
			ReturnImmediately:   boolPtr(false), // v1.0: false = wait for completion (was Blocking: true)
			AcceptedOutputModes: []string{"text"},
		},
	}

	messageResult, err := a2aClient.SendMessage(ctx, params)
	if err != nil {
		return "", fmt.Errorf("failed to send message: %w", err)
	}

	// v1.0: MessageResult is a Message/Task union.
	switch {
	case messageResult.GetMessage() != nil:
		return extractTextFromMessage(messageResult.GetMessage()), nil
	case messageResult.GetTask() != nil:
		task := messageResult.GetTask()
		if task.Status.Message != nil {
			return extractTextFromMessage(task.Status.Message), nil
		}
		return fmt.Sprintf("Task %s - State: %s", task.ID, task.Status.State), nil
	default:
		return "", fmt.Errorf("unexpected empty SendMessage response")
	}
}

// extractTextFromMessage extracts text content from a message.
func extractTextFromMessage(msg *protocol.Message) string {
	if msg == nil {
		return ""
	}
	for _, part := range msg.Parts {
		if t := part.TextContent(); t != "" {
			return t
		}
	}
	return "(no text content)"
}

// boolPtr returns a pointer to a boolean value.
func boolPtr(b bool) *bool {
	return &b
}
