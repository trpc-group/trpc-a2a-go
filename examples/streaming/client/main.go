// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
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

	"github.com/google/uuid"

	"trpc.group/trpc-go/trpc-a2a-go/client"
	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
)

func main() {
	serverURL := "http://localhost:8080"

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

	// 3. Define the task specification with a unique ID
	taskID := fmt.Sprintf("task-%d-%s", time.Now().UnixNano(), uuid.New().String())
	message := protocol.Message{
		Role: protocol.MessageRoleUser,
		Parts: []protocol.Part{
			protocol.NewTextPart("Process this streaming data chunk by chunk."),
		},
	}

	// Create context with timeout for the operation
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if streamingSupported {
		// 4. Use streaming approach if supported
		log.Info("Server supports streaming, using StreamTask...")

		taskParams := protocol.SendTaskParams{
			ID:      taskID,
			Message: message,
		}

		streamChan, err := c.StreamTask(ctx, taskParams)
		if err != nil {
			log.Fatalf("Error starting stream task: %v.", err)
		}

		processStreamEvents(ctx, streamChan)
	} else {
		// 5. Fallback to non-streaming approach
		log.Info("Server does not support streaming, using SendTasks...")

		taskParams := protocol.SendTaskParams{
			ID:      taskID,
			Message: message,
		}

		task, err := c.SendTasks(ctx, taskParams)
		if err != nil {
			log.Fatalf("Error sending task: %v.", err)
		}

		log.Infof("Task completed with state: %s", task.Status.State)
		if task.Status.Message != nil {
			log.Infof("Final message: Role=%s", task.Status.Message.Role)
			for i, part := range task.Status.Message.Parts {
				if textPart, ok := part.(protocol.TextPart); ok {
					log.Infof("  Part %d text: %s", i, textPart.Text)
				}
			}
		}

		if len(task.Artifacts) > 0 {
			log.Infof("Task produced %d artifacts:", len(task.Artifacts))
			for i, artifact := range task.Artifacts {
				log.Infof("  Artifact %d: %s", i, getArtifactName(artifact))
			}
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

	return agentCard.Capabilities.Streaming, nil
}

// processStreamEvents handles events received from a streaming task
func processStreamEvents(ctx context.Context, streamChan <-chan protocol.TaskEvent) {
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
			switch e := event.(type) {
			case protocol.TaskStatusUpdateEvent:
				log.Infof("Received Status Update - TaskID: %s, State: %s, Final: %t", e.ID, e.Status.State, e.Final)
				if e.Status.Message != nil {
					log.Infof("  Status Message: Role=%s, Parts=%+v", e.Status.Message.Role, e.Status.Message.Parts)
				}

				// Exit when we receive a final status update (indicating a terminal state)
				// Per A2A spec, this should be the definitive way to know the task is complete
				if e.IsFinal() {
					if e.Status.State == protocol.TaskStateCompleted {
						log.Info("Task completed successfully.")
					} else if e.Status.State == protocol.TaskStateFailed {
						log.Info("Task failed.")
					} else if e.Status.State == protocol.TaskStateCanceled {
						log.Info("Task was canceled.")
					}
					log.Info("Received final status update, exiting.")
					return
				}
			case protocol.TaskArtifactUpdateEvent:
				log.Infof("Received Artifact Update - TaskID: %s, Index: %d, Append: %v, LastChunk: %v",
					e.ID, e.Artifact.Index, e.Artifact.Append, e.Artifact.LastChunk)
				log.Infof("  Artifact Parts: %+v", e.Artifact.Parts)

				// For artifact updates, we note it's the final artifact,
				// but we don't exit yet - per A2A spec, we should wait for the final status update
				if e.IsFinal() {
					log.Info("Received final artifact update, waiting for final status.")
				}
			default:
				log.Infof("Received unknown event type: %T %v", event, event)
			}
		}
	}
}

// getArtifactName returns the name of an artifact or a default if name is nil
func getArtifactName(artifact protocol.Artifact) string {
	if artifact.Name != nil {
		return *artifact.Name
	}
	return fmt.Sprintf("Unnamed artifact (index: %d)", artifact.Index)
}
