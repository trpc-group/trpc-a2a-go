// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
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

	"trpc.group/trpc-go/trpc-a2a-go/client"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
)

// Config holds the application configuration.
type Config struct {
	AgentURL         string
	Timeout          time.Duration
	ForceNoStreaming bool
	ContextID        string
	UseTasksGet      bool
	HistoryLength    int
	ServerPort       int
	ServerHost       string
}

// Command types for CLI
const (
	cmdExit    = "exit"
	cmdHelp    = "help"
	cmdContext = "context"
	cmdMode    = "mode"
	cmdCancel  = "cancel"
	cmdGet     = "get"
	cmdCard    = "card"
	cmdPush    = "push"
	cmdGetPush = "getpush"
	cmdServer  = "server"
)

// Global variable to track the push notification server
var pushServer *http.Server

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
	flag.BoolVar(&config.UseTasksGet, "use-tasks-get", true, "Use tasks/get to fetch final state")
	flag.IntVar(&config.HistoryLength, "history", 0, "Number of history messages to request (0 = none)")
	flag.IntVar(&config.ServerPort, "port", 8090, "Port for push notification server")
	flag.StringVar(&config.ServerHost, "host", "localhost", "Host for push notification server")
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
	cardURL := baseURL + ".well-known/agent.json"

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
			if cmdResult := processCommand(
				a2aClient,
				input,
				&config,
				&contextID,
				&useStreaming,
				lastTaskID,
			); cmdResult {
				lastTaskState = ""
				continue
			}

			// Process the user input and handle the agent interaction
			taskID := processUserInput(a2aClient, input, contextID, config, useStreaming)

			// Update the last task ID and check task state if a task was created
			if taskID != "" {
				lastTaskID = taskID

				// Get the current task state to check if it's input-required
				ctx, cancel := context.WithTimeout(context.Background(), config.Timeout)
				task, err := a2aClient.GetTasks(ctx, protocol.TaskQueryParams{ID: taskID})
				cancel()

				if err == nil && task != nil {
					lastTaskState = task.Status.State
				} else {
					lastTaskState = ""
				}
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
		if cmdResult := processCommand(
			a2aClient,
			input,
			&config,
			&contextID,
			&useStreaming,
			lastTaskID,
		); cmdResult {
			// Reset task state after command processing
			lastTaskState = ""
			continue
		}

		// Process the user input and handle the agent interaction
		taskID := processUserInput(a2aClient, input, contextID, config, useStreaming)

		// Update the last task ID and check task state if a task was created
		if taskID != "" {
			lastTaskID = taskID

			// Get the current task state to check if it's input-required
			ctx, cancel := context.WithTimeout(context.Background(), config.Timeout)
			task, err := a2aClient.GetTasks(ctx, protocol.TaskQueryParams{ID: taskID})
			cancel()

			if err == nil && task != nil {
				lastTaskState = task.Status.State

				// Display a message if input is required
				if lastTaskState == protocol.TaskStateInputRequired {
					fmt.Println(strings.Repeat("-", 60))
					fmt.Println("[Additional input required to complete this task. Continue typing.]")
				}
			} else {
				lastTaskState = ""
			}
		}
	}
}

