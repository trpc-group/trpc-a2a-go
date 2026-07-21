// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements a basic A2A agent example.
//
// It is the minimal-edit port of the former v0.x TaskHandler-style processor:
// the body stays synchronous and keeps the old verbs via taskmanager.TaskHandle.
// One code path serves message/send and message/stream alike — the framework
// derives the unary result or the live stream from the emitted events.
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

// Command modes for the text processor.
const (
	modeReverse      = "reverse"
	modeUppercase    = "uppercase"
	modeLowercase    = "lowercase"
	modeCount        = "count"
	modeHelp         = "help"
	modeMultiStep    = "multi"
	modeInputExample = "example"

	// Multi-turn step markers stored on status Message.Metadata. The framework
	// moves the previous status message into conversation history on
	// continuation, so the next ProcessMessage round recovers state from
	// ExecContext.History — no process-local session map.
	metaStepKey   = "step"
	metaModeKey   = "mode"
	stepAwaitMode = "await_mode"
	stepAwaitText = "await_text"
)

// basicMessageProcessor implements taskmanager.MessageProcessor with no
// process-local session state: multi-turn progress lives on the task via
// input-required status messages and their Metadata.
type basicMessageProcessor struct{}

// ProcessMessage implements the taskmanager.MessageProcessor interface. The
// body is fully synchronous: emits made before Events() never block, so the
// old v0.x writing style works as-is.
func (p *basicMessageProcessor) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	log.Infof("Processing basic message with ID: %s", ec.Message.MessageID)

	handle := taskmanager.NewTaskHandle(ctx, ec)
	defer handle.Close()

	text := extractText(ec.Message)
	if text == "" {
		handle.Reply(protocol.NewAgentText("input message must contain text"))
		return handle.Events(), nil
	}

	// Continuation: client resent with the same taskId after input-required.
	if ec.Task != nil {
		p.continueMultiTurn(handle, text, ec.History)
		return handle.Events(), nil
	}

	parts := strings.SplitN(text, " ", 2)
	command := strings.ToLower(parts[0])
	var content string
	if len(parts) > 1 {
		content = parts[1]
	}

	switch command {
	case modeMultiStep:
		handle.UpdateTaskState(protocol.TaskStateInputRequired, agentAsk(
			"This is a multi-step interaction. Please select a processing mode:\n"+
				"- reverse: Reverses the text\n"+
				"- uppercase: Converts text to uppercase\n"+
				"- lowercase: Converts text to lowercase\n"+
				"- count: Counts words and characters",
			map[string]any{metaStepKey: stepAwaitMode},
		))
	case modeInputExample:
		handle.UpdateTaskState(protocol.TaskStateInputRequired, agentAsk(
			"Please provide more information to continue:",
			map[string]any{metaStepKey: stepAwaitText, metaModeKey: modeReverse},
		))
	case modeHelp:
		handle.Reply(protocol.NewAgentText(p.processTextWithMode(content, command)))
	default:
		if !isProcessMode(command) {
			// Unknown commands stay message-only: do not materialize a task.
			handle.Reply(protocol.NewAgentText(p.processTextWithMode(content, command)))
			break
		}
		p.processCommand(handle, command, content)
	}
	return handle.Events(), nil
}

func (p *basicMessageProcessor) continueMultiTurn(
	handle *taskmanager.TaskHandle,
	text string,
	history []protocol.Message,
) {
	step, mode := pendingStep(history)
	switch step {
	case stepAwaitMode:
		mode = strings.ToLower(strings.TrimSpace(text))
		if !isProcessMode(mode) {
			handle.UpdateTaskState(protocol.TaskStateInputRequired, agentAsk(
				fmt.Sprintf("Unknown mode %q. Please choose reverse, uppercase, lowercase, or count:", mode),
				map[string]any{metaStepKey: stepAwaitMode},
			))
			return
		}
		handle.UpdateTaskState(protocol.TaskStateInputRequired, agentAsk(
			"Please enter the text you want to process:",
			map[string]any{metaStepKey: stepAwaitText, metaModeKey: mode},
		))
	case stepAwaitText:
		if mode == "" {
			mode = modeReverse
		}
		p.completeWithMode(handle, mode, text)
	default:
		handle.UpdateTaskState(protocol.TaskStateFailed, protocol.NewAgentText(
			"this task is not waiting for multi-turn input"))
	}
}

