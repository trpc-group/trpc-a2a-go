// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements a CLI host for the A2A agent.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/client"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
)

// Config holds the application configuration.
type Config struct {
	AgentURL         string
	Timeout          time.Duration
	ForceNoStreaming bool
	ContextID        string
	HistoryLength    int
}

// Command types for CLI.
const (
	cmdExit    = "exit"
	cmdHelp    = "help"
	cmdContext = "context"
	cmdMode    = "mode"
	cmdCancel  = "cancel"
	cmdGet     = "get"
	cmdCard    = "card"
	cmdNew     = "new"
)

// sendResult is the task identity/state observed from a SendMessage or stream
// response — enough for input-required continuation without a follow-up GetTasks.
type sendResult struct {
	taskID string
	state  protocol.TaskState
}

func main() {
	// Parse command-line flags.
	config := parseFlags()

	// Create A2A client.
	a2aClient, err := createClient(config)
	if err != nil {
		log.Fatalf("Failed to create A2A client: %v", err)
	}

	// Fetch and display agent capabilities
	agentCard, err := fetchAgentCard(config.AgentURL)
	if err != nil {
		log.Printf("WARNING: Failed to fetch agent card: %v", err)
	} else {
		displayAgentCapabilities(agentCard)
	}

	// Display welcome message.
	displayWelcomeMessage(config)

	// Start interactive session.
	runInteractiveSession(a2aClient, config)

	fmt.Println("Exiting CLI host.")
}

// parseFlags parses command-line flags and returns a Config.
func parseFlags() Config {
	var config Config
	flag.StringVar(&config.AgentURL, "agent", "http://localhost:8080/", "Target A2A agent URL")
	flag.DurationVar(&config.Timeout, "timeout", 60*time.Second, "Request timeout (e.g., 30s, 1m)")
	flag.BoolVar(&config.ForceNoStreaming, "no-stream", false, "Disable streaming mode")
	flag.StringVar(&config.ContextID, "context", "", "Use specific context ID (empty = generate new)")
	flag.IntVar(&config.HistoryLength, "history", 0, "Number of history messages to request (0 = none)")
	flag.Parse()

	// Generate a context ID if not provided
	if config.ContextID == "" {
		config.ContextID = protocol.GenerateContextID()
	}

	return config
}

// createClient creates a new A2A client with the given configuration.
func createClient(config Config) (*client.A2AClient, error) {
	return client.NewA2AClient(config.AgentURL, client.WithTimeout(config.Timeout))
}

// fetchAgentCard retrieves the agent card from the .well-known endpoint.
func fetchAgentCard(baseURL string) (*server.AgentCard, error) {
	// Ensure base URL ends with "/"
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}

	// Construct agent card URL
	cardURL := baseURL + ".well-known/agent-card.json"

	// Make the request
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cardURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch agent card: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Decode the response
	var card server.AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		return nil, fmt.Errorf("failed to decode agent card: %w", err)
	}

	return &card, nil
}

