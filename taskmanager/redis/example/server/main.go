package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
	redisTaskManager "trpc.group/trpc-go/trpc-a2a-go/taskmanager/redis"
)

// ToLowerProcessor implements a simple text processing service that converts text to lowercase
type ToLowerProcessor struct{}

func (p *ToLowerProcessor) ProcessMessage(
	ctx context.Context,
	message protocol.Message,
	options taskmanager.ProcessOptions,
	handle taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
	log.Printf("Processing message: %s", message.MessageID)

	// Extract text from message parts
	var inputText string
	for _, part := range message.Parts {
		if textPart, ok := part.(*protocol.TextPart); ok {
			inputText += textPart.Text
		}
	}

	if inputText == "" {
		return &taskmanager.MessageProcessingResult{
			Result: &protocol.Message{
				Role: protocol.MessageRoleAgent,
				Parts: []protocol.Part{
					protocol.NewTextPart("Error: No text found in message"),
				},
			},
		}, nil
	}

	if options.Streaming {
		// Streaming mode - process with task updates
		task, err := handle.BuildTask(nil, message.ContextID)
		if err != nil {
			return nil, fmt.Errorf("failed to build task: %w", err)
		}

		// Subscribe to the task
		subscriber, err := handle.SubScribeTask(&task.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to subscribe to task: %w", err)
		}

		// Process asynchronously
		go p.processTextAsync(ctx, inputText, task.ID, handle)

		return &taskmanager.MessageProcessingResult{
			StreamingEvents: subscriber,
		}, nil
	} else {
		// Non-streaming mode - return result directly
		result := strings.ToLower(inputText)

		response := &protocol.Message{
			Role: protocol.MessageRoleAgent,
			Parts: []protocol.Part{
				protocol.NewTextPart(result),
			},
		}

		return &taskmanager.MessageProcessingResult{
			Result: response,
		}, nil
	}
}

func (p *ToLowerProcessor) processTextAsync(
	ctx context.Context,
	inputText string,
	taskID string,
	handle taskmanager.TaskHandler,
) {
	// Update task to working state
	_, err := handle.UpdateTaskState(&taskID, protocol.TaskStateWorking, nil)
	if err != nil {
		log.Printf("Failed to update task state: %v", err)
		return
	}

	// Simulate processing time (in real world this might be calling external APIs)
	time.Sleep(1 * time.Second)

	// Process the text
	result := strings.ToLower(inputText)

	// Create an artifact with the processed text
	artifact := protocol.Artifact{
		ArtifactID:  "processed-text-" + taskID,
		Name:        stringPtr("Lowercase Text"),
		Description: stringPtr("Text converted to lowercase"),
		Parts: []protocol.Part{
			protocol.NewTextPart(result),
		},
		Metadata: map[string]interface{}{
			"operation":    "toLowerCase",
			"originalText": inputText,
			"processedAt":  time.Now().UTC().Format(time.RFC3339),
		},
	}

	// Add artifact to task
	if err := handle.AddArtifact(&taskID, artifact, true, false); err != nil {
		log.Printf("Failed to add artifact: %v", err)
	}

	// Send final status with result message
	finalMessage := &protocol.Message{
		Role: protocol.MessageRoleAgent,
		Parts: []protocol.Part{
			protocol.NewTextPart(fmt.Sprintf("Text processing completed! Original: '%s' -> Lowercase: '%s'", inputText, result)),
		},
	}

	// Update task to completed state
	_, err = handle.UpdateTaskState(&taskID, protocol.TaskStateCompleted, finalMessage)
	if err != nil {
		log.Printf("Failed to complete task: %v", err)
	}
}

func stringPtr(s string) *string {
	return &s
}

func boolPtr(b bool) *bool {
	return &b
}

func main() {
	// Parse command line flags
	var redisAddr string = "localhost:6379"
	var serverPort int = 8080

	if len(os.Args) > 1 {
		redisAddr = os.Args[1]
	}
	if len(os.Args) > 2 {
		fmt.Sscanf(os.Args[2], "%d", &serverPort)
	}

	// Create Redis client
	rdb := redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: "", // no password
		DB:       0,  // default DB
	})

	// Test Redis connection
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("Failed to connect to Redis at %s: %v", redisAddr, err)
	}
	log.Printf("Connected to Redis at %s successfully", redisAddr)

	// Create the toLower processor
	processor := &ToLowerProcessor{}

	// Create Redis TaskManager
	taskManager, err := redisTaskManager.NewRedisTaskManager(
		rdb,
		processor,
		redisTaskManager.WithExpiration(2*time.Hour), // Short expiration for demo
		redisTaskManager.WithMaxHistoryLength(50),    // Keep limited history
	)
	if err != nil {
		log.Fatalf("Failed to create Redis TaskManager: %v", err)
	}
	defer taskManager.Close()

	// Create agent card
	agentCard := server.AgentCard{
		Name:        "Text Case Converter",
		Description: "A simple agent that converts text to lowercase using Redis storage",
		URL:         fmt.Sprintf("http://localhost:%d/", serverPort),
		Version:     "1.0.0",
		Provider: &server.AgentProvider{
			Organization: "Redis TaskManager Example",
		},
		Capabilities: server.AgentCapabilities{
			Streaming:         boolPtr(true),
			PushNotifications: boolPtr(false),
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills: []server.AgentSkill{
			{
				ID:          "text_to_lower",
				Name:        "Text to Lowercase",
				Description: stringPtr("Convert any text to lowercase"),
				Tags:        []string{"text", "conversion", "lowercase"},
				Examples:    []string{"Hello World!", "THIS IS UPPERCASE", "MiXeD cAsE tExT"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
		},
	}

	// Create HTTP server
	httpServer, err := server.NewA2AServer(agentCard, taskManager)
	if err != nil {
		log.Fatalf("Failed to create A2A server: %v", err)
	}

	// Start HTTP server
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", serverPort),
		Handler: httpServer.Handler(),
	}

	go func() {
		log.Printf("Starting Text Case Converter server on port %d", serverPort)
		log.Printf("Redis backend: %s", redisAddr)
		log.Printf("Try sending text like: 'Hello World!' and it will be converted to 'hello world!'")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// Wait for interrupt signal
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	log.Println("Shutting down server...")

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
}
