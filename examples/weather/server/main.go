// Package main implements a Weather Agent server.
// This example demonstrates how to build a simple, non-streaming A2A agent
// that provides weather information based on a city name.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// weatherProcessor implements the taskmanager.MessageProcessor interface.
// This struct is where the core business logic of our agent resides.
type weatherProcessor struct{}

// ProcessMessage is the single required method for the MessageProcessor interface.
// It gets called by the task manager whenever a new message arrives.
func (p *weatherProcessor) ProcessMessage(
	ctx context.Context,
	message protocol.Message,
	options taskmanager.ProcessOptions,
	handler taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
	// For this simple agent, we only support non-streaming (blocking) requests.
	if options.Streaming {
		errMsg := "This weather agent does not support streaming."
		log.Errorf(errMsg)
		errorMessage := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]protocol.Part{protocol.NewTextPart(errMsg)},
		)
		return &taskmanager.MessageProcessingResult{Result: &errorMessage}, nil
	}

	// Extract the city name from the incoming message.
	location := extractText(message)
	if location == "" {
		errMsg := "Please provide a city name to get the weather."
		log.Errorf("Message processing failed: %s", errMsg)
		errorMessage := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]protocol.Part{protocol.NewTextPart(errMsg)},
		)
		return &taskmanager.MessageProcessingResult{Result: &errorMessage}, nil
	}

	log.Infof("Processing message with input location: %s", location)

	// Call our business logic function to get the weather.
	weatherInfo, err := getWeather(location, "https://wttr.in/")
	if err != nil {
		errMsg := fmt.Sprintf("Failed to get weather for %s: %v", location, err)
		log.Errorf("getWeather failed: %s", errMsg)
		errorMessage := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]protocol.Part{protocol.NewTextPart(errMsg)},
		)
		return &taskmanager.MessageProcessingResult{Result: &errorMessage}, nil
	}

	// If successful, create a message with the weather info.
	successMessage := protocol.NewMessage(
		protocol.MessageRoleAgent,
		[]protocol.Part{protocol.NewTextPart(weatherInfo)},
	)

	// Wrap the success message in the result struct.
	return &taskmanager.MessageProcessingResult{
		Result: &successMessage,
	}, nil
}

// extractText is a helper function to get the first text part from a message.
func extractText(message protocol.Message) string {
	for _, part := range message.Parts {
		if textPart, ok := part.(*protocol.TextPart); ok {
			return textPart.Text
		}
	}
	return ""
}

// getWeather fetches weather information from the external wttr.in API.
func getWeather(location string, baseURL string) (string, error) {
	// URL-encode the location to handle spaces or special characters safely.
	encodedLocation := url.QueryEscape(location)
	// We use the simple format=3 for a concise weather report.
	fullURL := fmt.Sprintf("%s/%s?format=3", baseURL, encodedLocation)

	resp, err := http.Get(fullURL)
	if err != nil {
		// Wrap the error with context using %w, preserving the original error.
		return "", fmt.Errorf("failed to request weather API: %w", err)
	}
	// It's crucial to close the response body to prevent resource leaks.
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("weather API returned non-200 status: %s", resp.Status)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read weather API response: %w", err)
	}

	return string(bodyBytes), nil
}

// boolPtr is a helper function to get a pointer to a boolean value.
func boolPtr(b bool) *bool {
	return &b
}

func main() {
	host := "localhost"
	port := 8080

	// --- Define the Agent's Identity Card ---
	// The AgentCard provides metadata about the agent to clients.
	agentCard := server.AgentCard{
		Name:        "Weather Agent",
		Description: "An agent that provides real-time weather for a given city.",
		URL:         fmt.Sprintf("http://%s:%d/", host, port),
		Version:     "1.0.0",
		Provider: &server.AgentProvider{
			Organization: "tRPC-A2A-Go Examples",
		},
		// Capabilities clearly state what this agent can and cannot do.
		Capabilities: server.AgentCapabilities{
			Streaming:              boolPtr(false), // We explicitly say we don't support streaming.
			PushNotifications:      boolPtr(false),
			StateTransitionHistory: boolPtr(false),
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		// Skills describe the specific tasks the agent can perform.
		Skills: []server.AgentSkill{
			{
				ID:          "weather_query",
				Name:        "Weather Query",
				Description: func(s string) *string { return &s }("Get real-time weather for a city by name."),
				Tags:        []string{"weather", "location"},
				Examples:    []string{"beijing", "London"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
		},
	}

	// --- Set up the Core Components ---
	processor := &weatherProcessor{}
	// The task manager is responsible for orchestrating message processing.
	// We use a simple in-memory manager for this example.
	taskManager, err := taskmanager.NewMemoryTaskManager(processor)
	if err != nil {
		log.Fatalf("Failed to create task manager: %v", err)
	}

	// The server ties the agent card and task manager together into an HTTP service.
	srv, err := server.NewA2AServer(agentCard, taskManager)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	// --- Graceful Shutdown Handling ---
	// This setup ensures that if we press Ctrl+C, the server shuts down cleanly.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start the server in a new goroutine so it doesn't block the main thread.
	go func() {
		serverAddr := fmt.Sprintf("%s:%d", host, port)
		log.Infof("Starting Weather Agent server on %s...", serverAddr)
		// srv.Start is a blocking call.
		if err := srv.Start(serverAddr); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// Block the main thread here, waiting for a shutdown signal.
	sig := <-sigChan
	log.Infof("Received signal %v, shutting down...", sig)

	// Implement graceful shutdown logic here in a real-world application.
	// For this example, we simply exit.
}
