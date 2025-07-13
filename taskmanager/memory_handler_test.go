// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package taskmanager

import (
	"context"
	"reflect"
	"testing"

	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	v1 "trpc.group/trpc-go/trpc-a2a-go/protocol/a2apb"
)

// setupTestHandler creates a test handler for use in tests
func setupTestHandler(t *testing.T) (*memoryTaskHandler, *MemoryTaskManager) {
	processor := &MockMessageProcessor{}
	manager, err := NewMemoryTaskManager(processor)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	ctx := context.Background()
	message := protocol.Message{
		Message: &v1.Message{
			Role: protocol.MessageRoleUser,
			Content: []*v1.Part{
				{
					Part: &v1.Part_Text{Text: "Test message"},
				},
			},
			MessageId: protocol.GenerateMessageID(),
		},
	}

	manager.storeMessage(message.Message)

	handler := &memoryTaskHandler{
		manager:   manager,
		messageID: message.MessageId,
		ctx:       ctx,
	}

	return handler, manager
}

func TestMemoryTaskHandler_BuildTask(t *testing.T) {
	handler, _ := setupTestHandler(t)

	// Test building a task
	taskID, err := handler.BuildTask(nil, nil)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if taskID == "" {
		t.Error("Expected task ID to be set")
	}

	// Test building task with custom ID
	customID := "custom-task-id"
	customTaskID, err := handler.BuildTask(&customID, nil)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if customTaskID != customID {
		t.Errorf("Expected task ID %s, got %s", customID, customTaskID)
	}
}

func TestMemoryTaskHandler_UpdateTaskState(t *testing.T) {
	handler, _ := setupTestHandler(t)

	// First create a task
	taskID, err := handler.BuildTask(nil, nil)
	if err != nil {
		t.Fatalf("Failed to create task: %v", err)
	}

	// Update task state
	err = handler.UpdateTaskState(&taskID, protocol.TaskStateWorking, nil)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	// Get the task to verify state was updated
	updatedTask, err := handler.GetTask(&taskID)
	if err != nil {
		t.Errorf("Failed to get updated task: %v", err)
	}

	if updatedTask.Task().Status.State != protocol.TaskStateWorking {
		t.Errorf("Expected state %s, got %s", protocol.TaskStateWorking, updatedTask.Task().Status.State)
	}
}

func TestMemoryTaskHandler_AddArtifact(t *testing.T) {
	handler, _ := setupTestHandler(t)

	// First create a task
	taskID, err := handler.BuildTask(nil, nil)
	if err != nil {
		t.Fatalf("Failed to create task: %v", err)
	}

	// Add artifact
	artifact := protocol.Artifact{
		Artifact: &v1.Artifact{
			ArtifactId: "test-artifact",
			Parts: []*v1.Part{
				{
					Part: &v1.Part_Text{Text: "Artifact content"},
				},
			},
		},
	}

	err = handler.AddArtifact(&taskID, artifact, false, true)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	// Verify artifact was added
	retrievedTask, err := handler.GetTask(&taskID)
	if err != nil {
		t.Errorf("Failed to get task: %v", err)
	}

	if len(retrievedTask.Task().Artifacts) == 0 {
		t.Error("Expected artifact to be added")
	}

	if retrievedTask.Task().Artifacts[0].ArtifactId != artifact.ArtifactId {
		t.Errorf("Expected artifact ID %s, got %s", artifact.ArtifactId, retrievedTask.Task().Artifacts[0].ArtifactId)
	}
}

func TestMemoryTaskHandler_SubscribeTask(t *testing.T) {
	handler, _ := setupTestHandler(t)

	// First create a task
	taskID, err := handler.BuildTask(nil, nil)
	if err != nil {
		t.Fatalf("Failed to create task: %v", err)
	}

	// Subscribe to task
	subscriber, err := handler.SubscribeTask(&taskID)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if subscriber == nil {
		t.Error("Expected subscriber but got nil")
	}

	// Clean up
	subscriber.Close()
}

func TestMemoryTaskHandler_GetTask(t *testing.T) {
	handler, _ := setupTestHandler(t)

	// First create a task
	taskID, err := handler.BuildTask(nil, nil)
	if err != nil {
		t.Fatalf("Failed to create task: %v", err)
	}

	// Get the task
	retrievedTask, err := handler.GetTask(&taskID)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if retrievedTask == nil {
		t.Error("Expected task but got nil")
	}

	if retrievedTask.Task().Id != taskID {
		t.Errorf("Expected task ID %s, got %s", taskID, retrievedTask.Task().Id)
	}
}

func TestMemoryTaskHandler_CleanTask(t *testing.T) {
	handler, _ := setupTestHandler(t)

	// First create a task
	taskID, err := handler.BuildTask(nil, nil)
	if err != nil {
		t.Fatalf("Failed to create task: %v", err)
	}

	// Clean the task
	err = handler.CleanTask(&taskID)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	// Verify task was cleaned (should be deleted, not just canceled)
	_, err = handler.GetTask(&taskID)
	if err == nil {
		t.Error("Expected error when getting cleaned task, but got none")
	}
}

