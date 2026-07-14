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

	"trpc.group/trpc-go/trpc-a2a-go/v2/client"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
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

// testExecutor implements taskmanager.MessageProcessor for streaming E2E tests.
// It reverses the input text and reports it back chunk by chunk via status
// updates and a final artifact; the framework serves both message/send and
// message/stream from the same event stream.
type testExecutor struct {
	// gate, when non-nil, blocks the processor after it emits the first Working
	// chunk until the channel is closed. Tests that must attach a second stream
	// (resubscribe) before the task terminates use it to remove the timing
	// race; nil keeps the default fast behavior.
	gate <-chan struct{}
}

var _ taskmanager.MessageProcessor = (*testExecutor)(nil)

// ProcessMessage implements taskmanager.MessageProcessor.
func (p *testExecutor) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	// Extract input text from the message
	inputText := getTextPartContent(ec.Message.Parts)
	if inputText == "" {
		return nil, fmt.Errorf("no text content found in message")
	}

	taskID := ec.TaskID
	gate := p.gate
	out := make(chan protocol.StreamEvent, 8)
	go func() {
		defer close(out)

		reversedText := testReverseString(inputText)
		log.Printf("[testExecutor] Input: '%s', Reversed: '%s'", inputText, reversedText)

		// Send intermediate 'Working' status updates (chunked)
		chunkSize := 3
		for i := 0; i < len(reversedText); i += chunkSize {
			time.Sleep(20 * time.Millisecond) // Simulate work per chunk
			end := i + chunkSize
			if end > len(reversedText) {
				end = len(reversedText)
			}
			chunk := reversedText[i:end]
			out <- &protocol.TaskStatusUpdateEvent{
				Status: protocol.TaskStatus{
					State: protocol.TaskStateWorking,
					Message: &protocol.Message{
						Role: protocol.MessageRoleAgent,
						Parts: []*protocol.Part{
							protocol.NewTextPart(fmt.Sprintf("Processing chunk: %s", chunk)),
						},
					},
				},
			}
			if i == 0 && gate != nil {
				// Hold after the FIRST chunk (which the test reads to learn the
				// task ID) so a resubscriber attaches while the task is still
				// non-terminal — removes the ~60ms race. Escape on ctx cancel so
				// a test that fails before releasing the gate can still tear the
				// processor down (cleanup's manager.Close cancels this ctx)
				// instead of leaking this goroutine.
				select {
				case <-gate:
				case <-ctx.Done():
					return
				}
			}
		}

		// Send the final artifact containing the full reversed text
		out <- &protocol.TaskArtifactUpdateEvent{
			Artifact: protocol.Artifact{
				Name:        stringPtr("Processed Text"),
				Description: stringPtr("The reversed input text."),
				Parts: []*protocol.Part{
					protocol.NewTextPart(reversedText),
				},
			},
			LastChunk: boolPtr(true),
		}

		// Send final 'Completed' status
		out <- &protocol.TaskStatusUpdateEvent{
			Status: protocol.TaskStatus{
				State: protocol.TaskStateCompleted,
				Message: &protocol.Message{
					Role: protocol.MessageRoleAgent,
					Parts: []*protocol.Part{
						protocol.NewTextPart(
							fmt.Sprintf("Task %s completed successfully. Result: %s", taskID, reversedText),
						),
					},
				},
			},
		}

		log.Printf("[testExecutor] Finished processing task %s", taskID)
	}()
	return out, nil
}

// testBasicTaskManager is a simple TaskManager for basic tests.
type testBasicTaskManager struct {
	*memory.TaskManager
}

// newTestBasicTaskManager creates an instance for testing.
func newTestBasicTaskManager(t *testing.T) *testBasicTaskManager {
	processor := &testExecutor{}
	memTm, err := memory.NewTaskManager(processor)
	require.NoError(t, err, "Failed to create TaskManager for testBasicTaskManager")
	return &testBasicTaskManager{
		TaskManager: memTm,
	}
}

// OnSendMessage delegates to the composed TaskManager.
func (m *testBasicTaskManager) OnSendMessage(
	ctx context.Context,
	params protocol.SendMessageParams,
) (*protocol.SendMessageResponse, error) {
	log.Printf("[Test TM Wrapper] OnSendMessage called for %s, delegating to base.", params.Message.MessageID)
	return m.TaskManager.OnSendMessage(ctx, params)
}

