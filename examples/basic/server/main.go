// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements a basic A2A chat server built on the MessageProcessor
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
// Keep in sync with examples/basic/client.
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
		Name:        "Basic A2A Chat Example Server",
		Description: "An interactive chat example with task lifecycle operations",
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
	taskManager, err := memory.NewTaskManager(&basicMessageProcessor{})
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

// basicMessageProcessor contains ordinary synchronous business logic.
type basicMessageProcessor struct{}

// ProcessMessage processes one message. The default path is synchronous
// (TaskHandle buffers, then Events()). The long-task path emits live from a
// goroutine so returnImmediately / SubscribeToTask can observe progress.
func (e *basicMessageProcessor) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	handle := taskmanager.NewTaskHandle(ctx, ec)

	history := handle.GetMessageHistory()
	log.Infof(
		"Task context: taskID=%s contextID=%s historyCount=%d",
		handle.TaskID(),
		handle.GetContextID(),
		len(history),
	)
	if task := handle.GetTask(); task != nil {
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
		return e.processLongTask(ctx, handle)
	}

	defer handle.Close()

	if text == "" {
		// A pure-message reply: no task comes into existence for this round.
		if err := handle.Reply(protocol.NewAgentText("input message must contain text.")); err != nil {
			return nil, err
		}
		return handle.Events(), nil
	}

	log.Infof("Processing message with input: %s", text)

	if err := handle.UpdateTaskState(protocol.TaskStateSubmitted, nil); err != nil {
		return nil, err
	}
	if err := handle.UpdateTaskState(protocol.TaskStateWorking, nil); err != nil {
		return nil, err
	}

	// Stream a full sentence as word chunks (no delay) so message/stream and
	// GetTask both show the same aggregated reply.
	result := reverseString(text)
	sentence := fmt.Sprintf("You said %q; reversed it is %q.", text, result)
	if err := emitSentenceChunks(handle, "Reversed Text", "Input text reversed as a sentence", sentence); err != nil {
		return nil, err
	}

	// A second artifact in the same turn (also no delay).
	externalSentence := "You can return another artifact if you want to."
	if err := emitSentenceChunks(handle, "External Artifact", "A second artifact in the same turn", externalSentence); err != nil {
		return nil, err
	}

	// Keep status.message short; the full reply is in the artifact.
	if err := handle.UpdateTaskState(
		protocol.TaskStateCompleted,
		protocol.NewAgentText("done"),
	); err != nil {
		return nil, err
	}
	return handle.Events(), nil
}

// emitSentenceChunks writes a sentence as one artifact, one word per chunk,
// with no sleep — useful for demos of streaming vs aggregated GetTask.
func emitSentenceChunks(handle *taskmanager.TaskHandle, name, description, sentence string) error {
	words := strings.Fields(sentence)
	if len(words) == 0 {
		return nil
	}
	artifact := protocol.NewArtifactWithID(
		stringPtr(name),
		stringPtr(description),
		[]*protocol.Part{protocol.NewTextPart(words[0])},
	)
	if err := handle.AddArtifact(*artifact, len(words) == 1); err != nil {
		return err
	}
	for i := 1; i < len(words); i++ {
		lastChunk := i == len(words)-1
		if err := handle.AppendArtifact(protocol.Artifact{
			ArtifactID: artifact.ArtifactID,
			Parts:      []*protocol.Part{protocol.NewTextPart(" " + words[i])},
		}, lastChunk); err != nil {
			return err
		}
	}
	return nil
}

// processLongTask simulates a long-running agent turn: one word of
// longTaskSentence per second. Events are produced from a goroutine after
// Events() is returned, so clients can exercise returnImmediately,
// SubscribeToTask, and CancelTasks while the round is still in WORKING.
func (e *basicMessageProcessor) processLongTask(
	ctx context.Context,
	handle *taskmanager.TaskHandle,
) (<-chan protocol.StreamEvent, error) {
	words := strings.Fields(longTaskSentence)
	go func() {
		defer handle.Close()

		log.Infof("Starting long task: %d words, 1s interval", len(words))
		if err := handle.UpdateTaskState(protocol.TaskStateSubmitted, nil); err != nil {
			return
		}
		if err := handle.UpdateTaskState(protocol.TaskStateWorking, nil); err != nil {
			return
		}

		artifact := protocol.NewArtifactWithID(
			stringPtr("Long Task Progress"),
			stringPtr("One word per second of a full sentence"),
			[]*protocol.Part{protocol.NewTextPart(words[0])},
		)
		if err := handle.AddArtifact(*artifact, len(words) == 1); err != nil {
			return
		}

		for i := 1; i < len(words); i++ {
			select {
			case <-ctx.Done():
				log.Infof("Long task canceled after %d/%d words", i, len(words))
				_ = handle.UpdateTaskState(
					protocol.TaskStateCanceled,
					protocol.NewAgentText(fmt.Sprintf("canceled after %d/%d words", i, len(words))),
				)
				return
			case <-time.After(time.Second):
			}

			lastChunk := i == len(words)-1
			if err := handle.AppendArtifact(protocol.Artifact{
				ArtifactID: artifact.ArtifactID,
				Parts:      []*protocol.Part{protocol.NewTextPart(" " + words[i])},
			}, lastChunk); err != nil {
				return
			}
		}

		// Status message is a short completion note; the full sentence lives in
		// the artifact (GetTask already aggregates chunks there).
		_ = handle.UpdateTaskState(
			protocol.TaskStateCompleted,
			protocol.NewAgentText(fmt.Sprintf("long task finished (%d words)", len(words))),
		)
	}()
	return handle.Events(), nil
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
