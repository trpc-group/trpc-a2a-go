// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package tests

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"trpc.group/trpc-go/trpc-a2a-go/client"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// stringPtr is a helper to get a pointer to a string.
func stringPtr(s string) *string {
	return &s
}

// boolPtr is a helper to get a pointer to a boolean.
func boolPtr(b bool) *bool {
	return &b
}

// intPtr is a helper to get a pointer to an int.
func intPtr(i int) *int {
	return &i
}

// testReverseString is a helper that reverses a string.
func testReverseString(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

// testStreamingProcessor implements taskmanager.TaskProcessor for streaming E2E tests.
// It reverses the input text and sends it back chunk by chunk via status updates
// and a final artifact.
type testStreamingProcessor struct{}

// Process implements taskmanager.TaskProcessor for streaming.
func (p *testStreamingProcessor) Process(
	ctx context.Context,
	taskID string,
	msg protocol.Message,
	handle taskmanager.TaskHandle,
) error {
	log.Printf("[testStreamingProcessor] Processing task %s", taskID)

	var inputText string
	for _, part := range msg.Parts {
		if textPart, ok := part.(protocol.TextPart); ok { // Check for value type
			inputText = textPart.Text
			break
		}
	}

	if inputText == "" {
		// If no text part found, try legacy TextPart check just in case
		for _, part := range msg.Parts {
			if textPart, ok := part.(protocol.TextPart); ok {
				log.Printf("WARNING: Found legacy TextPart (value) in message for task %s", taskID)
				inputText = textPart.Text
				break
			}
		}
		if inputText == "" {
			return fmt.Errorf("no text input found in message for task %s", taskID)
		}
	}

	reversedText := testReverseString(inputText)
	log.Printf("[testStreamingProcessor] Input: '%s', Reversed: '%s'", inputText, reversedText)

	// Send intermediate 'Working' status updates (chunked).
	chunkSize := 3
	for i := 0; i < len(reversedText); i += chunkSize {
		time.Sleep(20 * time.Millisecond) // Simulate work per chunk.
		end := i + chunkSize
		if end > len(reversedText) {
			end = len(reversedText)
		}
		chunk := reversedText[i:end]
		statusMsg := &protocol.Message{
			Role: protocol.MessageRoleAgent,
			Parts: []protocol.Part{
				protocol.NewTextPart(fmt.Sprintf("Processing chunk: %s", chunk)), // Returns TextPart
			},
		}
		if err := handle.UpdateStatus(protocol.TaskStateWorking, statusMsg); err != nil {
			log.Printf("[testStreamingProcessor] Error sending working status chunk: %v", err)
			return err // Propagate error if sending status fails.
		}
	}

	// Send the final artifact containing the full reversed text.
	finalArtifact := protocol.Artifact{
		Name:        stringPtr("Processed Text"),
		Description: stringPtr("The reversed input text."),
		Parts: []protocol.Part{
			protocol.NewTextPart(reversedText), // Returns TextPart
		},
		Index:     0,
		LastChunk: boolPtr(true),
	}
	if err := handle.AddArtifact(finalArtifact); err != nil {
		log.Printf("[testStreamingProcessor] Error sending artifact: %v", err)
		return err // Propagate error if sending artifact fails.
	}

	// Send final 'Completed' status.
	completionMsg := &protocol.Message{
		Role: protocol.MessageRoleAgent,
		Parts: []protocol.Part{
			protocol.NewTextPart(
				fmt.Sprintf("Task %s completed successfully. Result: %s", taskID, reversedText),
			), // Include the reversed text in the final message
		},
	}
	if err := handle.UpdateStatus(protocol.TaskStateCompleted, completionMsg); err != nil {
		log.Printf("[testStreamingProcessor] Error sending completed status: %v", err)
		return err // Propagate error if sending final status fails.
	}

	log.Printf("[testStreamingProcessor] Finished processing task %s", taskID)
	return nil // Success.
}

// --- Test Task Manager Definition ---

// testBasicTaskManager is a simple TaskManager for basic tests.
type testBasicTaskManager struct {
	// Embed MemoryTaskManager to get basic functionality.
	*taskmanager.MemoryTaskManager
	// Add specific fields if needed for testing.
}

// newTestBasicTaskManager creates an instance for testing.
func newTestBasicTaskManager(t *testing.T) *testBasicTaskManager {
	processor := &testStreamingProcessor{}
	memTm, err := taskmanager.NewMemoryTaskManager(processor)
	require.NoError(t, err, "Failed to create MemoryTaskManager for testBasicTaskManager")
	return &testBasicTaskManager{
		MemoryTaskManager: memTm, // Correctly assign the embedded field.
	}
}

// Helper to mimic unexported upsertTask for testing purposes.
// Needs access to baseTm's fields.
func (m *testBasicTaskManager) testUpsertTask(params protocol.SendTaskParams) *protocol.Task {
	// NOTE: This accesses fields of baseTm directly, which relies on them being accessible
	// (e.g., within the same logical package structure if tests were internal, or if fields were exported).
	// For this test setup, we assume accessibility for direct map manipulation.
	m.TasksMutex.Lock()
	defer m.TasksMutex.Unlock()

	task, exists := m.Tasks[params.ID]
	if !exists {
		task = protocol.NewTask(params.ID, params.SessionID)
		m.Tasks[params.ID] = task
		log.Printf("[Test TM Helper] Created new task %s via testUpsertTask", params.ID)
	} else {
		log.Printf("[Test TM Helper] Updating task %s via testUpsertTask", params.ID)
	}

	// Update metadata if provided.
	if params.Metadata != nil {
		if task.Metadata == nil {
			task.Metadata = make(map[string]interface{})
		}
		for k, v := range params.Metadata {
			task.Metadata[k] = v
		}
	}
	return task
}

// Helper to mimic unexported storeMessage for testing purposes.
// Needs access to baseTm's fields.
func (m *testBasicTaskManager) testStoreMessage(taskID string, message protocol.Message) {
	// NOTE: This accesses fields of baseTm directly.
	m.MessagesMutex.Lock()
	defer m.MessagesMutex.Unlock()

	if _, exists := m.Messages[taskID]; !exists {
		m.Messages[taskID] = make([]protocol.Message, 0, 1)
	}
	m.Messages[taskID] = append(m.Messages[taskID], message)
	log.Printf("[Test TM Helper] Stored message for task %s via testStoreMessage", taskID)
}

// --- TaskManager Interface Implementation for testBasicTaskManager ---

// OnSendTask delegates to the composed MemoryTaskManager.
func (m *testBasicTaskManager) OnSendTask(
	ctx context.Context,
	params protocol.SendTaskParams,
) (*protocol.Task, error) {
	log.Printf("[Test TM Wrapper] OnSendTask called for %s, delegating to base.", params.ID)
	// In a real compositional wrapper, you might add pre/post logic here.
	return m.MemoryTaskManager.OnSendTask(ctx, params)
}

// OnGetTask delegates to the composed MemoryTaskManager.
func (m *testBasicTaskManager) OnGetTask(
	ctx context.Context,
	params protocol.TaskQueryParams,
) (*protocol.Task, error) {
	log.Printf("[Test TM Wrapper] OnGetTask called for %s, delegating to base.", params.ID)
	return m.MemoryTaskManager.OnGetTask(ctx, params)
}

// OnCancelTask delegates to the composed MemoryTaskManager.
func (m *testBasicTaskManager) OnCancelTask(
	ctx context.Context,
	params protocol.TaskIDParams,
) (*protocol.Task, error) {
	log.Printf("[Test TM Wrapper] OnCancelTask called for %s, delegating to base.", params.ID)
	return m.MemoryTaskManager.OnCancelTask(ctx, params)
}

// OnSendTaskSubscribe delegates to the composed MemoryTaskManager.
// The baseTm's method now handles the goroutine and processor invocation correctly.
func (m *testBasicTaskManager) OnSendTaskSubscribe(
	ctx context.Context,
	params protocol.SendTaskParams,
) (<-chan protocol.TaskEvent, error) {
	log.Printf("[Test TM Wrapper] OnSendTaskSubscribe called for %s, delegating to base.", params.ID)
	// Call the embedded MemoryTaskManager's method directly
	return m.MemoryTaskManager.OnSendTaskSubscribe(ctx, params)
}

// ProcessTask is required by the TaskManager interface but should not be called directly
// in this compositional setup. The actual processing is done by the injected processor.
func (m *testBasicTaskManager) ProcessTask(
	ctx context.Context,
	taskID string,
	message protocol.Message,
) (*protocol.Task, error) {
	log.Printf("WARNING: testBasicTaskManager.ProcessTask called directly for %s. "+
		"This should not happen in the compositional setup.", taskID)
	// Return an error or a specific state to indicate this shouldn't be called.
	return nil, fmt.Errorf("testBasicTaskManager.ProcessTask should not be called directly")
}

// --- End-to-End Test Function ---

// --- Common Test Utilities ---

// testHelper contains common utilities and setup for e2e tests.
type testHelper struct {
	t           *testing.T
	taskManager taskmanager.TaskManager
	server      *server.A2AServer
	httpServer  *httptest.Server
	client      *client.A2AClient
	serverURL   string
	serverPort  int
}

// newTestHelper creates a new test helper with a running server and client.
func newTestHelper(t *testing.T, processor taskmanager.TaskProcessor) *testHelper {
	// Create task manager
	var tm taskmanager.TaskManager
	if processor != nil {
		memTm, err := taskmanager.NewMemoryTaskManager(processor)
		require.NoError(t, err)
		tm = memTm
	} else {
		tm = newTestBasicTaskManager(t)
	}

	// Create server
	port := getFreePort(t)
	agentCard := createDefaultTestAgentCard()
	a2aServer, err := server.NewA2AServer(agentCard, tm)
	require.NoError(t, err)

	// Start server in goroutine
	addr := fmt.Sprintf("localhost:%d", port)
	serverURL := fmt.Sprintf("http://%s", addr)

	go func() {
		if err := a2aServer.Start(addr); err != nil && err != http.ErrServerClosed {
			log.Printf("Server error: %v", err)
		}
	}()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	// Create client
	a2aClient, err := client.NewA2AClient(serverURL)
	require.NoError(t, err)

	return &testHelper{
		t:           t,
		taskManager: tm,
		server:      a2aServer,
		serverURL:   serverURL,
		client:      a2aClient,
		serverPort:  port,
	}
}

// cleanup stops the server and cleans up resources.
func (h *testHelper) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if h.server != nil {
		h.server.Stop(ctx)
	}
	if h.httpServer != nil {
		h.httpServer.Close()
	}
}