// OnSendMessageStream delegates to the composed TaskManager.
func (m *testBasicTaskManager) OnSendMessageStream(
	ctx context.Context,
	params protocol.SendMessageParams,
) (<-chan protocol.StreamResponse, error) {
	log.Printf("[Test TM Wrapper] OnSendMessageStream called for %s, delegating to base.", params.Message.MessageID)
	return m.TaskManager.OnSendMessageStream(ctx, params)
}

// OnResubscribe delegates to the composed TaskManager.
func (m *testBasicTaskManager) OnResubscribe(
	ctx context.Context,
	params protocol.TaskIDParams,
) (<-chan protocol.StreamResponse, error) {
	log.Printf("[Test TM Wrapper] OnResubscribe called for %s, delegating to base.", params.ID)
	return m.TaskManager.OnResubscribe(ctx, params)
}

// OnPushNotificationSet delegates to the composed TaskManager.
func (m *testBasicTaskManager) OnPushNotificationSet(
	ctx context.Context,
	params protocol.TaskPushNotificationConfig,
) (*protocol.TaskPushNotificationConfig, error) {
	log.Printf("[Test TM Wrapper] OnPushNotificationSet called for %s, delegating to base.", params.TaskID)
	return m.TaskManager.OnPushNotificationSet(ctx, params)
}

// OnPushNotificationGet delegates to the composed TaskManager.
func (m *testBasicTaskManager) OnPushNotificationGet(
	ctx context.Context,
	params protocol.GetTaskPushNotificationConfigParams,
) (*protocol.TaskPushNotificationConfig, error) {
	log.Printf("[Test TM Wrapper] OnPushNotificationGet called for %s, delegating to base.", params.TaskID)
	return m.TaskManager.OnPushNotificationGet(ctx, params)
}

// OnGetTask delegates to the composed TaskManager.
func (m *testBasicTaskManager) OnGetTask(
	ctx context.Context,
	params protocol.TaskQueryParams,
) (*protocol.Task, error) {
	log.Printf("[Test TM Wrapper] OnGetTask called for %s, delegating to base.", params.ID)
	return m.TaskManager.OnGetTask(ctx, params)
}

// OnCancelTask delegates to the composed TaskManager.
func (m *testBasicTaskManager) OnCancelTask(
	ctx context.Context,
	params protocol.TaskIDParams,
) (*protocol.Task, error) {
	log.Printf("[Test TM Wrapper] OnCancelTask called for %s, delegating to base.", params.ID)
	return m.TaskManager.OnCancelTask(ctx, params)
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
		memTm, err := memory.NewTaskManager(processor)
		require.NoError(t, err)
		tm = memTm
	} else {
		tm = newTestBasicTaskManager(t)
	}

	// Create server
	port := getFreePort(t)
	agentCard := createDefaultTestAgentCard()
	a2aServer, err := server.NewA2AServer(tm, server.WithAgentCard(agentCard))
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
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
	}
}

// collectAllStreamingEvents collects all events from a streaming response channel until it's closed.
func collectAllStreamingEvents(eventChan <-chan protocol.StreamResponse) []protocol.StreamResponse {
	var events []protocol.StreamResponse
	timeout := time.After(3 * time.Second)
	done := false
	for !done {
		select {
		case event, ok := <-eventChan:
			if !ok {
				done = true
				break
			}
			events = append(events, event)

			if event.GetStatusUpdate() != nil && event.GetStatusUpdate().IsFinal() {
				time.Sleep(50 * time.Millisecond)
				select {
				case lastEvent, ok := <-eventChan:
					if ok {
						events = append(events, lastEvent)
					}
				default:
				}
				return events
			}
		case <-timeout:
			return events
		}
	}
	return events
}

// getTextPartContent extracts text content from parts of a message.
func getTextPartContent(parts []*protocol.Part) string {
	for _, part := range parts {
		if t := part.TextContent(); t != "" {
			return t
		}
	}
	return ""
}

// --- Test Functions ---

