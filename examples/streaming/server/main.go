// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements the server side of the v2 streaming example.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

const chunkInterval = 400 * time.Millisecond

type streamingProcessor struct{}

func (p *streamingProcessor) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	out := make(chan protocol.StreamEvent)

	go func() {
		defer close(out)

		text := strings.TrimSpace(firstText(ec.Message.Parts))
		if text == "" {
			sendEvent(ctx, out, protocol.NewAgentText("input message must contain text"))
			return
		}

		// The first task event materializes the task. A SendMessage request with
		// returnImmediately=true returns the snapshot created from this event.
		if !sendEvent(ctx, out, &protocol.TaskStatusUpdateEvent{
			Status: protocol.TaskStatus{
				State:   protocol.TaskStateWorking,
				Message: protocol.NewAgentText("streaming output"),
			},
		}) {
			return
		}

		artifactID := protocol.GenerateArtifactID()
		chunks := wordChunks(text)
		for i, chunk := range chunks {
			// Leave enough time after the initial snapshot for the client to call
			// SubscribeToTask. Waiting on ctx also makes CancelTask stop the work.
			if !wait(ctx, chunkInterval) {
				log.Infof("Task %s canceled after %d chunks", ec.TaskID, i)
				return
			}

			appendChunk := i > 0
			lastChunk := i == len(chunks)-1
			if !sendEvent(ctx, out, &protocol.TaskArtifactUpdateEvent{
				Artifact: protocol.Artifact{
					ArtifactID: artifactID,
					Name:       stringPtr("streamed text"),
					Parts:      []*protocol.Part{protocol.NewTextPart(chunk)},
				},
				Append:    &appendChunk,
				LastChunk: &lastChunk,
			}) {
				log.Infof("Task %s canceled while sending chunk %d", ec.TaskID, i+1)
				return
			}
		}

		sendEvent(ctx, out, &protocol.TaskStatusUpdateEvent{
			Status: protocol.TaskStatus{
				State:   protocol.TaskStateCompleted,
				Message: protocol.NewAgentText("stream complete"),
			},
		})
	}()

	return out, nil
}

// wordChunks keeps the example focused on streaming semantics. The leading
// space on later chunks makes concatenating the streamed parts reproduce the
// original normalized text.
func wordChunks(text string) []string {
	words := strings.Fields(text)
	for i := 1; i < len(words); i++ {
		words[i] = " " + words[i]
	}
	return words
}

func firstText(parts []*protocol.Part) string {
	for _, part := range parts {
		if text := part.TextContent(); text != "" {
			return text
		}
	}
	return ""
}

func sendEvent(ctx context.Context, out chan<- protocol.StreamEvent, event protocol.StreamEvent) bool {
	select {
	case out <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func main() {
	host := flag.String("host", "localhost", "host to listen on")
	port := flag.Int("port", 8089, "port to listen on")
	flag.Parse()

	address := fmt.Sprintf("%s:%d", *host, *port)
	agentURL := "http://" + address + "/"
	streaming := true
	pushNotifications := false
	description := "Streams one text artifact in appendable chunks"

	agentCard := protocol.AgentCard{
		Name:        "Streaming Example Agent",
		Description: "Demonstrates asynchronous task subscription and artifact chunking",
		SupportedInterfaces: []protocol.AgentInterface{{
			URL:             agentURL,
			ProtocolBinding: "JSONRPC",
			ProtocolVersion: protocol.ProtocolVersionV1,
		}},
		Version: "2.0.0",
		Capabilities: protocol.AgentCapabilities{
			Streaming:         &streaming,
			PushNotifications: &pushNotifications,
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills: []protocol.AgentSkill{{
			ID:          "stream-text",
			Name:        "Stream text",
			Description: &description,
			Tags:        []string{"streaming", "artifact"},
			Examples:    []string{"Streaming makes incremental results visible"},
		}},
	}

	taskManager, err := memory.NewTaskManager(&streamingProcessor{})
	if err != nil {
		log.Fatalf("Create task manager: %v", err)
	}
	srv, err := server.NewA2AServer(taskManager, server.WithAgentCard(agentCard))
	if err != nil {
		log.Fatalf("Create server: %v", err)
	}

	go func() {
		log.Infof("Streaming example listening at %s", agentURL)
		if err := srv.Start(address); err != nil {
			log.Fatalf("Start server: %v", err)
		}
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	<-signals

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Stop(ctx); err != nil {
		log.Errorf("Stop server: %v", err)
	}
}

func stringPtr(value string) *string { return &value }