// displayAgentCapabilities displays the capabilities from the agent card.
func displayAgentCapabilities(card *server.AgentCard) {
	fmt.Println("Agent Capabilities:")
	fmt.Printf("  Name: %s\n", card.Name)
	if card.Description != "" {
		fmt.Printf("  Description: %s\n", card.Description)
	}
	fmt.Printf("  Version: %s\n", card.Version)

	// Print provider if available
	if card.Provider != nil {
		fmt.Printf("  Provider: %s\n", card.Provider.Organization)
	}

	// Print capabilities - handle new *bool types
	streaming := false
	if card.Capabilities.Streaming != nil {
		streaming = *card.Capabilities.Streaming
	}
	pushNotifications := false
	if card.Capabilities.PushNotifications != nil {
		pushNotifications = *card.Capabilities.PushNotifications
	}
	stateHistory := false
	if card.Capabilities.StateTransitionHistory != nil {
		stateHistory = *card.Capabilities.StateTransitionHistory
	}

	fmt.Printf("  Streaming: %t\n", streaming)
	fmt.Printf("  Push Notifications: %t\n", pushNotifications)
	fmt.Printf("  State Transition History: %t\n", stateHistory)

	// Print input/output modes
	fmt.Printf("  Input Modes: %s\n", strings.Join(card.DefaultInputModes, ", "))
	fmt.Printf("  Output Modes: %s\n", strings.Join(card.DefaultOutputModes, ", "))

	// Print skills if available
	if len(card.Skills) > 0 {
		fmt.Println("  Skills:")
		for _, skill := range card.Skills {
			fmt.Printf("    - %s: ", skill.Name)
			if skill.Description != nil {
				fmt.Printf("%s\n", *skill.Description)
			} else {
				fmt.Println("(no description)")
			}

			if len(skill.Examples) > 0 {
				fmt.Printf("      Examples: %s\n", strings.Join(skill.Examples, ", "))
			}
		}
	}

	fmt.Println(strings.Repeat("-", 60))
}

// displayWelcomeMessage prints the welcome message with connection details.
func displayWelcomeMessage(config Config) {
	log.Printf("Connecting to agent: %s (Timeout: %v)", config.AgentURL, config.Timeout)
	fmt.Printf("Context ID: %s\n", config.ContextID)
	fmt.Printf("Streaming mode: %v\n", !config.ForceNoStreaming)
	fmt.Println("Enter text to send to the agent. Type 'help' for commands or 'exit' to quit.")
	fmt.Println(strings.Repeat("-", 60))
}

// runInteractiveSession runs the main interactive session loop.
func runInteractiveSession(a2aClient *client.A2AClient, config Config) {
	reader := bufio.NewReader(os.Stdin)
	contextID := config.ContextID
	var lastTaskID string
	var lastTaskState protocol.TaskState
	var useStreaming = !config.ForceNoStreaming

	// Check if input is from a pipe/redirect or interactive terminal
	stat, err := os.Stdin.Stat()
	isInteractive := err == nil && (stat.Mode()&os.ModeCharDevice) != 0

	if !isInteractive {
		// Non-interactive mode: process all piped input at once
		log.Println("Running in non-interactive mode (piped input)")
		scanner := bufio.NewScanner(os.Stdin)
		inputs := []string{}

		// Read all inputs first
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				inputs = append(inputs, line)
			}
		}

		// Process each input
		for i, input := range inputs {
			log.Printf("Processing input %d/%d: %s", i+1, len(inputs), input)

			// Process built-in commands
			if handled, resetContinuation := processCommand(
				a2aClient,
				input,
				&config,
				&contextID,
				&useStreaming,
				lastTaskID,
			); handled {
				if resetContinuation {
					lastTaskState = ""
				}
				continue
			}

			result := processUserInput(a2aClient, input, contextID, config, useStreaming, continueTaskID(lastTaskID, lastTaskState))
			if result.taskID != "" {
				lastTaskID, lastTaskState = result.taskID, result.state
			} else {
				lastTaskState = ""
			}
		}
		return
	}

	// Interactive mode: continuous loop
	log.Println("Running in interactive mode")
	for {
		// Display prompt
		fmt.Print("> ")

		input, readErr := reader.ReadString('\n')

		if readErr != nil {
			if readErr == io.EOF {
				fmt.Println("\nExiting.")
				break
			}
			log.Printf("ERROR: Failed to read input: %v", readErr)
			continue
		}

		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}

		// Process built-in commands
		if handled, resetContinuation := processCommand(
			a2aClient,
			input,
			&config,
			&contextID,
			&useStreaming,
			lastTaskID,
		); handled {
			if resetContinuation {
				lastTaskState = ""
			}
			continue
		}

		result := processUserInput(a2aClient, input, contextID, config, useStreaming, continueTaskID(lastTaskID, lastTaskState))
		if result.taskID != "" {
			lastTaskID, lastTaskState = result.taskID, result.state
		} else {
			lastTaskState = ""
		}
		if lastTaskState == protocol.TaskStateInputRequired {
			fmt.Println(strings.Repeat("-", 60))
			fmt.Println("[Additional input required to complete this task. Continue typing.]")
		}
	}
}

