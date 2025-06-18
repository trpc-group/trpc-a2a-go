// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/googleai"
	"trpc.group/trpc-go/trpc-a2a-go/client"
	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// rootAgentProcessor implements the taskmanager.MessageProcessor interface.
type rootAgentProcessor struct {
	// LLM client for decision making
	llm *googleai.GoogleAI
	// Subagent clients
	creativeClient      *client.A2AClient
	exchangeClient      *client.A2AClient
	reimbursementClient *client.A2AClient
}

// ProcessMessage implements the taskmanager.MessageProcessor interface
func (p *rootAgentProcessor) ProcessMessage(
	ctx context.Context,
	message protocol.Message,
	options taskmanager.ProcessOptions,
	handle taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
	// Extract text from the incoming message
	text := extractText(message)
	if text == "" {
		errMsg := "input message must contain text"
		log.Error("Message processing failed: %s", errMsg)

		// Return error message directly
		errorMessage := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]protocol.Part{protocol.NewTextPart(errMsg)},
		)
		return &taskmanager.MessageProcessingResult{
			Result: &errorMessage,
		}, nil
	}

	log.Info("RootAgent received new request: %s", text)

	// Use Gemini or rule-based routing to decide which subagent to route the task to
	subagent, err := p.routeTaskToSubagent(ctx, text)
	if err != nil {
		log.Error("Error routing task: %v", err)
		errMsg := fmt.Sprintf("Failed to process your request: %v", err)
		errMessage := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]protocol.Part{protocol.NewTextPart(errMsg)},
		)
		return &taskmanager.MessageProcessingResult{
			Result: &errMessage,
		}, nil
	}

	var result string

	// Forward the task to the appropriate subagent
	switch subagent {
	case "creative":
		log.Info("Routing to creative agent.")
		result, err = p.callCreativeAgent(ctx, text)
	case "exchange":
		log.Info("Routing to exchange agent.")
		result, err = p.callExchangeAgent(ctx, text)
	case "reimbursement":
		log.Info("Routing to reimbursement agent.")
		result, err = p.callReimbursementAgent(ctx, text)
	default:
		// Handle using the root agent's own logic if no specific subagent was identified
		log.Info("No specific subagent identified, handling with root agent.")
		result = fmt.Sprintf("I'm not sure how to process your request: '%s'. You can try asking me to write something creative, check currency exchange rates, or submit a reimbursement request.", text)
		err = nil
	}

	if err != nil {
		log.Error("Error from subagent: %v", err)
		errMsg := fmt.Sprintf("Failed to get response from subagent: %v", err)
		errMessage := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]protocol.Part{protocol.NewTextPart(errMsg)},
		)
		return &taskmanager.MessageProcessingResult{
			Result: &errMessage,
		}, nil
	}

	// Create response message
	responseMessage := protocol.NewMessage(
		protocol.MessageRoleAgent,
		[]protocol.Part{protocol.NewTextPart(result)},
	)

	return &taskmanager.MessageProcessingResult{
		Result: &responseMessage,
	}, nil
}

// routeTaskToSubagent uses the LLM to decide which subagent should handle the task.
func (p *rootAgentProcessor) routeTaskToSubagent(ctx context.Context, text string) (string, error) {
	if p.llm == nil {
		// Simple rule-based routing if LLM is not available
		text = strings.ToLower(text)
		if strings.Contains(text, "write") || strings.Contains(text, "story") ||
			strings.Contains(text, "poem") || strings.Contains(text, "creative") {
			return "creative", nil
		} else if strings.Contains(text, "exchange") || strings.Contains(text, "currency") ||
			strings.Contains(text, "convert") || strings.Contains(text, "rate") {
			return "exchange", nil
		} else if strings.Contains(text, "reimburse") || strings.Contains(text, "expense") ||
			strings.Contains(text, "receipt") || strings.Contains(text, "payment") {
			return "reimbursement", nil
		}
		return "", nil
	}

	// Use Gemini LLM to determine which subagent should handle the request
	prompt := fmt.Sprintf(
		"Based on the following user request, determine which agent should handle it:\n\n"+
			"User request: %s\n\n"+
			"Available agents:\n"+
			"1. 'creative' - Creative writing agent that can write stories, poems, or other creative text\n"+
			"2. 'exchange' - Currency exchange agent that can provide exchange rates\n"+
			"3. 'reimbursement' - Reimbursement agent that can process expense reports\n\n"+
			"Respond with ONLY one word: 'creative', 'exchange', 'reimbursement', or 'none' if no agent is applicable.",
		text,
	)

	completion, err := p.llm.Call(ctx, prompt, llms.WithTemperature(0))
	if err != nil {
		return "", fmt.Errorf("LLM error: %v", err)
	}

	// Extract and normalize the agent name
	subagent := strings.ToLower(strings.TrimSpace(completion))
	if subagent == "none" {
		return "", nil
	}

	// Validate that the response is one of our expected agents
	validAgents := map[string]bool{
		"creative":      true,
		"exchange":      true,
		"reimbursement": true,
	}

	if _, ok := validAgents[subagent]; !ok {
		return "", nil
	}

	return subagent, nil
}