func TestMemoryTaskHandler_GetMessageHistory(t *testing.T) {
	_, manager := setupTestHandler(t)

	// Create a message with context
	contextID := "test-context"
	contextMessage := &v1.Message{
		MessageId: "test-msg-id",
		ContextId: contextID,
		Role:      v1.Role_ROLE_USER,
		Content: []*v1.Part{
			{
				Part: &v1.Part_Text{Text: "Context message"},
			},
		},
	}

	manager.storeMessage(contextMessage)

	// Create handler with context message
	contextHandler := &memoryTaskHandler{
		manager:   manager,
		messageID: contextMessage.MessageId,
		ctx:       context.Background(),
	}

	// Get message history
	history := contextHandler.GetMessageHistory()

	if len(history) == 0 {
		t.Error("Expected message history but got empty")
	}

	// Should contain the context message
	found := false
	for _, msg := range history {
		if msg.MessageId == contextMessage.MessageId {
			found = true
			break
		}
	}

	if !found {
		t.Error("Expected to find context message in history")
	}
}

func TestMemoryTaskHandler_Metadata(t *testing.T) {
	t.Run("GetPopulatedMetadata", func(t *testing.T) {
		handler, _ := setupTestHandler(t)

		// add metadata to the handler
		expectedMetadata := map[string]interface{}{
			"strKey":  "value1",
			"intKey":  2,
			"boolKey": true,
			"mapKey": map[string]interface{}{
				"nestedKey": "nestedValue",
			},
			"sliceKey": []string{"item1", "item2"},
		}

		handler.metadata = expectedMetadata

		// retrieve metadata from handler method
		md, err := handler.GetMetadata()

		if err != nil {
			t.Errorf("Expected no error, got %v", err)
		}

		if md == nil {
			t.Error("Expected metadata to be populated, got nil")
		}

		if reflect.DeepEqual(md, expectedMetadata) == false {
			t.Errorf("Expected metadata %v, got %v", expectedMetadata, md)
		}
	})

	t.Run("GetNilMetadata", func(t *testing.T) {
		handler, _ := setupTestHandler(t)

		// retrieve metadata from handler method
		md, err := handler.GetMetadata()

		if err == nil {
			t.Error("Expected error for nil metadata, got none")
		}

		if md != nil {
			t.Errorf("Expected nil metadata, got %v", md)
		}
	})

}

func TestMemoryTaskHandler_GetContextID(t *testing.T) {
	_, manager := setupTestHandler(t)

	// Create a message with context
	contextID := "test-context-id"
	contextMessage := &v1.Message{
		MessageId: "test-msg-id-2",
		ContextId: contextID,
		Role:      v1.Role_ROLE_USER,
		Content: []*v1.Part{
			{
				Part: &v1.Part_Text{Text: "Context message"},
			},
		},
	}

	manager.storeMessage(contextMessage)

	// Create handler with context message
	contextHandler := &memoryTaskHandler{
		manager:   manager,
		messageID: contextMessage.MessageId,
		ctx:       context.Background(),
	}

	// Get context ID
	retrievedContextID := contextHandler.GetContextID()

	if retrievedContextID != contextID {
		t.Errorf("Expected context ID %s, got %s", contextID, retrievedContextID)
	}
}

func TestTaskHandlerErrors(t *testing.T) {
	processor := &MockMessageProcessor{}
	manager, err := NewMemoryTaskManager(processor)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	ctx := context.Background()
	handler := &memoryTaskHandler{
		manager:   manager,
		messageID: "non-existent-message",
		ctx:       ctx,
	}

	t.Run("UpdateTaskState_NonExistentTask", func(t *testing.T) {
		nonExistentTaskID := "non-existent-task"
		err := handler.UpdateTaskState(&nonExistentTaskID, protocol.TaskStateWorking, nil)
		if err == nil {
			t.Error("Expected error for non-existent task")
		}
	})

	t.Run("AddArtifact_NonExistentTask", func(t *testing.T) {
		nonExistentTaskID := "non-existent-task"
		artifact := protocol.Artifact{
			Artifact: &v1.Artifact{
				ArtifactId: "test-artifact",
			},
		}

		err := handler.AddArtifact(&nonExistentTaskID, artifact, false, true)
		if err == nil {
			t.Error("Expected error for non-existent task")
		}
	})

	t.Run("SubscribeTask_NonExistentTask", func(t *testing.T) {
		nonExistentTaskID := "non-existent-task"
		_, err := handler.SubscribeTask(&nonExistentTaskID)
		if err == nil {
			t.Error("Expected error for non-existent task")
		}
	})

	t.Run("GetTask_NonExistentTask", func(t *testing.T) {
		nonExistentTaskID := "non-existent-task"
		_, err := handler.GetTask(&nonExistentTaskID)
		if err == nil {
			t.Error("Expected error for non-existent task")
		}
	})

	t.Run("CleanTask_NonExistentTask", func(t *testing.T) {
		nonExistentTaskID := "non-existent-task"
		err := handler.CleanTask(&nonExistentTaskID)
		if err == nil {
			t.Error("Expected error for non-existent task")
		}
	})

	t.Run("NilTaskID", func(t *testing.T) {
		err := handler.UpdateTaskState(nil, protocol.TaskStateWorking, nil)
		if err == nil {
			t.Error("Expected error for nil task ID")
		}

		err = handler.AddArtifact(nil, protocol.Artifact{}, false, true)
		if err == nil {
			t.Error("Expected error for nil task ID")
		}

		_, err = handler.SubscribeTask(nil)
		if err == nil {
			t.Error("Expected error for nil task ID")
		}

		_, err = handler.GetTask(nil)
		if err == nil {
			t.Error("Expected error for nil task ID")
		}

		err = handler.CleanTask(nil)
		if err == nil {
			t.Error("Expected error for nil task ID")
		}
	})
}
