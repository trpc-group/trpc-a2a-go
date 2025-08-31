// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
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
	v1 "trpc.group/trpc-go/trpc-a2a-go/protocol/a2apb"
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

// testReverseString is a helper that reverses a string.
func testReverseString(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

// testProcessor implements taskmanager.MessageProcessor for streaming E2E tests.
// It reverses the input text and sends it back chunk by chunk via status updates
// and a final artifact.
type testProcessor struct{}

var _ taskmanager.MessageProcessor = (*testProcessor)(nil)

// ProcessMessage implements taskmanager.MessageProcessor for streaming.
func (p *testProcessor) ProcessMessage(
	ctx context.Context,
	message protocol.Message,
	options taskmanager.ProcessOptions,
	handle taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
	// Extract input text from the message
	inputText := getTextPartContent(message.Message.Content)
	if inputText == "" {
		return nil, fmt.Errorf("no text content found in message")
	}

	// Create a task
	taskID, err := handle.BuildTask(&message.TaskId, &message.ContextId)
	if err != nil {
		return nil, fmt.Errorf("failed to build task: %w", err)
	}

	if options.Streaming {
		// For streaming requests, process in background and return StreamingEvents
		subscriber, err := handle.SubscribeTask(stringPtr(taskID))
		if err != nil {
			return nil, fmt.Errorf("failed to subscribe to task: %w", err)
		}

		// Process task in background
		go func() {
			if err := p.processTask(taskID, message, inputText, subscriber, handle); err != nil {
				log.Printf("[testStreamingProcessor] Error processing task: %v", err)
			}
		}()

		return &taskmanager.MessageProcessingResult{
			StreamingEvents: subscriber,
		}, nil
	}
	// For non-streaming requests, process synchronously and return Result
	// Process the task synchronously without auto-cleanup
	if err := p.processTask(taskID, message, inputText, nil, handle); err != nil {
		return nil, fmt.Errorf("failed to process task: %w", err)
	}

	// Get the final task state
	finalTask, err := handle.GetTask(stringPtr(taskID))
	if err != nil {
		return nil, fmt.Errorf("failed to get final task: %w", err)
	}

	return &taskmanager.MessageProcessingResult{
		Result: finalTask.Task(),
	}, nil
}

func (p *testProcessor) processTask(
	taskID string,
	message protocol.Message,
	inputText string,
	subscriber taskmanager.TaskSubscriber,
	handle taskmanager.TaskHandler,
) error {
	reversedText := testReverseString(inputText)
	log.Printf("[testStreamingProcessor] Input: '%s', Reversed: '%s'", inputText, reversedText)

	// Send intermediate 'Working' status updates (chunked)
	chunkSize := 3
	for i := 0; i < len(reversedText); i += chunkSize {
		time.Sleep(20 * time.Millisecond) // Simulate work per chunk
		end := i + chunkSize
		if end > len(reversedText) {
			end = len(reversedText)
		}
		chunk := reversedText[i:end]
		statusMsg := &protocol.Message{
			Message: &v1.Message{
				Role: v1.Role_ROLE_AGENT,
				Content: []*v1.Part{
					{
						Part: &v1.Part_Text{
							Text: fmt.Sprintf("Processing chunk: %s", chunk),
						},
					},
				},
			},
		}

		// Will notify the subscriber automatically
		if err := handle.UpdateTaskState(stringPtr(taskID), protocol.TaskStateWorking, statusMsg); err != nil {
			log.Printf("[testStreamingProcessor] Error sending working status chunk: %v", err)
			return err
		}
	}

	// Send the final artifact containing the full reversed text
	finalArtifact := protocol.Artifact{
		Artifact: &v1.Artifact{
			Name:        "Processed Text",
			Description: "The reversed input text.",
			Parts: []*v1.Part{
				{
					Part: &v1.Part_Text{
						Text: reversedText,
					},
				},
			},
		},
	}

	if err := handle.AddArtifact(stringPtr(taskID), finalArtifact, true, false); err != nil {
		log.Printf("[testStreamingProcessor] Error sending artifact: %v", err)
		return err
	}

	// Send final 'Completed' status
	completionMsg := &protocol.Message{
		Message: &v1.Message{
			Role: v1.Role_ROLE_AGENT,
			Content: []*v1.Part{
				{
					Part: &v1.Part_Text{
						Text: fmt.Sprintf("Task %s completed successfully. Result: %s", taskID, reversedText),
					},
				},
			},
		},
	}

	if err := handle.UpdateTaskState(stringPtr(taskID), protocol.TaskStateCompleted, completionMsg); err != nil {
		log.Printf("[testStreamingProcessor] Error sending completed status: %v", err)
		return err
	}

	log.Printf("[testStreamingProcessor] Finished processing task %s", taskID)
	return nil
}

// testBasicTaskManager is a simple TaskManager for basic tests.
type testBasicTaskManager struct {
	*taskmanager.MemoryTaskManager
}

// newTestBasicTaskManager creates an instance for testing.
func newTestBasicTaskManager(t *testing.T) *testBasicTaskManager {
	processor := &testProcessor{}
	memTm, err := taskmanager.NewMemoryTaskManager(processor)
	require.NoError(t, err, "Failed to create MemoryTaskManager for testBasicTaskManager")
	return &testBasicTaskManager{
		MemoryTaskManager: memTm,
	}
}

// OnSendMessage delegates to the composed MemoryTaskManager.
func (m *testBasicTaskManager) OnSendMessage(
	ctx context.Context,
	params protocol.SendMessageParams,
) (*protocol.MessageResult, error) {
	log.Printf("[Test TM Wrapper] OnSendMessage called for %s, delegating to base.", params.Message.MessageId)
	return m.MemoryTaskManager.OnSendMessage(ctx, params)
}

// OnSendMessageStream delegates to the composed MemoryTaskManager.
func (m *testBasicTaskManager) OnSendMessageStream(
	ctx context.Context,
	params protocol.SendMessageParams,
) (<-chan protocol.StreamingMessageEvent, error) {
	log.Printf("[Test TM Wrapper] OnSendMessageStream called for %s, delegating to base.", params.Message.MessageId)
	return m.MemoryTaskManager.OnSendMessageStream(ctx, params)
}

// OnResubscribe delegates to the composed MemoryTaskManager.
func (m *testBasicTaskManager) OnResubscribe(
	ctx context.Context,
	params protocol.TaskIDParams,
) (<-chan protocol.StreamingMessageEvent, error) {
	log.Printf("[Test TM Wrapper] OnResubscribe called for %s, delegating to base.", params.ID)
	return m.MemoryTaskManager.OnResubscribe(ctx, params)
}

// OnPushNotificationSet delegates to the composed MemoryTaskManager.
func (m *testBasicTaskManager) OnPushNotificationSet(
	ctx context.Context,
	params *protocol.TaskPushNotificationConfig,
) (*protocol.TaskPushNotificationConfig, error) {
	log.Printf("[Test TM Wrapper] OnPushNotificationSet called for %s, delegating to base.", params.TaskID)
	return m.MemoryTaskManager.OnPushNotificationSet(ctx, params)
}

// OnPushNotificationGet delegates to the composed MemoryTaskManager.
func (m *testBasicTaskManager) OnPushNotificationGet(
	ctx context.Context,
	params protocol.TaskIDParams,
) (*protocol.TaskPushNotificationConfig, error) {
	log.Printf("[Test TM Wrapper] OnPushNotificationGet called for %s, delegating to base.", params.ID)
	return m.MemoryTaskManager.OnPushNotificationGet(ctx, params)
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
func newTestHelper(t *testing.T, processor taskmanager.MessageProcessor) *testHelper {
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
		Description: desc,
		Capabilities: server.AgentCapabilities{
			Streaming:              boolPtr(true),
			StateTransitionHistory: boolPtr(true),
		},
		DefaultInputModes:  []string{string(protocol.KindText)},
		DefaultOutputModes: []string{string(protocol.KindText)},
	}
}

// collectAllStreamingEvents collects all events from a streaming message event channel until it's closed.
func collectAllStreamingEvents(eventChan <-chan protocol.StreamingMessageEvent) []protocol.StreamingMessageEvent {
	var events []protocol.StreamingMessageEvent
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

			// Check if this is a final event
			if result, ok := event.Result.(*protocol.TaskStatusUpdateEvent); ok {
				if result.IsFinal() {
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
			}
		case <-timeout:
			// If we timeout, just return whatever events we've collected so far
			return events
		}
	}
	return events
}