// callCreativeAgent forwards a task to the creative writing agent.
func (p *rootAgentProcessor) callCreativeAgent(ctx context.Context, text string) (string, error) {
	task, err := p.creativeClient.SendTasks(ctx, protocol.SendTaskParams{
		Message: protocol.Message{
			Role: protocol.MessageRoleUser,
			Parts: []protocol.Part{
				protocol.NewTextPart(text),
			},
		},
	})

	if err != nil {
		return "", err
	}

	if task.Status.Message == nil {
		return "", fmt.Errorf("no response message from creative agent")
	}

	return extractText(*task.Status.Message), nil
}

// callExchangeAgent forwards a task to the exchange agent.
func (p *rootAgentProcessor) callExchangeAgent(ctx context.Context, text string) (string, error) {
	task, err := p.exchangeClient.SendTasks(ctx, protocol.SendTaskParams{
		Message: protocol.Message{
			Role: protocol.MessageRoleUser,
			Parts: []protocol.Part{
				protocol.NewTextPart(text),
			},
		},
	})

	if err != nil {
		return "", err
	}

	if task.Status.Message == nil {
		return "", fmt.Errorf("no response message from exchange agent")
	}

	return extractText(*task.Status.Message), nil
}

// callReimbursementAgent forwards a task to the reimbursement agent.
func (p *rootAgentProcessor) callReimbursementAgent(ctx context.Context, text string) (string, error) {
	task, err := p.reimbursementClient.SendTasks(ctx, protocol.SendTaskParams{
		Message: protocol.Message{
			Role: protocol.MessageRoleUser,
			Parts: []protocol.Part{
				protocol.NewTextPart(text),
			},
		},
	})

	if err != nil {
		return "", err
	}

	if task.Status.Message == nil {
		return "", fmt.Errorf("no response message from reimbursement agent")
	}

	return extractText(*task.Status.Message), nil
}

// extractText extracts the text content from a message.
func extractText(message protocol.Message) string {
	for _, part := range message.Parts {
		if textPart, ok := part.(*protocol.TextPart); ok {
			return textPart.Text
		}
	}
	return ""
}

// getAgentCard returns the agent's metadata.
func getAgentCard() server.AgentCard {
	return server.AgentCard{
		Name:        "Multi-Agent Router",
		Description: "An agent that routes tasks to appropriate subagents.",
		URL:         "http://localhost:8080",
		Version:     "1.0.0",
		Capabilities: server.AgentCapabilities{
			Streaming:              boolPtr(false),
			PushNotifications:      boolPtr(false),
			StateTransitionHistory: boolPtr(true),
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills: []server.AgentSkill{
			{
				ID:          "route",
				Name:        "Task Routing",
				Description: stringPtr("Routes tasks to the appropriate specialized agent."),
				Tags:        []string{"routing", "multi-agent", "orchestration"},
				Examples: []string{
					"Write a poem about autumn",
					"What's the exchange rate from USD to EUR?",
					"I need to get reimbursed for a $50 business lunch",
				},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
		},
	}
}

// stringPtr is a helper function to get a pointer to a string.
func stringPtr(s string) *string {
	return &s
}

// boolPtr is a helper function to get a pointer to a bool.
func boolPtr(b bool) *bool {
	return &b
}

func main() {
	port := flag.Int("port", 8080, "Port to listen on for the root agent")
	creativeAgentURL := flag.String("creative-url", "http://localhost:8082", "URL for the creative writing agent")
	exchangeAgentURL := flag.String("exchange-url", "http://localhost:8081", "URL for the exchange agent")
	reimbursementAgentURL := flag.String("reimbursement-url", "http://localhost:8083", "URL for the reimbursement agent")
	flag.Parse()

	// Create the processor
	processor := &rootAgentProcessor{}

	// Initialize subagent clients
	var err error
	processor.creativeClient, err = client.NewA2AClient(*creativeAgentURL)
	if err != nil {
		log.Fatal("Failed to create creative agent client: %v", err)
	}

	processor.exchangeClient, err = client.NewA2AClient(*exchangeAgentURL)
	if err != nil {
		log.Fatal("Failed to create exchange agent client: %v", err)
	}

	processor.reimbursementClient, err = client.NewA2AClient(*reimbursementAgentURL)
	if err != nil {
		log.Fatal("Failed to create reimbursement agent client: %v", err)
	}

	// Try to initialize the LLM if an API key is available
	apiKey := os.Getenv("GOOGLE_API_KEY")
	if apiKey != "" {
		ctx := context.Background()
		processor.llm, err = googleai.New(
			ctx,
			googleai.WithAPIKey(apiKey),
			googleai.WithDefaultModel("gemini-1.5-flash"),
		)
		if err != nil {
			log.Warn("Failed to initialize Gemini LLM: %v. Will use rule-based routing.", err)
		} else {
			log.Info("Successfully initialized Gemini LLM for task routing.")
		}
	} else {
		log.Info("No GOOGLE_API_KEY environment variable found. Using rule-based routing.")
	}

	// Create task manager with our processor
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

	addr := fmt.Sprintf(":%d", *port)
	log.Info("Starting Root Agent server on %s", addr)

	if err := a2aServer.Start(addr); err != nil {
		log.Fatal("Failed to start A2A server: %v", err)
	}
}
