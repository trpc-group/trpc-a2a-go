// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// Store request IDs for demonstration purposes
var requestIDs = make(map[string]bool)

// reimbursementProcessor implements the taskmanager.MessageProcessor interface
type reimbursementProcessor struct{}

// ProcessMessage implements the taskmanager.MessageProcessor interface
func (p *reimbursementProcessor) ProcessMessage(
	ctx context.Context,
	message protocol.Message,
	options taskmanager.ProcessOptions,
	handle taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
	// Extract text from the incoming message.
	text := extractText(message)
	if text == "" {
		errMsg := "input message must contain text."
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

	log.Info("Processing reimbursement request: %s", text)

	// Try to extract reimbursement details from natural language
	date, amount, purpose := extractReimbursementDetails(text)

	// Also try to extract structured form data if available
	formData := extractFormData(text)

	// Build a complete reimbursement record
	reimbursement := make(map[string]interface{})

	// Use extracted natural language data
	if date != "" {
		reimbursement["date"] = date
	}
	if amount != "" {
		reimbursement["amount"] = amount
	}
	if purpose != "" {
		reimbursement["purpose"] = purpose
	}

	// Override with structured form data if available
	for key, value := range formData {
		reimbursement[key] = value
	}

	// Generate request ID if not provided
	if _, exists := reimbursement["request_id"]; !exists {
		reimbursement["request_id"] = "REIMB-" + generateReferenceID()
	}

	// Validate the reimbursement request
	missing := validateForm(reimbursement)

	var result string
	if len(missing) > 0 {
		// Request is incomplete - ask for missing information
		result = fmt.Sprintf("Your reimbursement request is missing the following required information: %s.\n\n"+
			"Please provide:\n", strings.Join(missing, ", "))

		for _, field := range missing {
			switch field {
			case "request_id":
				result += "- Request ID (will be auto-generated if not provided)\n"
			case "date":
				result += "- Date of expense (YYYY-MM-DD format)\n"
			case "amount":
				result += "- Amount ($XX.XX format)\n"
			case "purpose":
				result += "- Purpose/reason for the expense\n"
			}
		}

		result += "\nYou can provide information in natural language or structured format like:\n"
		result += "Date: 2023-10-15\nAmount: $50.00\nPurpose: Business lunch with client"
	} else {
		// Request is complete - process it
		requestID := reimbursement["request_id"].(string)
		requestIDs[requestID] = true

		result = fmt.Sprintf("✅ Reimbursement request processed successfully!\n\n"+
			"Request Details:\n"+
			"- Request ID: %s\n"+
			"- Date: %s\n"+
			"- Amount: %s\n"+
			"- Purpose: %s\n\n"+
			"Status: Approved\n"+
			"Processing Time: 2-3 business days\n"+
			"You will receive an email confirmation shortly.",
			requestID,
			reimbursement["date"],
			reimbursement["amount"],
			reimbursement["purpose"])
	}

	// Create response message
	responseMessage := protocol.NewMessage(
		protocol.MessageRoleAgent,
		[]protocol.Part{protocol.NewTextPart(result)},
	)

	// Create result with potential artifact for completed requests
	processingResult := &taskmanager.MessageProcessingResult{
		Result: &responseMessage,
	}

	// Add artifact for completed reimbursement requests
	if len(missing) == 0 {
		// Build task to get artifact support
		task, err := handle.BuildTask(nil, nil)
		if err == nil {
			// Create reimbursement details artifact
			reimbursementJSON, _ := json.Marshal(reimbursement)
			artifact := protocol.Artifact{
				ArtifactID:  fmt.Sprintf("reimb-%s", reimbursement["request_id"]),
				Name:        stringPtr("Reimbursement Details"),
				Description: stringPtr(fmt.Sprintf("Processed reimbursement request %s", reimbursement["request_id"])),
				Parts:       []protocol.Part{protocol.NewTextPart(string(reimbursementJSON))},
			}

			_ = handle.AddArtifact(&task.Task.ID, artifact, true, false)
		}
	}

	return processingResult, nil
}

// newReimbursementProcessor creates a new reimbursement processor
func newReimbursementProcessor() (*reimbursementProcessor, error) {
	return &reimbursementProcessor{}, nil
}

// extractReimbursementDetails attempts to extract date, amount, and purpose from the text
func extractReimbursementDetails(text string) (date, amount, purpose string) {
	// Initialize with empty values
	date = ""
	amount = ""
	purpose = ""

	// Use LLM or simple rules to try to extract information
	// This is a simplified version - a real implementation would use more sophisticated parsing

	// Look for date patterns (YYYY-MM-DD)
	words := strings.Fields(text)
	for _, word := range words {
		word = strings.Trim(word, ",.?!:;()[]{}\"'")

		// Try to match a date format
		if len(word) == 10 && strings.Count(word, "-") == 2 {
			date = word
		}

		// Try to match a currency amount (e.g., $50, 50.00)
		if strings.HasPrefix(word, "$") || strings.HasPrefix(word, "€") {
			amount = word
		} else if _, err := strconv.ParseFloat(strings.Trim(word, ",."), 64); err == nil {
			// If it's a number, it might be an amount
			amount = word
		}
	}

	// For purpose, we'd need more complex parsing, which is simplified here
	if strings.Contains(strings.ToLower(text), "for") {
		parts := strings.SplitN(text, "for", 2)
		if len(parts) > 1 {
			purpose = strings.TrimSpace(parts[1])
			// Truncate purpose if too long
			if len(purpose) > 50 {
				purpose = purpose[:50] + "..."
			}
		}
	}

	return
}

// extractFormData extracts form data from text if not in JSON format
func extractFormData(text string) map[string]interface{} {
	form := make(map[string]interface{})

	// Look for patterns like "field: value" or "field = value"
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Try different delimiters
		for _, delimiter := range []string{":", "="} {
			if parts := strings.SplitN(line, delimiter, 2); len(parts) == 2 {
				key := strings.ToLower(strings.TrimSpace(parts[0]))
				value := strings.TrimSpace(parts[1])

				// Map common field names to our expected fields
				switch {
				case strings.Contains(key, "request") && strings.Contains(key, "id"):
					form["request_id"] = value
				case strings.Contains(key, "date"):
					form["date"] = value
				case strings.Contains(key, "amount"):
					form["amount"] = value
				case strings.Contains(key, "purpose") || strings.Contains(key, "reason"):
					form["purpose"] = value
				}
			}
		}
	}

	return form
}