// getTextPartContent extracts text content from parts of a message.
func getTextPartContent(parts []*v1.Part) string {
	for _, part := range parts {
		if textPart, ok := part.Part.(*v1.Part_Text); ok {
			return textPart.Text
		}
	}
	return ""
}

// --- Test Functions ---

// TestE2E_MessageAPI_Streaming tests the streaming functionality using the new message API.
func TestE2E_MessageAPI_Streaming(t *testing.T) {
	helper := newTestHelper(t, &testProcessor{})
	defer helper.cleanup()

	// Test data
	inputText := "Hello world!"

	// Generate context ID and task ID
	contextID := protocol.GenerateContextID()
	taskID := protocol.GenerateMessageID()

	// Create message using v1 protobuf structures
	message := protocol.Message{
		Message: &v1.Message{
			MessageId: protocol.GenerateMessageID(),
			TaskId:    taskID,
			ContextId: contextID,
			Role:      v1.Role_ROLE_USER,
			Content: []*v1.Part{
				{
					Part: &v1.Part_Text{
						Text: inputText,
					},
				},
			},
		},
	}

	// Subscribe to streaming message events using the new API
	eventChan, err := helper.client.StreamMessage(
		context.Background(),
		protocol.SendMessageParams{
			Message: message,
		},
	)
	require.NoError(t, err)

	// Collect all events
	events := collectAllStreamingEvents(eventChan)
	checkStreamingEvents(t, events)
}