// processCommand handles built-in client commands and returns true if a command was processed.
func processCommand(
	a2aClient *client.A2AClient,
	input string,
	config *Config,
	contextID *string,
	useStreaming *bool,
	lastTaskID string,
) bool {
	parts := strings.Fields(input)
	cmd := strings.ToLower(parts[0])

	switch cmd {
	case cmdExit:
		// Stop the push server if it's running
		if pushServer != nil {
			stopPushServer()
		}
		fmt.Println("Exiting.")
		os.Exit(0)
		return true

	case cmdHelp:
		displayHelpMessage()
		return true

	case cmdContext:
		if len(parts) > 1 {
			// Set new context ID
			*contextID = parts[1]
			fmt.Printf("Context ID set to: %s\n", *contextID)
		} else {
			// Generate new context ID
			*contextID = protocol.GenerateContextID()
			fmt.Printf("Generated new context ID: %s\n", *contextID)
		}
		return true

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
		return true

	case cmdCancel:
		taskID := lastTaskID
		if len(parts) > 1 {
			taskID = parts[1]
		}

		if taskID == "" {
			fmt.Println("No task ID provided or available from last request.")
			return true
		}

		cancelTask(a2aClient, taskID, config.Timeout)
		return true

	case cmdGet:
		taskID := lastTaskID
		if len(parts) > 1 {
			taskID = parts[1]
		}

		if taskID == "" {
			fmt.Println("No task ID provided or available from last request.")
			return true
		}

		historyLength := config.HistoryLength
		if len(parts) > 2 {
			_, err := fmt.Sscanf(parts[2], "%d", &historyLength)
			if err != nil {
				fmt.Printf("Invalid history length: %s. Using default: %d\n", parts[2], config.HistoryLength)
				historyLength = config.HistoryLength
			}
		}

		getTask(a2aClient, taskID, historyLength, config.Timeout)
		return true

	case cmdCard:
		// Fetch and display agent card
		agentCard, err := fetchAgentCard(config.AgentURL)
		if err != nil {
			fmt.Printf("Failed to fetch agent card: %v\n", err)
			return true
		}

		displayAgentCapabilities(agentCard)
		return true

	case cmdPush:
		if len(parts) < 3 {
			fmt.Println("Usage: push <task-id> <callback-url> [token]")
			return true
		}

		taskID := parts[1]
		callbackURL := parts[2]
		var token *string

		if len(parts) > 3 {
			tokenStr := parts[3]
			token = &tokenStr
		}

		setPushNotification(a2aClient, taskID, callbackURL, token, config.Timeout)
		return true

	case cmdGetPush:
		if len(parts) < 2 {
			fmt.Println("Usage: getpush <task-id>")
			return true
		}

		getPushNotification(a2aClient, parts[1], config.Timeout)
		return true

	case "new":
		// Force start a new context
		*contextID = protocol.GenerateContextID()
		fmt.Printf("Starting a new context: %s\n", *contextID)
		return true

	case cmdServer:
		if len(parts) > 1 && parts[1] == "start" {
			// Start the push notification server
			if err := startPushServer(*config); err != nil {
				fmt.Printf("Failed to start server: %v\n", err)
			} else {
				// Display the server URL for convenience
				fmt.Printf("Push notification server started at http://%s:%d/push\n",
					config.ServerHost, config.ServerPort)
				fmt.Println("Use this URL for push notifications.")
			}
		} else if len(parts) > 1 && parts[1] == "stop" {
			// Stop the push notification server
			if err := stopPushServer(); err != nil {
				fmt.Printf("Failed to stop server: %v\n", err)
			}
		} else {
			fmt.Println("Usage: server start|stop")
		}
		return true
	}

	return false
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
	fmt.Println("  push <task-id> <url> [token] - Set push notification for a task")
	fmt.Println("  getpush <task-id>        - Get push notification configuration for a task")
	fmt.Println("  server start             - Start push notification server")
	fmt.Println("  server stop              - Stop push notification server")
	fmt.Println("  new                      - Start a new context")
	fmt.Println("")
	fmt.Println("For normal interaction, just type your message and press Enter.")
	fmt.Println("Messages in the same context will maintain conversation history.")
	fmt.Println(strings.Repeat("-", 60))
}

// processUserInput handles a single user input, sends it to the agent, and processes the response.
func processUserInput(
	a2aClient *client.A2AClient,
	input,
	contextID string,
	config Config,
	useStreaming bool,
) string {
	// Create message with context
	message := protocol.NewMessageWithContext(
		protocol.MessageRoleUser,
		[]protocol.Part{protocol.NewTextPart(input)},
		nil, // taskID
		&contextID,
	)

	// Create message parameters
	params := createMessageParams(message, config.HistoryLength)

	// Send the request and process the response based on mode
	var taskID string
	if useStreaming && !config.ForceNoStreaming {
		taskID = handleStreamingInteraction(a2aClient, params, config)
	} else {
		taskID = handleStandardInteraction(a2aClient, params, config)
	}

	return taskID
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
) string {
	// Create context for the stream.
	ctx, cancel := context.WithTimeout(context.Background(), config.Timeout*2)
	defer cancel()

	log.Printf("Sending stream request for message %s (Context: %s)...", params.Message.MessageID, *params.Message.ContextID)
	eventChan, streamErr := a2aClient.StreamMessage(ctx, params)

	if streamErr != nil {
		log.Printf("ERROR: StreamMessage request failed: %v", streamErr)
		fmt.Println(strings.Repeat("-", 60))
		return ""
	}

	// Process the stream response.
	taskID := processStreamResponse(ctx, eventChan)

	log.Printf("Stream processing finished for message %s", params.Message.MessageID)
	fmt.Println(strings.Repeat("-", 60))

	return taskID
}