// getFreePort returns a free port from the OS.
func getFreePort(t *testing.T) int {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	require.NoError(t, err)

	l, err := net.ListenTCP("tcp", addr)
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// createDefaultTestAgentCard creates a default agent card for testing.
func createDefaultTestAgentCard() server.AgentCard {
	desc := "A test agent for E2E tests"
	return server.AgentCard{
		Name:        "Test Agent",
		Description: &desc,
		Capabilities: server.AgentCapabilities{
			Streaming:              true,
			StateTransitionHistory: true,
		},
		DefaultInputModes:  []string{string(protocol.PartTypeText)},
		DefaultOutputModes: []string{string(protocol.PartTypeText)},
	}
}

// sendTestMessage sends a test message and returns the task.
func (h *testHelper) sendTestMessage(taskID string, text string) (*protocol.Task, error) {
	return h.client.SendTasks(context.Background(), protocol.SendTaskParams{
		ID: taskID,
		Message: protocol.Message{
			Role: protocol.MessageRoleUser,
			Parts: []protocol.Part{
				protocol.NewTextPart(text),
			},
		},
	})
}

// isFinalState checks if the given task state is a terminal state.
func isFinalState(state protocol.TaskState) bool {
	return state == protocol.TaskStateCompleted ||
		state == protocol.TaskStateFailed ||
		state == protocol.TaskStateCanceled
}

// waitForTaskCompletion waits for a task to reach a final state.
func waitForTaskCompletion(ctx context.Context, client *client.A2AClient, taskID string) (*protocol.Task, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			task, err := client.GetTasks(ctx, protocol.TaskQueryParams{ID: taskID})
			if err != nil {
				return nil, err
			}

			if isFinalState(task.Status.State) {
				return task, nil
			}

			time.Sleep(50 * time.Millisecond)
		}
	}
}