// continueTaskID returns the task ID to attach on a follow-up message when the
// previous round suspended in input-required; otherwise the next turn is fresh.
func continueTaskID(lastTaskID string, lastTaskState protocol.TaskState) string {
	if lastTaskState == protocol.TaskStateInputRequired && lastTaskID != "" {
		return lastTaskID
	}
	return ""
}

// processCommand handles built-in client commands. resetContinuation reports
// whether the command intentionally leaves the current input-required task.
func processCommand(
	a2aClient *client.A2AClient,
	input string,
	config *Config,
	contextID *string,
	useStreaming *bool,
	lastTaskID string,
) (handled, resetContinuation bool) {
	parts := strings.Fields(input)
	cmd := strings.ToLower(parts[0])

	switch cmd {
	case cmdExit:
		fmt.Println("Exiting.")
		os.Exit(0)
		return true, true

	case cmdHelp:
		displayHelpMessage()
		return true, false

	case cmdContext:
		if len(parts) > 1 {
			*contextID = parts[1]
			fmt.Printf("Context ID set to: %s\n", *contextID)
		} else {
			*contextID = protocol.GenerateContextID()
			fmt.Printf("Generated new context ID: %s\n", *contextID)
		}
		return true, true

	case cmdMode:
		if len(parts) > 1 {
			modeStr := strings.ToLower(parts[1])
			if modeStr == "stream" || modeStr == "streaming" {
				*useStreaming = true
				fmt.Println("Switched to streaming mode.")
			} else if modeStr == "sync" || modeStr == "standard" {
				*useStreaming = false
				fmt.Println("Switched to standard (non-streaming) mode.")
			} else {
				fmt.Printf("Unknown mode: %s. Use 'stream' or 'sync'.\n", modeStr)
			}
		} else {
			fmt.Printf("Current mode: %s\n", getModeName(*useStreaming))
			fmt.Println("Usage: mode [stream|sync]")
		}
		return true, false

	case cmdCancel:
		taskID := lastTaskID
		if len(parts) > 1 {
			taskID = parts[1]
		}
		if taskID == "" {
			fmt.Println("No task ID provided or available from last request.")
			return true, false
		}
		canceled := cancelTask(a2aClient, taskID, config.Timeout)
		return true, canceled && taskID == lastTaskID

	case cmdGet:
		taskID := lastTaskID
		if len(parts) > 1 {
			taskID = parts[1]
		}
		if taskID == "" {
			fmt.Println("No task ID provided or available from last request.")
			return true, false
		}
		historyLength := config.HistoryLength
		if len(parts) > 2 {
			if _, err := fmt.Sscanf(parts[2], "%d", &historyLength); err != nil {
				fmt.Printf("Invalid history length: %s. Using default: %d\n", parts[2], config.HistoryLength)
				historyLength = config.HistoryLength
			}
		}
		getTask(a2aClient, taskID, historyLength, config.Timeout)
		return true, false

	case cmdCard:
		agentCard, err := fetchAgentCard(config.AgentURL)
		if err != nil {
			fmt.Printf("Failed to fetch agent card: %v\n", err)
			return true, false
		}
		displayAgentCapabilities(agentCard)
		return true, false

	case cmdNew:
		*contextID = protocol.GenerateContextID()
		fmt.Printf("Starting a new context: %s\n", *contextID)
		return true, true
	}

	return false, false
}

// getModeName returns a user-friendly name for the current mode.
func getModeName(streaming bool) string {
	if streaming {
		return "streaming (real-time updates)"
	}
	return "standard (non-streaming)"
}