func (p *basicMessageProcessor) processCommand(
	handle *taskmanager.TaskHandle,
	command string,
	content string,
) {
	handle.UpdateTaskState(protocol.TaskStateWorking, nil)
	p.completeWithMode(handle, command, content)
}

func (p *basicMessageProcessor) completeWithMode(
	handle *taskmanager.TaskHandle,
	mode string,
	content string,
) {
	result := p.processTextWithMode(content, mode)
	handle.AddArtifact(protocol.Artifact{
		ArtifactID:  "processed-text-" + handle.TaskID(),
		Name:        stringPtr("Processed Text"),
		Description: stringPtr(fmt.Sprintf("Text processed with mode: %s", mode)),
		Parts:       []*protocol.Part{protocol.NewTextPart(result)},
		Metadata: map[string]any{
			"operation":    mode,
			"originalText": content,
			"processedAt":  time.Now().UTC().Format(time.RFC3339),
		},
	}, true)
	handle.UpdateTaskState(protocol.TaskStateCompleted, protocol.NewAgentText(result))
}

func (p *basicMessageProcessor) processTextWithMode(text, mode string) string {
	switch mode {
	case modeReverse:
		runes := []rune(text)
		for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
			runes[i], runes[j] = runes[j], runes[i]
		}
		return fmt.Sprintf("Reversed: %s", string(runes))
	case modeUppercase:
		return strings.ToUpper(text)
	case modeLowercase:
		return strings.ToLower(text)
	case modeCount:
		words := len(strings.Fields(text))
		chars := len(text)
		return fmt.Sprintf("Word count: %d, Character count: %d", words, chars)
	case modeHelp:
		return `Available commands:
- reverse <text>: Reverse the given text
- uppercase <text>: Convert text to uppercase
- lowercase <text>: Convert text to lowercase
- count <text>: Count words and characters
- multi: Start a multi-turn interaction
- example: Example of input-required state
- help: Show this help message

Example: reverse hello world`
	default:
		return fmt.Sprintf("Unknown mode '%s'. Use 'help' for available commands.", mode)
	}
}

func agentAsk(text string, meta map[string]any) *protocol.Message {
	msg := protocol.NewAgentText(text)
	msg.Metadata = meta
	return msg
}

// pendingStep recovers multi-turn progress from the latest agent status
// message that was rolled into history when the continuation began.
func pendingStep(history []protocol.Message) (step, mode string) {
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if msg.Role != protocol.MessageRoleAgent || len(msg.Metadata) == 0 {
			continue
		}
		s, _ := msg.Metadata[metaStepKey].(string)
		if s == "" {
			continue
		}
		m, _ := msg.Metadata[metaModeKey].(string)
		return s, m
	}
	return "", ""
}

func isProcessMode(mode string) bool {
	switch mode {
	case modeReverse, modeUppercase, modeLowercase, modeCount:
		return true
	default:
		return false
	}
}

func extractText(message protocol.Message) string {
	var texts []string
	for _, part := range message.Parts {
		if text := part.TextContent(); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, " ")
}