// handleStandardInteraction sends a standard (non-streaming) request to the agent.
func handleStandardInteraction(
	a2aClient *client.A2AClient,
	params protocol.SendMessageParams,
	config Config,
) string {
	// Create context for the request.
	ctx, cancel := context.WithTimeout(context.Background(), config.Timeout)
	defer cancel()

	log.Printf("Sending standard request for message %s (Context: %s)...", params.Message.MessageID, *params.Message.ContextID)

	// Send the message
	result, err := a2aClient.SendMessage(ctx, params)

	if err != nil {
		log.Printf("ERROR: SendMessage request failed: %v", err)
		fmt.Println(strings.Repeat("-", 60))
		return ""
	}

	// Process the response
	fmt.Println("\n<< Agent Response:")
	fmt.Println(strings.Repeat("-", 10))

	var taskID string
	switch response := result.Result.(type) {
	case *protocol.Message:
		fmt.Println("  Message Response:")
		printMessage(*response)

	case *protocol.Task:
		taskID = response.ID
		// Display task state
		fmt.Printf("  Task %s State: %s (%s)\n", response.ID, response.Status.State, formatTimestamp(response.Status.Timestamp))

		// Display message if present
		if response.Status.Message != nil {
			fmt.Println("  Message:")
			printMessage(*response.Status.Message)
		}

		// Display artifacts if present
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

		// Display history if present
		if response.History != nil && len(response.History) > 0 {
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

		// Add special handling for input-required state
		if response.Status.State == protocol.TaskStateInputRequired {
			fmt.Println("  [Additional input required]")
		}

	default:
		fmt.Printf("  Unknown response type: %T\n", response)
	}

	fmt.Println(strings.Repeat("-", 60))
	return taskID
}

// processStreamResponse processes the stream of events from the agent.
func processStreamResponse(
	ctx context.Context, eventChan <-chan protocol.StreamingMessageEvent,
) string {
	fmt.Println("\n<< Agent Response Stream:")
	fmt.Println(strings.Repeat("-", 10))

	var taskID string

	for {
		select {
		case <-ctx.Done():
			// Context timed out or was cancelled
			log.Printf("ERROR: Context timeout or cancellation while waiting for stream events: %v", ctx.Err())
			return taskID

		case event, ok := <-eventChan:
			if !ok {
				// Channel closed by the client/server
				log.Println("Stream channel closed.")
				if ctx.Err() != nil {
					log.Printf("Context error after stream close: %v", ctx.Err())
				}
				return taskID
			}

			// Process the received event based on its type
			switch e := event.Result.(type) {
			case *protocol.Message:
				fmt.Println("  [Message Response:]")
				printMessage(*e)

			case *protocol.Task:
				taskID = e.ID
				fmt.Printf("  [Task %s State: %s (%s)]\n", e.ID, e.Status.State, formatTimestamp(e.Status.Timestamp))
				if e.Status.Message != nil {
					printMessage(*e.Status.Message)
				}

			case *protocol.TaskStatusUpdateEvent:
				taskID = e.TaskID
				fmt.Printf("  [Status Update: %s (%s)]\n", e.Status.State, formatTimestamp(e.Status.Timestamp))
				if e.Status.Message != nil {
					printMessage(*e.Status.Message)
				}

				// Handle final states and input-required state
				if e.Status.State == protocol.TaskStateInputRequired {
					fmt.Println("  [Additional input required]")
					return taskID
				} else if e.Final != nil && *e.Final {
					log.Printf("Final status received: %s", e.Status.State)

					// Print a message indicating task completion state
					if e.Status.State == protocol.TaskStateCompleted {
						fmt.Println("  [Task completed successfully]")
					} else if e.Status.State == protocol.TaskStateFailed {
						fmt.Println("  [Task failed]")
					} else if e.Status.State == protocol.TaskStateCanceled {
						fmt.Println("  [Task was canceled]")
					}
					return taskID
				}

			case *protocol.TaskArtifactUpdateEvent:
				taskID = e.TaskID
				// Get the artifact name or use a default
				name := getArtifactName(e.Artifact)

				fmt.Printf("  [Artifact Update: %s]\n", name)

				// Print the artifact parts
				printParts(e.Artifact.Parts)

				// For artifact updates, we note it's the final artifact,
				// but we don't exit yet - per A2A spec, we should wait for the final status update
				if e.LastChunk != nil && *e.LastChunk {
					log.Printf("Final artifact received with ID %s", e.Artifact.ArtifactID)
				}

			default:
				log.Printf("Warning: Received unknown event type: %T\n", event.Result)
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

// cancelTask attempts to cancel a running task.
func cancelTask(a2aClient *client.A2AClient, taskID string, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	log.Printf("Attempting to cancel task %s...", taskID)

	task, err := a2aClient.CancelTasks(ctx, protocol.TaskIDParams{ID: taskID})

	if err != nil {
		log.Printf("ERROR: Failed to cancel task %s: %v", taskID, err)
		fmt.Printf("Failed to cancel task: %v\n", err)
		return
	}

	fmt.Println("Task cancellation result:")
	fmt.Printf("  State: %s (%s)\n", task.Status.State, formatTimestamp(task.Status.Timestamp))

	if task.Status.Message != nil {
		fmt.Println("  Message:")
		printMessage(*task.Status.Message)
	}
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
func printParts(parts []protocol.Part) {
	for _, part := range parts {
		printPart(part)
	}
}

// printPart prints a single part with proper indentation.
func printPart(part interface{}) {
	const indent = "    "
	switch p := part.(type) {
	case *protocol.TextPart:
		fmt.Println(indent + p.Text)
	case protocol.TextPart:
		fmt.Println(indent + p.Text)
	case map[string]interface{}:
		// Handle parts that come as maps (from JSON)
		if typeStr, ok := p["type"].(string); ok && typeStr == "text" {
			if text, ok := p["text"].(string); ok {
				fmt.Println(indent + text)
			}
		} else {
			// For other types, just print the map
			fmt.Printf("%s[Structured Content: %+v]\n", indent, p)
		}
	default:
		fmt.Printf("%s[Unknown Part Type: %T]\n", indent, p)
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

// PushNotificationHandler handles incoming push notifications
func PushNotificationHandler(w http.ResponseWriter, r *http.Request) {
	// Only accept POST requests
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Read and parse the request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("Error reading push notification body: %v", err)
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}

	// Validate JWT token if provided
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		// In a real implementation, validate the token here
		// For this example, we just log it
		token := strings.TrimPrefix(authHeader, "Bearer ")
		log.Printf("Received notification with token: %s", token)
	}

	// Log the notification
	log.Printf("Received push notification: %s", string(body))

	// Parse the notification
	var notification map[string]interface{}
	if err := json.Unmarshal(body, &notification); err != nil {
		log.Printf("Error parsing notification JSON: %v", err)
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Display the notification
	fmt.Println("\n[PUSH NOTIFICATION RECEIVED]")
	fmt.Println(strings.Repeat("-", 60))
	taskID, _ := notification["id"].(string)
	fmt.Printf("Task ID: %s\n", taskID)

	// Display status update if present
	if status, ok := notification["status"].(map[string]interface{}); ok {
		state, _ := status["state"].(string)
		timestamp, _ := status["timestamp"].(string)
		fmt.Printf("Status: %s (%s)\n", state, timestamp)

		// Display message if present
		if message, ok := status["message"].(map[string]interface{}); ok {
			role, _ := message["role"].(string)
			fmt.Printf("Message from %s:\n", role)

			if parts, ok := message["parts"].([]interface{}); ok {
				for _, part := range parts {
					if textPart, ok := part.(map[string]interface{}); ok {
						if text, ok := textPart["text"].(string); ok {
							fmt.Printf("  %s\n", text)
						}
					}
				}
			}
		}
	}

	// Display artifact if present
	if artifact, ok := notification["artifact"].(map[string]interface{}); ok {
		name, _ := artifact["name"].(string)
		fmt.Printf("Artifact: %s\n", name)

		if parts, ok := artifact["parts"].([]interface{}); ok {
			for _, part := range parts {
				if textPart, ok := part.(map[string]interface{}); ok {
					if text, ok := textPart["text"].(string); ok {
						fmt.Printf("  %s\n", text)
					}
				}
			}
		}
	}

	fmt.Println(strings.Repeat("-", 60))

	// Respond with success
	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

// startPushServer starts an HTTP server to receive push notifications
func startPushServer(config Config) error {
	if pushServer != nil {
		return fmt.Errorf("server is already running")
	}

	// Create a new server mux
	mux := http.NewServeMux()
	mux.HandleFunc("/push", PushNotificationHandler)

	// Create the server
	addr := fmt.Sprintf("%s:%d", config.ServerHost, config.ServerPort)
	pushServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	// Start the server in a goroutine
	go func() {
		log.Printf("Starting push notification server on %s", addr)
		if err := pushServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Push server error: %v", err)
		}
	}()

	return nil
}

// stopPushServer gracefully stops the push notification server
func stopPushServer() error {
	if pushServer == nil {
		return fmt.Errorf("no server is running")
	}

	// Create a timeout context for shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Attempt to gracefully shut down the server
	if err := pushServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("server shutdown failed: %v", err)
	}

	pushServer = nil
	log.Println("Push notification server stopped")
	return nil
}

// setPushNotification sets up push notification for a task
func setPushNotification(
	a2aClient *client.A2AClient,
	taskID, callbackURL string,
	token *string,
	timeout time.Duration,
) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	log.Printf("Setting push notification for task %s to URL %s", taskID, callbackURL)

	// Create the push notification configuration
	pushConfig := protocol.PushNotificationConfig{
		URL: callbackURL,
	}

	// Set token if provided
	if token != nil {
		pushConfig.Token = *token
	}

	// Create the task push notification configuration using TaskID field
	taskPushConfig := protocol.TaskPushNotificationConfig{
		TaskID:                 taskID,
		PushNotificationConfig: pushConfig,
	}

	// Call the client method to set push notification
	result, err := a2aClient.SetPushNotification(ctx, taskPushConfig)
	if err != nil {
		log.Printf("ERROR: Failed to set push notification: %v", err)
		fmt.Printf("Failed to set push notification: %v\n", err)
		return
	}

	// Display success
	fmt.Println("Push notification set successfully:")
	fmt.Printf("  Task ID: %s\n", result.TaskID)
	fmt.Printf("  URL: %s\n", result.PushNotificationConfig.URL)
	if result.PushNotificationConfig.Token != "" {
		fmt.Printf("  Token: %s\n", result.PushNotificationConfig.Token)
	}
}

// getPushNotification gets the push notification configuration for a task
func getPushNotification(a2aClient *client.A2AClient, taskID string, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	log.Printf("Getting push notification config for task %s", taskID)

	// Create task ID params
	taskIDParams := protocol.TaskIDParams{
		ID: taskID,
	}

	// Call the client method to get push notification
	result, err := a2aClient.GetPushNotification(ctx, taskIDParams)
	if err != nil {
		log.Printf("ERROR: Failed to get push notification: %v", err)
		fmt.Printf("Failed to get push notification: %v\n", err)
		return
	}

	// Display the push notification configuration
	fmt.Println("Push notification configuration:")
	fmt.Printf("  Task ID: %s\n", result.TaskID)
	fmt.Printf("  URL: %s\n", result.PushNotificationConfig.URL)
	if result.PushNotificationConfig.Token != "" {
		fmt.Printf("  Token: %s\n", result.PushNotificationConfig.Token)
	}

	// Display authentication info if present
	if result.PushNotificationConfig.Authentication != nil {
		auth := result.PushNotificationConfig.Authentication
		if len(auth.Schemes) > 0 {
			fmt.Printf("  Authentication Schemes: %v\n", auth.Schemes)
		}
		if auth.Credentials != nil {
			fmt.Printf("  Credentials: %s\n", *auth.Credentials)
		}
	}

	// Display metadata if present
	if len(result.PushNotificationConfig.Metadata) > 0 {
		fmt.Println("  Metadata:")
		for key, value := range result.PushNotificationConfig.Metadata {
			fmt.Printf("    %s: %v\n", key, value)
		}
	}
}
