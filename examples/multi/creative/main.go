package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/googleai"
	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// conversationCache to store conversation histories
type conversationCache struct {
	conversations map[string][]string // maps sessionID -> message history
}

// newConversationCache creates a new conversation cache
func newConversationCache() *conversationCache {
	return &conversationCache{
		conversations: make(map[string][]string),
	}
}

// AddMessage adds a message to the conversation history
func (c *conversationCache) AddMessage(sessionID string, message string) {
	if _, ok := c.conversations[sessionID]; !ok {
		c.conversations[sessionID] = make([]string, 0)
	}
	c.conversations[sessionID] = append(c.conversations[sessionID], message)
	if len(c.conversations[sessionID]) > 10 { // limit history to 10 messages
		c.conversations[sessionID] = c.conversations[sessionID][len(c.conversations[sessionID])-10:]
	}
}

// GetHistory retrieves the conversation history
func (c *conversationCache) GetHistory(sessionID string) []string {
	if history, ok := c.conversations[sessionID]; ok {
		return history
	}
	return []string{}
}

// creativeWritingProcessor implements the taskmanager.TaskProcessor interface
type creativeWritingProcessor struct {
	llm   llms.Model
	cache *conversationCache
}

// newCreativeWritingProcessor creates a new creative writing processor
func newCreativeWritingProcessor() (*creativeWritingProcessor, error) {
	// Initialize Google Gemini model
	llm, err := googleai.New(
		context.Background(),
		googleai.WithAPIKey(getAPIKey()),
		googleai.WithDefaultModel("gemini-1.5-flash"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Gemini model: %w", err)
	}

	return &creativeWritingProcessor{
		llm:   llm,
		cache: newConversationCache(),
	}, nil
}

func getAPIKey() string {
	apiKey := os.Getenv("GOOGLE_API_KEY")
	if apiKey == "" {
		log.Warn("GOOGLE_API_KEY environment variable not set.")
	}
	return apiKey
}

// Process implements the taskmanager.TaskProcessor interface
func (p *creativeWritingProcessor) Process(
	ctx context.Context,
	taskID string,
	message protocol.Message,
	handle taskmanager.TaskHandle,
) error {
	// Extract text from the incoming message
	prompt := extractText(message)
	if prompt == "" {
		errMsg := "input message must contain text."
		log.Error("Task %s failed: %s", taskID, errMsg)

		// Update status to Failed via handle
		failedMessage := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]protocol.Part{protocol.NewTextPart(errMsg)},
		)
		_ = handle.UpdateStatus(protocol.TaskStateFailed, &failedMessage)
		return fmt.Errorf(errMsg)
	}

	log.Info("Processing creative writing task %s with prompt: %s", taskID, prompt)

	// Get session ID from task metadata or use taskID as fallback
	sessionID := taskID

	// Update to in-progress status
	progressMessage := protocol.NewMessage(
		protocol.MessageRoleAgent,
		[]protocol.Part{protocol.NewTextPart("Crafting your creative response...")},
	)
	if err := handle.UpdateStatus(protocol.TaskStateWorking, &progressMessage); err != nil {
		log.Error("Failed to update task status: %v", err)
	}

	// Build the context from conversation history
	history := p.cache.GetHistory(sessionID)

	var fullPrompt string
	if len(history) > 0 {
		// If we have conversation history, include it for context
		historyText := strings.Join(history, "\n\n")
		fullPrompt = fmt.Sprintf("Previous conversation:\n%s\n\nNew request: %s", historyText, prompt)
	} else {
		fullPrompt = prompt
	}

	// Add creative writing instructions
	systemPrompt := "You are a creative writing assistant. Your task is to provide creative, " +
		"engaging responses to the user's prompts. Use vivid language, imaginative scenarios, " +
		"and interesting characters when appropriate. If the user asks for a specific style or format " +
		"(poem, story, joke, etc.), follow their request."
	finalPrompt := fmt.Sprintf("%s\n\n%s", systemPrompt, fullPrompt)

	// Generate the creative response using the LLM
	response, err := llms.GenerateFromSinglePrompt(ctx, p.llm, finalPrompt)
	if err != nil {
		errorMsg := fmt.Sprintf("Failed to generate response: %v", err)
		log.Error("Task %s failed: %s", taskID, errorMsg)

		errorMessage := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]protocol.Part{protocol.NewTextPart(errorMsg)},
		)
		return handle.UpdateStatus(protocol.TaskStateFailed, &errorMessage)
	}

	// Save prompt and response to conversation history
	p.cache.AddMessage(sessionID, fmt.Sprintf("User: %s", prompt))
	p.cache.AddMessage(sessionID, fmt.Sprintf("Assistant: %s", response))

	// Create response message with the generated text
	responseMessage := protocol.NewMessage(
		protocol.MessageRoleAgent,
		[]protocol.Part{protocol.NewTextPart(response)},
	)

	// Update task status to completed
	if err := handle.UpdateStatus(protocol.TaskStateCompleted, &responseMessage); err != nil {
		return fmt.Errorf("failed to update task status: %w", err)
	}

	// Add response as an artifact
	artifact := protocol.Artifact{
		Name:        stringPtr("Creative Writing Response"),
		Description: stringPtr(prompt),
		Index:       0,
		Parts:       []protocol.Part{protocol.NewTextPart(response)},
		LastChunk:   boolPtr(true),
	}

	if err := handle.AddArtifact(artifact); err != nil {
		log.Error("Error adding artifact for task %s: %v", taskID, err)
	}

	return nil
}