func main() {
	var (
		host          string
		port          int
		description   string
		noCORS        bool
		forceNoStream bool
	)

	flag.StringVar(&host, "host", "localhost", "Server host address")
	flag.IntVar(&port, "port", 8080, "Server port")
	flag.StringVar(&description, "desc", "A versatile A2A example agent that processes text", "Agent description")
	flag.BoolVar(&noCORS, "no-cors", false, "Disable CORS headers")
	flag.BoolVar(&forceNoStream, "no-stream", false, "Disable streaming capability")
	flag.Parse()

	address := fmt.Sprintf("%s:%d", host, port)
	serverURL := fmt.Sprintf("http://%s/", address)

	if !forceNoStream {
		description += " with streaming support"
	}

	// Push is intentionally off here: use examples/notify or examples/jwks for
	// the framework's WithPushNotifications path.
	agentCard := server.AgentCard{
		Name:        "Text Processing Agent",
		Description: description,
		URL:         serverURL,
		Version:     "2.0.0",
		Provider: &server.AgentProvider{
			Organization: "tRPC-A2A-go Examples",
		},
		Capabilities: server.AgentCapabilities{
			Streaming:              boolPtr(!forceNoStream),
			PushNotifications:      boolPtr(false),
			StateTransitionHistory: boolPtr(true),
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills: []server.AgentSkill{
			{
				ID:          "text_processor_reverse",
				Name:        "Text Reverser",
				Description: stringPtr("Input: reverse hello\nOutput: Reversed: olleh"),
				Tags:        []string{"text", "reverse"},
				Examples:    []string{"reverse hello world", "reverse The quick brown fox"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
			{
				ID:          "text_processor_uppercase",
				Name:        "Uppercase Converter",
				Description: stringPtr("Input: uppercase hello world\nOutput: HELLO WORLD"),
				Tags:        []string{"text", "uppercase"},
				Examples:    []string{"uppercase hello world", "uppercase Example text"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
			{
				ID:          "text_processor_lowercase",
				Name:        "Lowercase Converter",
				Description: stringPtr("Input: lowercase HELLO\nOutput: hello"),
				Tags:        []string{"text", "lowercase"},
				Examples:    []string{"lowercase HELLO WORLD", "lowercase TEXT"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
			{
				ID:          "text_processor_count",
				Name:        "Word Counter",
				Description: stringPtr("Input: count hello world\nOutput: Word count: 2, Character count: 11"),
				Tags:        []string{"text", "count"},
				Examples:    []string{"count The quick brown fox", "count hello world"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
			{
				ID:          "text_processor_multistep",
				Name:        "Multi-Step Processor",
				Description: stringPtr("Input: multi\nStarts an interactive multi-turn conversation"),
				Tags:        []string{"interactive", "multi-turn"},
				Examples:    []string{"multi"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
			{
				ID:          "text_processor_example",
				Name:        "Input-Required Example",
				Description: stringPtr("Input: example\nAsks for more text, then reverses it"),
				Tags:        []string{"interactive", "input-required"},
				Examples:    []string{"example"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
			{
				ID:          "text_processor_help",
				Name:        "Help Guide",
				Description: stringPtr("Input: help\nOutput: List of available commands and usage"),
				Tags:        []string{"help"},
				Examples:    []string{"help"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
		},
	}

	taskManager, err := memory.NewTaskManager(&basicMessageProcessor{})
	if err != nil {
		log.Fatalf("Failed to create task manager: %v", err)
	}

	srv, err := server.NewA2AServer(taskManager,
		server.WithAgentCard(agentCard),
		server.WithCORSEnabled(!noCORS),
	)
	if err != nil {
		log.Fatalf("Failed to create A2A server: %v", err)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Infof("Text Processing Agent server starting on %s (CORS enabled: %t, Streaming: %t)",
			address, !noCORS, !forceNoStream)
		if err := srv.Start(address); err != nil {
			log.Fatalf("Server failed to start: %v", err)
		}
	}()

	<-sigChan
	log.Info("Shutdown signal received, initiating graceful shutdown...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Stop(ctx); err != nil {
		log.Errorf("Server shutdown failed: %v", err)
	} else {
		log.Info("Server exited gracefully.")
	}
}

func stringPtr(s string) *string {
	return &s
}

func boolPtr(b bool) *bool {
	return &b
}