// validateForm validates the form has all required fields
func validateForm(form map[string]interface{}) []string {
	required := []string{"request_id", "date", "amount", "purpose"}
	missing := []string{}

	for _, field := range required {
		value, exists := form[field]
		if !exists {
			missing = append(missing, field)
			continue
		}

		// Also check if the value is empty
		if str, ok := value.(string); ok && strings.TrimSpace(str) == "" {
			missing = append(missing, field)
		}
	}

	return missing
}

// extractText extracts the text content from a message
func extractText(message protocol.Message) string {
	for _, part := range message.Parts {
		if textPart, ok := part.(*protocol.TextPart); ok {
			return textPart.Text
		}
	}
	return ""
}

// generateReferenceID generates a simple reference ID for demonstration
func generateReferenceID() string {
	return fmt.Sprintf("%d", len(requestIDs)+1)
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
		Name:        "Reimbursement Agent",
		Description: "An agent that processes employee reimbursement requests.",
		URL:         "http://localhost:8083",
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
				ID:          "reimbursement",
				Name:        "Process Reimbursements",
				Description: stringPtr("Creates and processes expense reimbursement requests."),
				Tags:        []string{"expense", "reimbursement", "finance"},
				Examples: []string{
					"I need to get reimbursed for my business lunch.",
					"Process my reimbursement for $50 for office supplies.",
					"Submit a reimbursement request for my travel expenses on 2023-10-15.",
				},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
		},
	}
}

func main() {
	// Parse command-line flags
	host := flag.String("host", "localhost", "Host to listen on")
	port := flag.Int("port", 8083, "Port to listen on for the reimbursement agent")
	flag.Parse()

	// Create the reimbursement processor
	processor, err := newReimbursementProcessor()
	if err != nil {
		log.Fatal("Failed to create reimbursement processor: %v", err)
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
		log.Info("Starting Reimbursement Agent server on %s\n", serverAddr)
		if err := a2aServer.Start(serverAddr); err != nil {
			log.Fatal("Server failed: %v", err)
		}
	}()

	// Wait for termination signal
	sig := <-sigChan
	log.Info("Received signal %v, shutting down...", sig)
}
