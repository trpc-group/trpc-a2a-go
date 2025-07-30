// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements a basic example of streaming a task from a client to a server.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/client"
	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
)

func main() {
	serverURL := "http://localhost:8089"

	// 1. Create a new client instance.
	c, err := client.NewA2AClient(serverURL)
	if err != nil {
		log.Fatalf("Error creating client: %v.", err)
	}

	// 2. Check for streaming capability by fetching the agent card
	streamingSupported, err := checkStreamingSupport(serverURL)
	if err != nil {
		log.Warnf("Could not determine streaming capability, assuming supported: %v.", err)
		streamingSupported = true // Assume supported if check fails
	}

	log.Infof("Server streaming capability: %t", streamingSupported)

	// Create context with timeout for the operation
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if streamingSupported {
		// 4. Use streaming approach if supported
		log.Info("Server supports streaming, using StreamTask...")

		msgParams := protocol.SendMessageParams{
			Message: protocol.NewMessage(
				protocol.MessageRoleUser,
				[]protocol.Part{protocol.NewTextPart("Process this streaming data chunk by chunk.")},
			),
		}

		streamChan, err := c.StreamMessage(ctx, msgParams)
		if err != nil {
			log.Fatalf("Error starting stream task: %v.", err)
		}

		processStreamEvents(ctx, streamChan)
	} else {
		// 5. Fallback to non-streaming approach
		log.Info("Server does not support streaming, using SendMessage...")

		msgParams := protocol.SendMessageParams{
			Message: protocol.NewMessage(protocol.MessageRoleUser, []protocol.Part{
				protocol.NewTextPart("Process this data."),
			}),
		}

		result, err := c.SendMessage(ctx, msgParams)
		if err != nil {
			log.Fatalf("Error sending task: %v.", err)
		}

		switch result.Result.GetKind() {
		case protocol.KindMessage:
			msg := result.Result.(*protocol.Message)
			text := extractTextFromMessage(msg)
			log.Infof("Final message: Role=%s, Text=%s", msg.Role, text)
		case protocol.KindTask:
			task := result.Result.(*protocol.Task)
			log.Infof("Final task: ID=%s, State=%s", task.ID, task.Status.State)
		default:
			log.Infof("Unexpected result type: %T", result.Result)
		}
	}
}

// checkStreamingSupport fetches the server's agent card to check if streaming is supported
func checkStreamingSupport(serverURL string) (bool, error) {
	// According to the A2A protocol, agent cards are available at protocol.AgentCardPath
	agentCardURL := serverURL
	if agentCardURL[len(agentCardURL)-1] != '/' {
		agentCardURL += "/"
	}
	// Use the constant defined in the protocol package instead of hardcoding the path
	agentCardURL += protocol.AgentCardPath[1:] // Remove leading slash as we already have one

	// Create a request with a short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, agentCardURL, nil)
	if err != nil {
		return false, fmt.Errorf("error creating request: %w", err)
	}

	// Make the request
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("error fetching agent card: %w", err)
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Read and parse the agent card
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("error reading response body: %w", err)
	}

	var agentCard server.AgentCard
	if err := json.Unmarshal(body, &agentCard); err != nil {
		return false, fmt.Errorf("error parsing agent card: %w", err)
	}

	// Handle the new *bool type for Streaming capability
	if agentCard.Capabilities.Streaming != nil {
		return *agentCard.Capabilities.Streaming, nil
	}
	return false, nil
}

// processStreamEvents handles events received from a streaming task
func processStreamEvents(ctx context.Context, streamChan <-chan protocol.StreamingMessageEvent) {
	log.Info("Waiting for streaming updates...")

	for {
		select {
		case <-ctx.Done():
			// Context timed out or was cancelled
			log.Infof("Streaming context done: %v", ctx.Err())
			return
		case event, ok := <-streamChan:
			if !ok {
				// Channel closed by the client/server
				log.Info("Stream closed.")
				if ctx.Err() != nil {
					log.Infof("Context error after stream close: %v", ctx.Err())
				}
				return
			}

			// Process the received event
			switch event.Result.GetKind() {
			case protocol.KindMessage:
				msg := event.Result.(*protocol.Message)
				text := extractTextFromMessage(msg)
				log.Infof("Received Message - MessageID: %s", msg.MessageID)
				log.Infof("  Message Text: %s", text)
			case protocol.KindTaskArtifactUpdate:
				artifact := event.Result.(*protocol.TaskArtifactUpdateEvent)
				log.Infof("Received Artifact Update - TaskID: %s, ArtifactID: %s", artifact.TaskID, artifact.Artifact.ArtifactID)
				for _, part := range artifact.Artifact.Parts {
					if textPart, ok := part.(*protocol.TextPart); ok {
						log.Infof("  Artifact Text (Reversed Text): %s", textPart.Text)
					}
				}

				// For artifact updates, we note it's the final artifact,
				// but we don't exit yet - per A2A spec, we should wait for the final status update
				if artifact.LastChunk != nil && *artifact.LastChunk {
					log.Info("Received final artifact update, waiting for final status.")
				}
			case protocol.KindTask:
				task := event.Result.(*protocol.Task)
				log.Infof("Received Task - TaskID: %s, State: %s", task.ID, task.Status.State)
			case protocol.KindTaskStatusUpdate:
				statusUpdate := event.Result.(*protocol.TaskStatusUpdateEvent)
				log.Infof("Received Task Status Update - TaskID: %s, State: %s", statusUpdate.TaskID, statusUpdate.Status.State)
			default:
				log.Infof("Received unknown event type: %T %v", event, event)
			}
		}
	}
}

func extractTextFromMessage(msg *protocol.Message) string {
	var text string
	for _, part := range msg.Parts {
		if textPart, ok := part.(protocol.TextPart); ok {
			text += textPart.Text
		}
	}
	return text
}