// displayHelpMessage shows available commands and their usage.
func displayHelpMessage() {
	fmt.Println("Available commands:")
	fmt.Println("  help                     - Show this help message")
	fmt.Println("  exit                     - Exit the program")
	fmt.Println("  context [id]             - Set or generate a new context ID")
	fmt.Println("  mode [stream|sync]       - Set interaction mode (streaming or standard)")
	fmt.Println("  cancel [task-id]         - Cancel a task (uses last task ID if not specified)")
	fmt.Println("  get [task-id] [history]  - Get task details (uses last task ID if not specified)")
	fmt.Println("  card                     - Fetch and display the agent's capabilities card")
	fmt.Println("  new                      - Start a new context")
	fmt.Println("")
	fmt.Println("For normal interaction, just type your message and press Enter.")
	fmt.Println("After input-required, the next message continues the same taskId.")
	fmt.Println("For push notifications, see examples/notify or examples/jwks.")
	fmt.Println(strings.Repeat("-", 60))
}

// processUserInput handles a single user input, sends it to the agent, and processes the response.
// When continueTaskID is non-empty, the message continues a suspended (input-required) task.
func processUserInput(
	a2aClient *client.A2AClient,
	input,
	contextID string,
	config Config,
	useStreaming bool,
	continueTaskID string,
) sendResult {
	var taskIDPtr *string
	if continueTaskID != "" {
		taskIDPtr = &continueTaskID
	}
	message := protocol.NewMessageWithContext(
		protocol.MessageRoleUser,
		[]*protocol.Part{protocol.NewTextPart(input)},
		taskIDPtr,
		&contextID,
	)

	params := createMessageParams(message, config.HistoryLength)

	if useStreaming && !config.ForceNoStreaming {
		return handleStreamingInteraction(a2aClient, params, config)
	}
	return handleStandardInteraction(a2aClient, params, config)
}

// createMessageParams creates the parameters for sending a message.
func createMessageParams(message protocol.Message, historyLength int) protocol.SendMessageParams {
	params := protocol.SendMessageParams{
		Message: message,
	}

	// Add configuration if needed
	if historyLength > 0 {
		params.Configuration = &protocol.SendMessageConfiguration{
			HistoryLength: &historyLength,
		}
	}

	return params
}

// handleStreamingInteraction sends a streaming request to the agent and processes the response.
func handleStreamingInteraction(
	a2aClient *client.A2AClient,
	params protocol.SendMessageParams,
	config Config,
) sendResult {
	ctx, cancel := context.WithTimeout(context.Background(), config.Timeout*2)
	defer cancel()

	log.Printf("Sending stream request for message %s (Context: %s)...", params.Message.MessageID, *params.Message.ContextID)
	eventChan, streamErr := a2aClient.StreamMessage(ctx, params)
	if streamErr != nil {
		log.Printf("ERROR: StreamMessage request failed: %v", streamErr)
		fmt.Println(strings.Repeat("-", 60))
		return sendResult{}
	}

	result := processStreamResponse(ctx, eventChan)
	log.Printf("Stream processing finished for message %s", params.Message.MessageID)
	fmt.Println(strings.Repeat("-", 60))
	return result
}