// TestE2E_MessageAPI_Streaming tests the streaming functionality using the new message API.
func TestE2E_MessageAPI_Streaming(t *testing.T) {
	helper := newTestHelper(t, &testExecutor{})
	defer helper.cleanup()

	// Test data
	inputText := "Hello world!"

	// Generate the context ID; the server assigns the task ID (v1.0: a taskId
	// on a message refers to an existing task).
	contextID := protocol.GenerateContextID()

	// Create message using the NewMessageWithContext constructor
	message := protocol.NewMessageWithContext(
		protocol.MessageRoleUser,
		[]*protocol.Part{
			protocol.NewTextPart(inputText),
		},
		nil,
		&contextID,
	)

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
	// Gate the executor after its first chunk so the resubscribe reliably
	// attaches while the task is still non-terminal (no timing race).
	gate := make(chan struct{})
	helper := newTestHelper(t, &testExecutor{gate: gate})
	defer helper.cleanup()

	// Test data
	inputText := "Hello world!"

	// Generate the context ID; the server assigns the task ID (v1.0: a taskId
	// on a message refers to an existing task).
	contextID := protocol.GenerateContextID()

	// Create message using the NewMessageWithContext constructor
	message := protocol.NewMessageWithContext(
		protocol.MessageRoleUser,
		[]*protocol.Part{
			protocol.NewTextPart(inputText),
		},
		nil,
		&contextID,
	)

	// Send streaming message using the new API; read only the first event to
	// learn the server-assigned task ID (the rest keeps flowing to this pipe's
	// buffer while we resubscribe on a second stream).
	firstChan, err := helper.client.StreamMessage(
		context.Background(),
		protocol.SendMessageParams{
			Message: message,
		},
	)
	require.NoError(t, err)

	first, ok := <-firstChan
	require.True(t, ok, "Should have received a first stream event")
	firstStatus := first.GetStatusUpdate()
	require.NotNil(t, firstStatus, "First event should be a status update")
	taskID := firstStatus.TaskID
	require.NotEmpty(t, taskID, "Server should have assigned a task ID")

	// Resubscribe to streaming message events using the new API
	eventChan, err := helper.client.ResubscribeTask(
		context.Background(),
		protocol.TaskIDParams{
			ID: taskID,
		},
	)
	require.NoError(t, err)

	// The resubscriber is now attached: let the executor finish.
	close(gate)

	// Collect all events
	events := collectAllStreamingEvents(eventChan)
	checkStreamingEvents(t, events)
}

func checkStreamingEvents(t *testing.T, events []protocol.StreamResponse) {
	require.NotEmpty(t, events, "Should have received events")

	hasWorkingStatus := false
	hasArtifact := false
	hasCompletedStatus := false

	for _, event := range events {
		if su := event.GetStatusUpdate(); su != nil {
			if su.Status.State == protocol.TaskStateWorking {
				hasWorkingStatus = true
				require.NotNil(t, su.Status.Message, "Working status should have a message")
				require.NotEmpty(t, su.Status.Message.Parts, "Working status message should have parts")
				text := su.Status.Message.Parts[0].TextContent()
				require.Contains(t, text, "Processing chunk:", "Working status should contain processing info")
			} else if su.Status.State == protocol.TaskStateCompleted {
				hasCompletedStatus = true
				require.NotNil(t, su.Status.Message, "Completed status should have a message")
				require.NotEmpty(t, su.Status.Message.Parts, "Completed status message should have parts")
				text := su.Status.Message.Parts[0].TextContent()
				require.Contains(t, text, "completed successfully", "Completed status should contain success info")
				require.Contains(t, text, "!dlrow olleH", "Completed status should contain reversed text")
			}
		}
		if au := event.GetArtifactUpdate(); au != nil {
			hasArtifact = true
			require.NotNil(t, au.Artifact.Name, "Artifact should have a name")
			require.Equal(t, "Processed Text", *au.Artifact.Name, "Artifact name should match")
			require.NotEmpty(t, au.Artifact.Parts, "Artifact should have parts")
			text := au.Artifact.Parts[0].TextContent()
			require.Equal(t, "!dlrow olleH", text, "Artifact should contain reversed text")
		}
	}

	require.True(t, hasWorkingStatus, "Should have received working status updates")
	require.True(t, hasArtifact, "Should have received artifact update")
	require.True(t, hasCompletedStatus, "Should have received completed status")

	t.Logf("Successfully received %d events", len(events))
}

