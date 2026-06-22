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

	"trpc.group/trpc-go/trpc-a2a-go/v2/client"
	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
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
				[]*protocol.Part{protocol.NewTextPart("Process this streaming data chunk by chunk.")},
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
			Message: protocol.NewMessage(protocol.MessageRoleUser, []*protocol.Part{
				protocol.NewTextPart("Process this data."),
			}),
		}

		result, err := c.SendMessage(ctx, msgParams)
		if err != nil {
			log.Fatalf("Error sending task: %v.", err)
		}

		if msg := result.Message; msg != nil {
			text := extractTextFromMessage(msg)
			log.Infof("Final message: Role=%s, Text=%s", msg.Role, text)
		} else if task := result.Task; task != nil {
			log.Infof("Final task: ID=%s, State=%s", task.ID, task.Status.State)
		} else {
			log.Infof("Unexpected empty result")
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
func processStreamEvents(ctx context.Context, streamChan <-chan protocol.StreamResponse) {
	log.Info("Waiting for streaming updates...")

	for {
		select {
		case <-ctx.Done():
			log.Infof("Streaming context done: %v", ctx.Err())
			return
		case event, ok := <-streamChan:
			if !ok {
				log.Info("Stream closed.")
				if ctx.Err() != nil {
					log.Infof("Context error after stream close: %v", ctx.Err())
				}
				return
			}

			if msg := event.Message; msg != nil {
				text := extractTextFromMessage(msg)
				log.Infof("Received Message - MessageID: %s", msg.MessageID)
				log.Infof("  Message Text: %s", text)
			} else if au := event.ArtifactUpdate; au != nil {
				log.Infof("Received Artifact Update - TaskID: %s, ArtifactID: %s", au.TaskID, au.Artifact.ArtifactID)
				for _, part := range au.Artifact.Parts {
					if t := part.TextContent(); t != "" {
						log.Infof("  Artifact Text (Reversed Text): %s", t)
					}
				}
				if au.LastChunk != nil && *au.LastChunk {
					log.Info("Received final artifact update, waiting for final status.")
				}
			} else if task := event.Task; task != nil {
				log.Infof("Received Task - TaskID: %s, State: %s", task.ID, task.Status.State)
			} else if su := event.StatusUpdate; su != nil {
				log.Infof("Received Task Status Update - TaskID: %s, State: %s", su.TaskID, su.Status.State)
			} else {
				log.Infof("Received unknown event: %+v", event)
			}
		}
	}
}

func extractTextFromMessage(msg *protocol.Message) string {
	var text string
	for _, part := range msg.Parts {
		if t := part.TextContent(); t != "" {
			text += t
		}
	}
	return text
}