// extractText extracts the text content from a message
func extractText(message protocol.Message) string {
	for _, part := range message.Parts {
		if textPart, ok := part.(protocol.TextPart); ok {
			return textPart.Text
		}
	}
	return ""
}

// Helper functions
func stringPtr(s string) *string {
	return &s
}

func boolPtr(b bool) *bool {
	return &b
}

// getAgentCard returns the agent's metadata
func getAgentCard() server.AgentCard {
	return server.AgentCard{
		Name:        "Creative Writing Agent",
		Description: stringPtr("An agent that generates creative writing based on prompts using Google Gemini."),
		URL:         "http://localhost:8082",
		Version:     "1.0.0",
		Capabilities: server.AgentCapabilities{
			Streaming:              false,
			PushNotifications:      false,
			StateTransitionHistory: true,
		},
		DefaultInputModes:  []string{string(protocol.PartTypeText)},
		DefaultOutputModes: []string{string(protocol.PartTypeText)},
		Skills: []server.AgentSkill{
			{
				ID:          "creative_writing",
				Name:        "Creative Writing",
				Description: stringPtr("Creates engaging creative text based on user prompts."),
				Examples: []string{
					"Write a short story about a space explorer",
					"Compose a poem about autumn leaves",
					"Create a funny dialogue between a cat and a dog",
					"Write a brief fantasy adventure about a magical forest",
				},
			},
		},
	}
}

func main() {
	// Parse command-line flags
	host := flag.String("host", "localhost", "Host to listen on")
	port := flag.Int("port", 8082, "Port to listen on for the creative writing agent")
	flag.Parse()

	// Create the creative writing processor
	processor, err := newCreativeWritingProcessor()
	if err != nil {
		log.Fatal("Failed to create creative writing processor: %v", err)
	}

	// Create task manager and inject processor
	taskManager, err := taskmanager.NewMemoryTaskManager(processor)
	if err != nil {
		log.Fatal("Failed to create task manager: %v", err)
	}

	// Create the A2A server
	agentCard := getAgentCard()
	a2aServer, err := server.NewA2AServer(agentCard, taskManager)
	if err != nil {
		log.Fatal("Failed to create A2A server: %v", err)
	}

	// Set up a channel to listen for termination signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start the server in a goroutine
	go func() {
		serverAddr := fmt.Sprintf("%s:%d", *host, *port)
		log.Info("Starting Creative Writing Agent server on %s", serverAddr)
		if err := a2aServer.Start(serverAddr); err != nil {
			log.Fatal("Server failed: %v", err)
		}
	}()

	// Wait for termination signal
	sig := <-sigChan
	log.Info("Received signal %v, shutting down...", sig)
}