// collectAllTaskEvents collects all events from a task event channel until it's closed.
func collectAllTaskEvents(eventChan <-chan protocol.TaskEvent) []protocol.TaskEvent {
	var events []protocol.TaskEvent
	timeout := time.After(3 * time.Second) // Safety timeout
	done := false
	for !done {
		select {
		case event, ok := <-eventChan:
			if !ok {
				done = true // Channel closed
				break
			}
			events = append(events, event)

			// Check if this is a final event (completed, failed, canceled)
			// If we received a final event, we can consider the collection complete
			// even if the channel isn't formally closed
			if event.IsFinal() {
				// Wait just a tiny bit more to see if there are any trailing events
				time.Sleep(50 * time.Millisecond)
				// Try to drain one more event non-blocking
				select {
				case lastEvent, ok := <-eventChan:
					if ok {
						events = append(events, lastEvent)
					}
				default:
					// No more events available
				}
				return events
			}
		case <-timeout:
			// If we timeout, just return whatever events we've collected so far
			return events
		}
	}
	return events
}

// getTextPartContent extracts text content from parts of a message.
func getTextPartContent(parts []protocol.Part) string {
	for _, part := range parts {
		if textPart, ok := part.(protocol.TextPart); ok {
			return textPart.Text
		}
	}
	return ""
}

