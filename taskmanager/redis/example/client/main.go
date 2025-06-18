package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/client"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

func main() {
	// Parse command line arguments
	var serverURL string = "http://localhost:8080/"
	var inputText string = "Hello World! THIS IS A TEST MESSAGE."

	if len(os.Args) > 1 {
		inputText = os.Args[1]
	}
	if len(os.Args) > 2 {
		serverURL = os.Args[2]
	}

	// Create A2A client
	a2aClient, err := client.NewA2AClient(
		serverURL,
		client.WithTimeout(30*time.Second),
	)
	if err != nil {
		log.Fatalf("Failed to create A2A client: %v", err)
	}

	ctx := context.Background()

	// Display what we're going to do
	fmt.Printf("=== Text Case Converter Client ===\n")
	fmt.Printf("Server: %s\n", serverURL)
	fmt.Printf("Input text: '%s'\n\n", inputText)

	// Test 1: Non-streaming conversion
	fmt.Println("Test 1: Non-streaming conversion")
	testNonStreaming(ctx, a2aClient, inputText)

	fmt.Println("\n" + strings.Repeat("=", 50) + "\n")

	// Test 2: Streaming conversion
	fmt.Println("Test 2: Streaming conversion with task updates")
	testStreaming(ctx, a2aClient, inputText)
}

func testNonStreaming(ctx context.Context, client *client.A2AClient, inputText string) {
	message := protocol.Message{
		Role: protocol.MessageRoleUser,
		Parts: []protocol.Part{
			protocol.NewTextPart(inputText),
		},
	}

	params := protocol.SendMessageParams{
		Message: message,
		Configuration: &protocol.SendMessageConfiguration{
			Blocking: boolPtr(true), // Non-streaming
		},
	}

	start := time.Now()
	result, err := client.SendMessage(ctx, params)
	duration := time.Since(start)

	if err != nil {
		log.Printf("Error: %v", err)
		return
	}

	fmt.Printf("Processing time: %v\n", duration)

	if response, ok := result.Result.(*protocol.Message); ok {
		for i, part := range response.Parts {
			if textPart, ok := part.(*protocol.TextPart); ok {
				fmt.Printf("Result %d: '%s'\n", i+1, textPart.Text)
			}
		}
	} else {
		fmt.Printf("Unexpected result type: %T\n", result.Result)
	}
}

func testStreaming(ctx context.Context, client *client.A2AClient, inputText string) {
	message := protocol.Message{
		Role: protocol.MessageRoleUser,
		Parts: []protocol.Part{
			protocol.NewTextPart(inputText),
		},
	}

	params := protocol.SendMessageParams{
		Message: message,
		Configuration: &protocol.SendMessageConfiguration{
			Blocking: boolPtr(false), // Streaming
		},
	}

	start := time.Now()
	eventChan, err := client.StreamMessage(ctx, params)
	if err != nil {
		log.Printf("Failed to start streaming: %v", err)
		return
	}

	fmt.Println("Streaming events:")
	timeout := time.After(30 * time.Second)

	var taskID string
	eventCount := 0

	for {
		select {
		case event, ok := <-eventChan:
			if !ok {
				duration := time.Since(start)
				fmt.Printf("Stream completed (%d events, %v total)\n", eventCount, duration)
				return
			}

			eventCount++

			switch result := event.Result.(type) {
			case *protocol.TaskStatusUpdateEvent:
				if taskID == "" {
					taskID = result.TaskID
				}

				fmt.Printf("Task %s: State=%s\n", result.TaskID, result.Status.State)

				if result.Status.Message != nil {
					for _, part := range result.Status.Message.Parts {
						if textPart, ok := part.(*protocol.TextPart); ok {
							fmt.Printf("   Message: %s\n", textPart.Text)
						}
					}
				}

				if result.IsFinal() {
					duration := time.Since(start)
					fmt.Printf("Task completed! (Total time: %v)\n", duration)
					return
				}

			case *protocol.TaskArtifactUpdateEvent:
				fmt.Printf("Artifact: %s\n", result.Artifact.ArtifactID)

				if result.Artifact.Name != nil {
					fmt.Printf("   Name: %s\n", *result.Artifact.Name)
				}

				if result.Artifact.Description != nil {
					fmt.Printf("   Description: %s\n", *result.Artifact.Description)
				}

				for _, part := range result.Artifact.Parts {
					if textPart, ok := part.(*protocol.TextPart); ok {
						fmt.Printf("   Content: '%s'\n", textPart.Text)
					}
				}

				// Show metadata if available
				if len(result.Artifact.Metadata) > 0 {
					fmt.Printf("   Metadata:\n")
					for key, value := range result.Artifact.Metadata {
						fmt.Printf("      %s: %v\n", key, value)
					}
				}

			default:
				fmt.Printf("Unknown event type: %T\n", result)
			}

		case <-timeout:
			fmt.Println("Timeout waiting for events")
			return
		}
	}
}

func boolPtr(b bool) *bool {
	return &b
}