// handleStandardInteraction sends a standard (non-streaming) request to the agent.
func handleStandardInteraction(
	a2aClient *client.A2AClient,
	params protocol.SendMessageParams,
	config Config,
) sendResult {
	ctx, cancel := context.WithTimeout(context.Background(), config.Timeout)
	defer cancel()

	log.Printf("Sending standard request for message %s (Context: %s)...", params.Message.MessageID, *params.Message.ContextID)
	result, err := a2aClient.SendMessage(ctx, params)
	if err != nil {
		log.Printf("ERROR: SendMessage request failed: %v", err)
		fmt.Println(strings.Repeat("-", 60))
		return sendResult{}
	}

	fmt.Println("\n<< Agent Response:")
	fmt.Println(strings.Repeat("-", 10))

	out := sendResult{}
	if response := result.GetMessage(); response != nil {
		fmt.Println("  Message Response:")
		printMessage(*response)
	} else if response := result.GetTask(); response != nil {
		out.taskID = response.ID
		out.state = response.Status.State
		fmt.Printf("  Task %s State: %s (%s)\n", response.ID, response.Status.State, formatTimestamp(response.Status.Timestamp))

		if response.Status.Message != nil {
			fmt.Println("  Message:")
			printMessage(*response.Status.Message)
		}
		if len(response.Artifacts) > 0 {
			fmt.Println("  Artifacts:")
			for i, artifact := range response.Artifacts {
				name := fmt.Sprintf("Artifact #%d", i+1)
				if artifact.Name != nil {
					name = *artifact.Name
				}
				fmt.Printf("    [%s]\n", name)
				printParts(artifact.Parts)
			}
		}
		if len(response.History) > 0 {
			fmt.Println("  History:")
			for i, msg := range response.History {
				role := "User"
				if msg.Role == protocol.MessageRoleAgent {
					role = "Agent"
				}
				fmt.Printf("    [%d] %s:\n", i+1, role)
				printParts(msg.Parts)
			}
		}
		if response.Status.State == protocol.TaskStateInputRequired {
			fmt.Println("  [Additional input required]")
		}
	} else {
		fmt.Println("  Unknown response type")
	}

	fmt.Println(strings.Repeat("-", 60))
	return out
}

// processStreamResponse processes the stream of events from the agent.
func processStreamResponse(
	ctx context.Context, eventChan <-chan protocol.StreamResponse,
) sendResult {
	fmt.Println("\n<< Agent Response Stream:")
	fmt.Println(strings.Repeat("-", 10))

	out := sendResult{}
	for {
		select {
		case <-ctx.Done():
			log.Printf("ERROR: Context timeout or cancellation while waiting for stream events: %v", ctx.Err())
			return out

		case event, ok := <-eventChan:
			if !ok {
				log.Println("Stream channel closed.")
				if ctx.Err() != nil {
					log.Printf("Context error after stream close: %v", ctx.Err())
				}
				return out
			}

			if e := event.GetMessage(); e != nil {
				fmt.Println("  [Message Response:]")
				printMessage(*e)
			} else if e := event.GetTask(); e != nil {
				out.taskID = e.ID
				out.state = e.Status.State
				fmt.Printf("  [Task %s State: %s (%s)]\n", e.ID, e.Status.State, formatTimestamp(e.Status.Timestamp))
				if e.Status.Message != nil {
					printMessage(*e.Status.Message)
				}
			} else if e := event.GetStatusUpdate(); e != nil {
				out.taskID = e.TaskID
				out.state = e.Status.State
				fmt.Printf("  [Status Update: %s (%s)]\n", e.Status.State, formatTimestamp(e.Status.Timestamp))
				if e.Status.Message != nil {
					printMessage(*e.Status.Message)
				}
				if e.Status.State == protocol.TaskStateInputRequired {
					fmt.Println("  [Additional input required]")
					return out
				}
				if e.Final {
					log.Printf("Final status received: %s", e.Status.State)
					switch e.Status.State {
					case protocol.TaskStateCompleted:
						fmt.Println("  [Task completed successfully]")
					case protocol.TaskStateFailed:
						fmt.Println("  [Task failed]")
					case protocol.TaskStateCanceled:
						fmt.Println("  [Task was canceled]")
					}
					return out
				}
			} else if e := event.GetArtifactUpdate(); e != nil {
				out.taskID = e.TaskID
				name := getArtifactName(e.Artifact)
				fmt.Printf("  [Artifact Update: %s]\n", name)
				printParts(e.Artifact.Parts)
				if e.LastChunk != nil && *e.LastChunk {
					log.Printf("Final artifact received with ID %s", e.Artifact.ArtifactID)
				}
			} else {
				log.Println("Warning: Received unknown stream event")
			}
		}
	}
}

