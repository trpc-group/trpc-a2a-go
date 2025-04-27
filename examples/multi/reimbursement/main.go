package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/googleai"
	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// Store request IDs for demonstration purposes
var requestIDs = make(map[string]bool)

// reimbursementProcessor implements the taskmanager.TaskProcessor interface
type reimbursementProcessor struct {
	llm llms.Model
}

// newReimbursementProcessor creates a new reimbursement processor with LangChain
func newReimbursementProcessor() (*reimbursementProcessor, error) {
	// Initialize Google Gemini model
	llm, err := googleai.New(
		context.Background(),
		googleai.WithAPIKey(getAPIKey()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Gemini model: %w", err)
	}

	return &reimbursementProcessor{
		llm: llm,
	}, nil
}

func getAPIKey() string {
	return os.Getenv("GOOGLE_API_KEY")
}

// Process implements the taskmanager.TaskProcessor interface
func (p *reimbursementProcessor) Process(
	ctx context.Context,
	taskID string,
	message protocol.Message,
	handle taskmanager.TaskHandle,
) error {
	// Extract text from the incoming message
	query := extractText(message)
	if query == "" {
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

	log.Info("Processing reimbursement task %s with query: %s", taskID, query)

	// Check if this is a form submission
	if strings.Contains(query, "request_id") && strings.Contains(query, "date") &&
		strings.Contains(query, "amount") && strings.Contains(query, "purpose") {
		return p.handleFormSubmission(ctx, taskID, query, handle)
	}

	// Otherwise, this is a new request - create a form
	return p.handleNewRequest(ctx, taskID, query, handle)
}

// handleNewRequest processes a new reimbursement request by creating a form
func (p *reimbursementProcessor) handleNewRequest(
	ctx context.Context,
	taskID string,
	query string,
	handle taskmanager.TaskHandle,
) error {
	// Try to extract date, amount, and purpose from the query
	date, amount, purpose := extractReimbursementDetails(query)

	// Generate a random request ID
	requestID := fmt.Sprintf("request_id_%d", rand.Intn(9000000)+1000000)
	requestIDs[requestID] = true

	// Create a form request
	formRequest := map[string]interface{}{
		"request_id": requestID,
		"date":       date,
		"amount":     amount,
		"purpose":    purpose,
	}

	// Create form response
	formDict := map[string]interface{}{
		"type": "form",
		"form": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"date": map[string]interface{}{
					"type":        "string",
					"format":      "date",
					"description": "Date of expense",
					"title":       "Date",
				},
				"amount": map[string]interface{}{
					"type":        "string",
					"format":      "number",
					"description": "Amount of expense",
					"title":       "Amount",
				},
				"purpose": map[string]interface{}{
					"type":        "string",
					"description": "Purpose of expense",
					"title":       "Purpose",
				},
				"request_id": map[string]interface{}{
					"type":        "string",
					"description": "Request id",
					"title":       "Request ID",
				},
			},
			"required": []string{"request_id", "date", "amount", "purpose"},
		},
		"form_data":    formRequest,
		"instructions": "Please fill out this reimbursement request form with all required information.",
	}

	// Convert to JSON
	formJSON, err := json.MarshalIndent(formDict, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to create form JSON: %w", err)
	}

	// Create response message
	responseMessage := protocol.NewMessage(
		protocol.MessageRoleAgent,
		[]protocol.Part{protocol.NewTextPart(string(formJSON))},
	)

	// Update task status to in-progress with the form
	if err := handle.UpdateStatus(protocol.TaskStateWorking, &responseMessage); err != nil {
		return fmt.Errorf("failed to update task status: %w", err)
	}

	return nil
}

// handleFormSubmission processes a submitted reimbursement form
func (p *reimbursementProcessor) handleFormSubmission(
	ctx context.Context,
	taskID string,
	formData string,
	handle taskmanager.TaskHandle,
) error {
	// Try to parse the form data
	var parsedForm map[string]interface{}
	if err := json.Unmarshal([]byte(formData), &parsedForm); err != nil {
		// If can't parse as JSON, try to extract form data from the text
		parsedForm = extractFormData(formData)
	}

	// Validate the form
	missingFields := validateForm(parsedForm)
	if len(missingFields) > 0 {
		// Form is incomplete - ask for the missing fields
		errorMsg := fmt.Sprintf(
			"Your reimbursement request is missing required information: %s. Please provide all required information.",
			strings.Join(missingFields, ", "),
		)
		errorMessage := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]protocol.Part{protocol.NewTextPart(errorMsg)},
		)
		return handle.UpdateStatus(protocol.TaskStateWorking, &errorMessage)
	}

	// Check if request ID is valid
	requestID, _ := parsedForm["request_id"].(string)
	if !requestIDs[requestID] {
		errorMsg := fmt.Sprintf("Error: Invalid request_id: %s", requestID)
		errorMessage := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]protocol.Part{protocol.NewTextPart(errorMsg)},
		)
		return handle.UpdateStatus(protocol.TaskStateFailed, &errorMessage)
	}

	// Process the reimbursement
	amount, _ := parsedForm["amount"].(string)
	purpose, _ := parsedForm["purpose"].(string)
	date, _ := parsedForm["date"].(string)

	// Generate response
	response := fmt.Sprintf(
		"Your reimbursement request has been approved!\n\n"+
			"Request ID: %s\n"+
			"Date: %s\n"+
			"Amount: %s\n"+
			"Purpose: %s\n\n"+
			"Status: approved",
		requestID, date, amount, purpose,
	)

	// Mark the task as completed
	responseMessage := protocol.NewMessage(
		protocol.MessageRoleAgent,
		[]protocol.Part{protocol.NewTextPart(response)},
	)

	if err := handle.UpdateStatus(protocol.TaskStateCompleted, &responseMessage); err != nil {
		return fmt.Errorf("failed to update task status: %w", err)
	}

	// Add the reimbursement data as an artifact
	artifact := protocol.Artifact{
		Name:        stringPtr("Reimbursement Request"),
		Description: stringPtr(fmt.Sprintf("Reimbursement request for %s", amount)),
		Index:       0,
		Parts:       []protocol.Part{protocol.NewTextPart(response)},
		LastChunk:   boolPtr(true),
	}

	if err := handle.AddArtifact(artifact); err != nil {
		log.Error("Error adding artifact for task %s: %v", taskID, err)
	}

	return nil
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
		Name:        "Reimbursement Agent",
		Description: stringPtr("An agent that processes employee reimbursement requests."),
		URL:         "http://localhost:8083",
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
				ID:          "reimbursement",
				Name:        "Process Reimbursements",
				Description: stringPtr("Creates and processes expense reimbursement requests."),
				Examples: []string{
					"I need to get reimbursed for my business lunch.",
					"Process my reimbursement for $50 for office supplies.",
					"Submit a reimbursement request for my travel expenses on 2023-10-15.",
				},
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
