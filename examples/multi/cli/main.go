// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

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
		response, err := sendMessage(a2aClient, input)
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

// sendMessage sends a message to the agent and waits for the response.
func sendMessage(client *client.A2AClient, text string) (string, error) {
	ctx := context.Background()

	// Create the message to send
	message := protocol.NewMessage(
		protocol.MessageRoleUser,
		[]protocol.Part{protocol.NewTextPart(text)},
	)

	// Prepare the message parameters
	params := protocol.SendMessageParams{
		Message: message,
	}

	// Send the message to the agent
	result, err := client.SendMessage(ctx, params)
	if err != nil {
		return "", fmt.Errorf("failed to send message: %w", err)
	}

	// Extract text from the response message
	switch result.Result.GetKind() {
	case protocol.KindMessage:
		msg := result.Result.(*protocol.Message)
		return extractText(*msg), nil
	case protocol.KindTask:
		task := result.Result.(*protocol.Task)
		if task.Status.Message != nil {
			return extractText(*task.Status.Message), nil
		}
		return "", fmt.Errorf("no response message from agent")
	default:
		return "", fmt.Errorf("unexpected response type: %T", result.Result)
	}
}

// extractText extracts the text content from a message.
func extractText(message protocol.Message) string {
	var result strings.Builder
	for _, part := range message.Parts {
		if textPart, ok := part.(*protocol.TextPart); ok {
			result.WriteString(textPart.Text)
		}
	}
	return result.String()
}