// TestE2E_MessageAPI_Resubscribe tests the streaming functionality with interrupted using the new message API.
func TestE2E_MessageAPI_Resubscribe(t *testing.T) {
	helper := newTestHelper(t, &testProcessor{})
	defer helper.cleanup()

	// Test data
	inputText := "Hello world!"

	// Generate context ID and task ID
	contextID := protocol.GenerateContextID()
	taskID := protocol.GenerateTaskID()

	// Create message using the NewMessageWithContext constructor
	message := protocol.NewMessageWithContext(
		protocol.MessageRoleUser,
		[]*v1.Part{
			{Part: protocol.NewTextPart(inputText)},
		},
		&taskID,
		&contextID,
	)

	// Send streaming message using the new API, without subscribing events
	_, err := helper.client.StreamMessage(
		context.Background(),
		protocol.SendMessageParams{
			Message: message,
		},
	)
	require.NoError(t, err)

	// Resubscribe to streaming message events using the new API
	eventChan, err := helper.client.ResubscribeTask(
		context.Background(),
		protocol.TaskIDParams{
			ID: taskID,
		},
	)
	require.NoError(t, err)

	// Collect all events
	events := collectAllStreamingEvents(eventChan)
	checkStreamingEvents(t, events)
}

func checkStreamingEvents(t *testing.T, events []protocol.StreamingMessageEvent) {
	// Verify we received events
	require.NotEmpty(t, events, "Should have received events")

	// Verify the events we received
	hasWorkingStatus := false
	hasArtifact := false
	hasCompletedStatus := false

	for _, event := range events {
		switch result := event.Result.(type) {
		case *protocol.TaskStatusUpdateEvent:
			if result.Status.State == protocol.TaskStateWorking {
				hasWorkingStatus = true
				require.NotNil(t, result.Status.Update, "Working status should have a message")
				require.NotEmpty(t, result.Status.Update.Content, "Working status message should have parts")
				textPart, ok := result.Status.Update.Content[0].Part.(*v1.Part_Text)
				require.True(t, ok, "Working status message should have text part")
				require.Contains(t, textPart.Text, "Processing chunk:", "Working status should contain processing info")
			} else if result.Status.State == protocol.TaskStateCompleted {
				hasCompletedStatus = true
				require.NotNil(t, result.Status.Update, "Completed status should have a message")
				require.NotEmpty(t, result.Status.Update.Content, "Completed status message should have parts")
				textPart, ok := result.Status.Update.Content[0].Part.(*v1.Part_Text)
				require.True(t, ok, "Completed status message should have text part")
				require.Contains(t, textPart.Text, "completed successfully", "Completed status should contain success info")
				require.Contains(t, textPart.Text, "!dlrow olleH", "Completed status should contain reversed text")
			}
		case *protocol.TaskArtifactUpdateEvent:
			hasArtifact = true
			require.NotNil(t, result.Artifact.Name, "Artifact should have a name")
			require.Equal(t, "Processed Text", result.Artifact.Name, "Artifact name should match")
			require.NotEmpty(t, result.Artifact.Parts, "Artifact should have parts")
			textPart, ok := result.Artifact.Parts[0].Part.(*v1.Part_Text)
			require.True(t, ok, "Artifact should have text part")
			require.Equal(t, "!dlrow olleH", textPart.Text, "Artifact should contain reversed text")
		}
	}

	// Verify we got all expected event types
	require.True(t, hasWorkingStatus, "Should have received working status updates")
	require.True(t, hasArtifact, "Should have received artifact update")
	require.True(t, hasCompletedStatus, "Should have received completed status")

	t.Logf("Successfully received %d events", len(events))
}

