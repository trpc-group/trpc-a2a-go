// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements a simple A2A server example built on the MessageProcessor
// contract: one code path serves both message/send and message/stream, and the
// framework owns the task lifecycle (creation, persistence, fan-out).
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

// longTaskMarker is the well-known text that triggers processLongTask.
// Keep in sync with examples/simple/client.
// Used only to simulate a long-running task for async / subscribe / cancel demos.
const longTaskMarker = "__long_task__"

// longTaskSentence is streamed one word per second; GetTask / subscribe snapshots
// show the words aggregated into this full sentence.
const longTaskSentence = "Hello from the long task demo: we stream one word per second so SubscribeToTask can show live updates and GetTask can return the aggregated sentence until CancelTasks stops us early."

func main() {
	// Parse command-line flags.
	host := flag.String("host", "localhost", "Host to listen on")
	port := flag.Int("port", 8080, "Port to listen on")
	flag.Parse()

	// Create the agent card.
	agentCard := server.AgentCard{
		Name:        "Simple A2A Example Server",
		Description: "A simple example A2A server that reverses text",
		URL:         fmt.Sprintf("http://%s:%d/", *host, *port),
		Version:     "1.0.0",
		Provider: &server.AgentProvider{
			Organization: "tRPC-A2A-Go Examples",
			URL:          stringPtr(fmt.Sprintf("http://%s:%d/", *host, *port)),
		},
		Capabilities: server.AgentCapabilities{
			Streaming:              boolPtr(true),
			PushNotifications:      boolPtr(false),
			StateTransitionHistory: boolPtr(true),
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills: []server.AgentSkill{
			{
				ID:          "text_reversal",
				Name:        "Text Reversal",
				Description: stringPtr("Reverses the input text"),
				Tags:        []string{"text", "processing"},
				Examples:    []string{"Hello, world!"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
		},
	}

	// Create the processor and inject it into a task manager.
	// (redis.NewTaskManager accepts the same MessageProcessor for persistent storage.)
	taskManager, err := memory.NewTaskManager(&simpleMessageProcessor{})
	if err != nil {
		log.Fatalf("Failed to create task manager: %v", err)
	}

	// Create the server.
	srv, err := server.NewA2AServer(taskManager, server.WithAgentCard(agentCard))
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	// Set up a channel to listen for termination signals.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start the server in a goroutine.
	go func() {
		serverAddr := fmt.Sprintf("%s:%d", *host, *port)
		log.Infof("Starting server on %s...", serverAddr)
		if err := srv.Start(serverAddr); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// Wait for termination signal.
	sig := <-sigChan
	log.Infof("Received signal %v, shutting down...", sig)
}

// simpleMessageProcessor contains ordinary synchronous business logic.
type simpleMessageProcessor struct{}

// ProcessMessage processes one message. The default path fills a buffered raw
// event channel synchronously. The long-task path emits live from a goroutine
// so returnImmediately / SubscribeToTask can observe progress.
func (e *simpleMessageProcessor) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	history := ec.History
	log.Infof(
		"Task context: taskID=%s contextID=%s historyCount=%d",
		ec.TaskID,
		ec.ContextID,
		len(history),
	)
	if task := ec.Task; task != nil {
		log.Infof("Continuing task: taskID=%s state=%s", task.ID, task.Status.State)
	}
	for i, message := range history {
		log.Infof(
			"History[%d]: messageID=%s role=%s text=%q",
			i,
			message.MessageID,
			message.Role,
			extractText(message),
		)
	}

	text := extractText(ec.Message)
	if text == longTaskMarker {
		return e.processLongTask(ctx)
	}

	if text == "" {
		// A pure-message reply: no task comes into existence for this round.
		events := make(chan protocol.StreamEvent, 1)
		events <- protocol.NewAgentText("input message must contain text.")
		close(events)
		return events, nil
	}

	log.Infof("Processing message with input: %s", text)

	// Stream a full sentence as word chunks (no delay) so message/stream and
	// GetTask both show the same aggregated reply.
	result := reverseString(text)
	sentence := fmt.Sprintf("You said %q; reversed it is %q.", text, result)
	// A second artifact in the same turn (also no delay).
	externalSentence := "You can return another artifact if you want to."
	events := make(chan protocol.StreamEvent,
		3+len(strings.Fields(sentence))+len(strings.Fields(externalSentence)))
	events <- &protocol.TaskStatusUpdateEvent{
		Status: protocol.TaskStatus{State: protocol.TaskStateSubmitted},
	}
	events <- &protocol.TaskStatusUpdateEvent{
		Status: protocol.TaskStatus{State: protocol.TaskStateWorking},
	}
	emitSentenceChunks(events, "Reversed Text", "Input text reversed as a sentence", sentence)
	emitSentenceChunks(events, "External Artifact", "A second artifact in the same turn", externalSentence)

	// Keep status.message short; the full reply is in the artifact.
	events <- &protocol.TaskStatusUpdateEvent{
		Status: protocol.TaskStatus{
			State:   protocol.TaskStateCompleted,
			Message: protocol.NewAgentText("done"),
		},
	}
	close(events)
	return events, nil
}

// emitSentenceChunks writes a sentence as one artifact, one word per chunk,
// with no sleep — useful for demos of streaming vs aggregated GetTask.
func emitSentenceChunks(events chan<- protocol.StreamEvent, name, description, sentence string) {
	words := strings.Fields(sentence)
	if len(words) == 0 {
		return
	}
	artifact := protocol.NewArtifactWithID(
		stringPtr(name),
		stringPtr(description),
		[]*protocol.Part{protocol.NewTextPart(words[0])},
	)
	events <- &protocol.TaskArtifactUpdateEvent{
		Artifact:  *artifact,
		LastChunk: boolPtr(len(words) == 1),
	}
	for i := 1; i < len(words); i++ {
		events <- &protocol.TaskArtifactUpdateEvent{
			Artifact: protocol.Artifact{
				ArtifactID: artifact.ArtifactID,
				Parts:      []*protocol.Part{protocol.NewTextPart(" " + words[i])},
			},
			Append:    boolPtr(true),
			LastChunk: boolPtr(i == len(words)-1),
		}
	}
}

// processLongTask simulates a long-running agent turn: one word of
// longTaskSentence per second. Events are produced from a goroutine after
// Events() is returned, so clients can exercise returnImmediately,
// SubscribeToTask, and CancelTasks while the round is still in WORKING.
func (e *simpleMessageProcessor) processLongTask(
	ctx context.Context,
) (<-chan protocol.StreamEvent, error) {
	words := strings.Fields(longTaskSentence)
	events := make(chan protocol.StreamEvent)
	go func() {
		defer close(events)

		log.Infof("Starting long task: %d words, 1s interval", len(words))
		events <- &protocol.TaskStatusUpdateEvent{
			Status: protocol.TaskStatus{State: protocol.TaskStateSubmitted},
		}
		events <- &protocol.TaskStatusUpdateEvent{
			Status: protocol.TaskStatus{State: protocol.TaskStateWorking},
		}

		artifact := protocol.NewArtifactWithID(
			stringPtr("Long Task Progress"),
			stringPtr("One word per second of a full sentence"),
			[]*protocol.Part{protocol.NewTextPart(words[0])},
		)
		events <- &protocol.TaskArtifactUpdateEvent{
			Artifact:  *artifact,
			LastChunk: boolPtr(len(words) == 1),
		}

		for i := 1; i < len(words); i++ {
			select {
			case <-ctx.Done():
				log.Infof("Long task canceled after %d/%d words", i, len(words))
				events <- &protocol.TaskStatusUpdateEvent{
					Status: protocol.TaskStatus{
						State: protocol.TaskStateCanceled,
						Message: protocol.NewAgentText(
							fmt.Sprintf("canceled after %d/%d words", i, len(words)),
						),
					},
				}
				return
			case <-time.After(time.Second):
			}

			events <- &protocol.TaskArtifactUpdateEvent{
				Artifact: protocol.Artifact{
					ArtifactID: artifact.ArtifactID,
					Parts:      []*protocol.Part{protocol.NewTextPart(" " + words[i])},
				},
				Append:    boolPtr(true),
				LastChunk: boolPtr(i == len(words)-1),
			}
		}

		// Status message is a short completion note; the full sentence lives in
		// the artifact (GetTask already aggregates chunks there).
		events <- &protocol.TaskStatusUpdateEvent{
			Status: protocol.TaskStatus{
				State: protocol.TaskStateCompleted,
				Message: protocol.NewAgentText(
					fmt.Sprintf("long task finished (%d words)", len(words)),
				),
			},
		}
	}()
	return events, nil
}

// extractText extracts the text content from a message.
func extractText(message protocol.Message) string {
	for _, part := range message.Parts {
		if t := part.TextContent(); t != "" {
			return t
		}
	}
	return ""
}

// reverseString reverses a string.
func reverseString(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

// Helper function to create string pointers.
func stringPtr(s string) *string {
	return &s
}

// Helper function to create bool pointers.
func boolPtr(b bool) *bool {
	return &b
}