// getArtifactName returns the name of an artifact or a default if name is nil
func getArtifactName(artifact protocol.Artifact) string {
	if artifact.Name != nil {
		return *artifact.Name
	}
	return fmt.Sprintf("Artifact %s", artifact.ArtifactID)
}

// cancelTask attempts to cancel a running task and reports whether the request succeeded.
func cancelTask(a2aClient *client.A2AClient, taskID string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	log.Printf("Attempting to cancel task %s...", taskID)

	task, err := a2aClient.CancelTasks(ctx, protocol.TaskIDParams{ID: taskID})

	if err != nil {
		log.Printf("ERROR: Failed to cancel task %s: %v", taskID, err)
		fmt.Printf("Failed to cancel task: %v\n", err)
		return false
	}

	fmt.Println("Task cancellation result:")
	fmt.Printf("  State: %s (%s)\n", task.Status.State, formatTimestamp(task.Status.Timestamp))

	if task.Status.Message != nil {
		fmt.Println("  Message:")
		printMessage(*task.Status.Message)
	}
	return true
}

// getTask fetches and displays a task's current state.
func getTask(a2aClient *client.A2AClient, taskID string, historyLength int, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	log.Printf("Fetching task %s...", taskID)

	params := protocol.TaskQueryParams{ID: taskID}
	if historyLength > 0 {
		params.HistoryLength = &historyLength
	}

	task, err := a2aClient.GetTasks(ctx, params)

	if err != nil {
		log.Printf("ERROR: Failed to get task %s: %v", taskID, err)
		fmt.Printf("Failed to get task: %v\n", err)
		return
	}

	fmt.Println("Task details:")
	displayFinalTaskState(task)
}

// displayFinalTaskState displays the final state of a task.
func displayFinalTaskState(task *protocol.Task) {
	fmt.Printf("  State: %s (%s)\n", task.Status.State, formatTimestamp(task.Status.Timestamp))

	if task.Status.Message != nil {
		fmt.Println("  Message:")
		printMessage(*task.Status.Message)
	}

	if len(task.Artifacts) > 0 {
		fmt.Println("  Artifacts:")
		for i, artifact := range task.Artifacts {
			name := fmt.Sprintf("Artifact #%d", i+1)
			if artifact.Name != nil {
				name = *artifact.Name
			}
			fmt.Printf("    [%s]\n", name)
			printParts(artifact.Parts)
		}
	}

	if task.History != nil && len(task.History) > 0 {
		fmt.Println("  History:")
		for i, msg := range task.History {
			role := "User"
			if msg.Role == protocol.MessageRoleAgent {
				role = "Agent"
			}
			fmt.Printf("    [%d] %s:\n", i+1, role)
			printParts(msg.Parts)
		}
	}
}

// printMessage prints the parts contained within a message.
func printMessage(message protocol.Message) {
	printParts(message.Parts)
}

// printParts iterates through and prints different message/artifact part types.
func printParts(parts []*protocol.Part) {
	for _, part := range parts {
		printPart(part)
	}
}

// printPart prints a single part with proper indentation.
func printPart(part *protocol.Part) {
	const indent = "    "
	if text := part.TextContent(); text != "" {
		fmt.Println(indent + text)
	} else {
		fmt.Printf("%s[Non-text Part: %T]\n", indent, part.Content)
	}
}

// formatTimestamp attempts to parse and reformat an ISO8601 timestamp.
func formatTimestamp(ts string) string {
	if ts == "" {
		return "(no timestamp)"
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		log.Printf("Warning: could not parse timestamp '%s': %v", ts, err)
		return ts
	}
	return t.Local().Format(time.Stamp)
}