// --- Test Functions ---

// TestE2E_BasicAgent_Streaming tests the streaming functionality.
func TestE2E_BasicAgent_Streaming(t *testing.T) {
	helper := newTestHelper(t, &testStreamingProcessor{})
	defer helper.cleanup()

	// Test code
	taskID := "test-streaming-1"
	inputText := "Hello world!"

	// Subscribe to task events
	eventChan, err := helper.client.StreamTask(
		context.Background(), protocol.SendTaskParams{
			ID: taskID,
			Message: protocol.Message{
				Role: protocol.MessageRoleUser,
				Parts: []protocol.Part{
					protocol.NewTextPart(inputText),
				},
			},
		})
	require.NoError(t, err)

	// Collect all events
	events := collectAllTaskEvents(eventChan)

	// Verify events
	require.NotEmpty(t, events, "Should have received events")

	// Check final state
	lastEvent := events[len(events)-1]
	require.Equal(t, protocol.TaskStateCompleted,
		lastEvent.(protocol.TaskStatusUpdateEvent).Status.State, "Task should be completed")

	// Verify task result
	task, err := helper.client.GetTasks(
		context.Background(),
		protocol.TaskQueryParams{ID: taskID},
	)
	require.NoError(t, err)
	require.Equal(t, protocol.TaskStateCompleted, task.Status.State)

	// Verify artifacts
	require.NotEmpty(t, task.Artifacts, "Task should have artifacts")
	require.Equal(t, 1, len(task.Artifacts), "Task should have 1 artifact")

	// Verify artifact content
	artifact := task.Artifacts[0]
	require.NotNil(t, artifact.Parts, "Artifact should have parts")
	require.Equal(t, 1, len(artifact.Parts), "Artifact should have 1 part")

	// Check the reversed text
	reversedText := getTextPartContent(artifact.Parts)
	expectedText := testReverseString(inputText)
	require.Equal(t, expectedText, reversedText, "Artifact should contain reversed text")
}

// ... existing code ...