// TestE2E_MessageAPI_NonStreaming tests the non-streaming functionality using the new message API.
func TestE2E_MessageAPI_NonStreaming(t *testing.T) {
	helper := newTestHelper(t, &testExecutor{})
	defer helper.cleanup()

	// Test data
	inputText := "Hello world!"

	// Generate the context ID; the server assigns the task ID (v1.0: a taskId
	// on a message refers to an existing task).
	contextID := protocol.GenerateContextID()

	// Create message using the NewMessageWithContext constructor
	message := protocol.NewMessageWithContext(
		protocol.MessageRoleUser,
		[]*protocol.Part{
			protocol.NewTextPart(inputText),
		},
		nil,
		&contextID,
	)

	// Send message using the new non-streaming API
	result, err := helper.client.SendMessage(
		context.Background(),
		protocol.SendMessageParams{
			Message: message,
		},
	)
	require.NoError(t, err)

	// A blocking message/send returns only after the round ends: the result is
	// already the terminal task snapshot (§3.1), no wait needed.
	require.NotNil(t, result.GetTask(), "Result should contain a task")
	task := result.GetTask()
	require.Equal(t, protocol.TaskStateCompleted, task.Status.State,
		"Blocking send must return the terminal task")

	// GetTasks must agree with the returned snapshot (store consistency).
	finalTask, err := helper.client.GetTasks(
		context.Background(),
		protocol.TaskQueryParams{ID: task.ID},
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

// blockingStreamExecutor emits many events on a small blocking-send pipe, so a
// vanished stream consumer would wedge the drain engine unless the server
// keeps draining the abandoned pipe to closure.
type blockingStreamExecutor struct{}

func (p *blockingStreamExecutor) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	out := make(chan protocol.StreamEvent, 1)
	go func() {
		defer close(out)
		for i := 0; i < 20; i++ {
			out <- &protocol.TaskStatusUpdateEvent{
				Status: protocol.TaskStatus{
					State: protocol.TaskStateWorking,
					Message: &protocol.Message{
						Role:  protocol.MessageRoleAgent,
						Parts: []*protocol.Part{protocol.NewTextPart(fmt.Sprintf("chunk %d", i))},
					},
				},
			}
			time.Sleep(5 * time.Millisecond)
		}
		out <- &protocol.TaskStatusUpdateEvent{
			Status: protocol.TaskStatus{State: protocol.TaskStateCompleted},
		}
	}()
	return out, nil
}

// TestE2E_StreamDisconnect_BlockingSendDoesNotWedge proves the server drains an
// abandoned SSE pipe to closure: with a blocking-send manager, a client that
// disconnects mid-stream must not wedge the execution — the task still reaches
// COMPLETED and is retrievable.
func TestE2E_StreamDisconnect_BlockingSendDoesNotWedge(t *testing.T) {
	tm, err := memory.NewTaskManager(
		&blockingStreamExecutor{},
		memory.WithTaskSubscriberBlockingSend(true),
		memory.WithTaskSubscriberBufferSize(1),
	)
	require.NoError(t, err)

	port := getFreePort(t)
	addr := fmt.Sprintf("localhost:%d", port)
	serverURL := fmt.Sprintf("http://%s", addr)
	a2aServer, err := server.NewA2AServer(tm, server.WithAgentCard(createDefaultTestAgentCard()))
	require.NoError(t, err)
	go func() {
		if err := a2aServer.Start(addr); err != nil && err != http.ErrServerClosed {
			log.Printf("Server error: %v", err)
		}
	}()
	time.Sleep(100 * time.Millisecond)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		a2aServer.Stop(ctx)
	}()

	a2aClient, err := client.NewA2AClient(serverURL)
	require.NoError(t, err)

	// Open the stream with a cancelable context, read the first event to learn
	// the task ID, then disconnect by canceling — the pipe is abandoned mid-run.
	streamCtx, cancelStream := context.WithCancel(context.Background())
	eventChan, err := a2aClient.StreamMessage(streamCtx, protocol.SendMessageParams{
		Message: protocol.NewMessage(
			protocol.MessageRoleUser,
			[]*protocol.Part{protocol.NewTextPart("go")},
		),
	})
	require.NoError(t, err)

	first, ok := <-eventChan
	require.True(t, ok, "should receive a first event")
	taskID := first.GetStatusUpdate().TaskID
	require.NotEmpty(t, taskID)
	cancelStream() // client disconnects here; buffer(1) will fill server-side

	// The engine must finish despite the vanished consumer: poll until COMPLETED.
	deadline := time.Now().Add(3 * time.Second)
	for {
		task, err := a2aClient.GetTasks(context.Background(), protocol.TaskQueryParams{ID: taskID})
		if err == nil && task.Status.State == protocol.TaskStateCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task did not complete after disconnect (engine wedged): err=%v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
