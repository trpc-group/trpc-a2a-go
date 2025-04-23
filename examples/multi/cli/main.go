package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"trpc.group/trpc-go/trpc-a2a-go/client"
	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

func main() {
	// Define command-line flags - only root agent is supported
	rootAgentURL := flag.String("url", "http://localhost:8080", "URL for the root agent")
	flag.Parse()

	// Create the A2A client
	a2aClient, err := client.NewA2AClient(*rootAgentURL)
	if err != nil {
		log.Fatal("Failed to create A2A client: %v", err)
	}

	fmt.Printf("Connected to root agent at %s\n", *rootAgentURL)
	fmt.Println("Type your requests and press Enter. Type 'exit' to quit.")

	// Create a scanner to read user input
	scanner := bufio.NewScanner(os.Stdin)

	// Main input loop
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}

		input := scanner.Text()
		if strings.ToLower(input) == "exit" {
			break
		}

		if input == "" {
			continue
		}

		// Send the task to the agent
		response, err := sendTask(a2aClient, input)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}

		// Display the response
		fmt.Printf("\nResponse: %s\n\n", response)
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
	}
}

// sendTask sends a task to the agent and waits for the response.
func sendTask(client *client.A2AClient, text string) (string, error) {
	ctx := context.Background()

	// Create the task parameters with the user's message
	params := protocol.SendTaskParams{
		Message: protocol.Message{
			Role: protocol.MessageRoleUser,
			Parts: []protocol.Part{
				protocol.NewTextPart(text),
			},
		},
	}

	// Send the task to the agent
	task, err := client.SendTasks(ctx, params)
	if err != nil {
		return "", fmt.Errorf("failed to send task: %w", err)
	}

	// Extract the response text from the task status message
	if task.Status.Message == nil {
		return "", fmt.Errorf("no response message from agent")
	}

	// Extract text from the response message
	return extractText(*task.Status.Message), nil
}

// extractText extracts the text content from a message.
func extractText(message protocol.Message) string {
	for _, part := range message.Parts {
		if textPart, ok := part.(protocol.TextPart); ok {
			return textPart.Text
		}
	}
	return ""
}