// TestE2E_MessageAPI_NonStreaming tests the non-streaming functionality using the new message API.
func TestE2E_MessageAPI_NonStreaming(t *testing.T) {
	helper := newTestHelper(t, &testProcessor{})
	defer helper.cleanup()

	// Test data
	inputText := "Hello world!"

	// Generate context ID and task ID
	contextID := protocol.GenerateContextID()
	taskID := protocol.GenerateMessageID()

	// Create message using v1 protobuf structures
	message := protocol.Message{
		Message: &v1.Message{
			MessageId: protocol.GenerateMessageID(),
			TaskId:    taskID,
			ContextId: contextID,
			Role:      v1.Role_ROLE_USER,
			Content: []*v1.Part{
				{
					Part: &v1.Part_Text{
						Text: inputText,
					},
				},
			},
		},
	}

	// Send message using the new non-streaming API
	result, err := helper.client.SendMessage(
		context.Background(),
		protocol.SendMessageParams{
			Message: message,
		},
	)
	require.NoError(t, err)

	// Verify the result contains a task
	task, ok := result.Result.(*protocol.Task)
	require.True(t, ok, "Result should contain a task")
	require.NotNil(t, task, "Task should not be nil")

	// Wait a bit for the task to complete
	time.Sleep(500 * time.Millisecond)

	// Get the final task state
	finalTask, err := helper.client.GetTasks(
		context.Background(),
		protocol.TaskQueryParams{ID: task.Id},
	)
	require.NoError(t, err)
	require.Equal(t, protocol.TaskStateCompleted, finalTask.Status.State)

	// Verify artifacts
	require.NotEmpty(t, finalTask.Artifacts, "Task should have artifacts")
	require.Equal(t, 1, len(finalTask.Artifacts), "Task should have 1 artifact")

	// Verify artifact content
	artifact := finalTask.Artifacts[0]
	require.NotNil(t, artifact.Parts, "Artifact should have parts")
	require.Equal(t, 1, len(artifact.Parts), "Artifact should have 1 part")

	// Check the reversed text
	reversedText := getTextPartContent(artifact.Parts)
	expectedText := testReverseString(inputText)
	require.Equal(t, expectedText, reversedText, "Artifact should contain reversed text")
}
