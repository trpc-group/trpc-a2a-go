// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package redis provides a Redis-based implementation of the A2A TaskManager interface.
package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

// taskHandler implements TaskHandler interface for Redis.
type taskHandler struct {
	manager   *TaskManager
	messageID string
	ctx       context.Context
}

var _ taskmanager.TaskHandler = (*taskHandler)(nil)

// BuildTask creates a new task and returns the task ID.
func (h *taskHandler) BuildTask(specificTaskID *string, contextID *string) (string, error) {
	// If no taskID provided, generate one.
	var actualTaskID string
	if specificTaskID == nil || *specificTaskID == "" {
		actualTaskID = protocol.GenerateTaskID()
	} else {
		actualTaskID = *specificTaskID
	}

	// Check if task already exists.
	_, err := h.manager.getTaskInternal(h.ctx, actualTaskID)
	if err == nil {
		// Task exists, return the existing task ID.
		log.Warnf("Task %s already exists, returning existing task ID", actualTaskID)
		return actualTaskID, nil
	}

	var actualContextID string
	if contextID == nil || *contextID == "" {
		actualContextID = ""
	} else {
		actualContextID = *contextID
	}

	// Create new context for cancellation.
	_, cancel := context.WithCancel(context.Background())

	// Create new task.
	task := &protocol.Task{
		ID:        actualTaskID,
		ContextID: actualContextID,
		Kind:      protocol.KindTask,
		Status: protocol.TaskStatus{
			State:     protocol.TaskStateSubmitted,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
		Artifacts: make([]protocol.Artifact, 0),
		History:   make([]protocol.Message, 0),
		Metadata:  make(map[string]interface{}),
	}

	// Store task in Redis.
	if err := h.manager.storeTask(h.ctx, task); err != nil {
		cancel() // Clean up the context.
		return "", fmt.Errorf("failed to store task: %w", err)
	}

	// Store the cancel function.
	h.manager.cancelMu.Lock()
	h.manager.cancels[actualTaskID] = cancel
	h.manager.cancelMu.Unlock()

	log.Debugf("Created new task %s with context %s", actualTaskID, actualContextID)

	return actualTaskID, nil
}

// UpdateTaskState updates the task's state and returns an error if failed.
func (h *taskHandler) UpdateTaskState(
	taskID *string,
	state protocol.TaskState,
	message *protocol.Message,
) error {
	if taskID == nil || *taskID == "" {
		return fmt.Errorf("taskID cannot be nil or empty")
	}

	task, err := h.manager.getTaskInternal(h.ctx, *taskID)
	if err != nil {
		log.Warnf("UpdateTaskState called for non-existent task %s", *taskID)
		return fmt.Errorf("task not found: %s", *taskID)
	}

	// Update task status.
	task.Status = protocol.TaskStatus{
		State:     state,
		Message:   message,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	// Store updated task.
	if err := h.manager.storeTask(h.ctx, task); err != nil {
		return fmt.Errorf("failed to update task status: %w", err)
	}

	log.Debugf("Updated task %s state to %s", *taskID, state)

	// Notify subscribers.
	finalState := isFinalState(state)
	event := &protocol.TaskStatusUpdateEvent{
		TaskID:    *taskID,
		ContextID: task.ContextID,
		Status:    task.Status,
		Kind:      protocol.KindTaskStatusUpdate,
		Final:     &finalState,
	}
	streamEvent := protocol.StreamingMessageEvent{Result: event}
	h.manager.notifySubscribers(*taskID, streamEvent)

	if finalState {
		// Clean up resources for final states.
		h.manager.cleanSubscribers(*taskID)
		h.manager.cancelMu.Lock()
		if cancel, exists := h.manager.cancels[*taskID]; exists {
			cancel()
			delete(h.manager.cancels, *taskID)
		}
		h.manager.cancelMu.Unlock()
	}

	return nil
}

// AddArtifact adds an artifact to the specified task.
func (h *taskHandler) AddArtifact(taskID *string, artifact protocol.Artifact, isFinal bool, needMoreData bool) error {
	if taskID == nil || *taskID == "" {
		return fmt.Errorf("taskID cannot be nil or empty")
	}

	task, err := h.manager.getTaskInternal(h.ctx, *taskID)
	if err != nil {
		return fmt.Errorf("task not found: %s", *taskID)
	}

	// Append the artifact.
	if task.Artifacts == nil {
		task.Artifacts = make([]protocol.Artifact, 0, 1)
	}
	task.Artifacts = append(task.Artifacts, artifact)

	// Store updated task.
	if err := h.manager.storeTask(h.ctx, task); err != nil {
		return fmt.Errorf("failed to update task artifacts: %w", err)
	}

	log.Debugf("Added artifact %s to task %s", artifact.ArtifactID, *taskID)

	// Notify subscribers.
	event := &protocol.TaskArtifactUpdateEvent{
		TaskID:    *taskID,
		ContextID: task.ContextID,
		Artifact:  artifact,
		Kind:      protocol.KindTaskArtifactUpdate,
		LastChunk: &isFinal,
		Append:    &needMoreData,
	}
	streamEvent := protocol.StreamingMessageEvent{Result: event}
	h.manager.notifySubscribers(*taskID, streamEvent)

	return nil
}

// SubScribeTask subscribes to the task and returns a TaskSubscriber.
func (h *taskHandler) SubScribeTask(taskID *string) (taskmanager.TaskSubscriber, error) {
	if taskID == nil || *taskID == "" {
		return nil, fmt.Errorf("taskID cannot be nil or empty")
	}

	// Check if task exists.
	_, err := h.manager.getTaskInternal(h.ctx, *taskID)
	if err != nil {
		return nil, fmt.Errorf("task not found: %s", *taskID)
	}

	subscriber := NewTaskSubscriber(*taskID, defaultTaskSubscriberBufferSize)
	h.manager.addSubscriber(*taskID, subscriber)
	return subscriber, nil
}

// GetTask returns the task by taskID as a CancellableTask.
func (h *taskHandler) GetTask(taskID *string) (taskmanager.CancellableTask, error) {
	if taskID == nil || *taskID == "" {
		return nil, fmt.Errorf("taskID cannot be nil or empty")
	}

	task, err := h.manager.getTaskInternal(h.ctx, *taskID)
	if err != nil {
		return nil, err
	}

	// Get the cancel function for the task if it exists.
	h.manager.cancelMu.RLock()
	cancel, exists := h.manager.cancels[*taskID]
	h.manager.cancelMu.RUnlock()

	if !exists {
		// Create a no-op cancel function if one doesn't exist.
		cancel = func() {}
	}

	return NewRedisCancellableTask(task, cancel), nil
}

// CleanTask deletes the task and cleans up all associated resources.
func (h *taskHandler) CleanTask(taskID *string) error {
	if taskID == nil || *taskID == "" {
		return fmt.Errorf("taskID cannot be nil or empty")
	}

	// Get the task first to verify it exists.
	_, err := h.manager.getTaskInternal(h.ctx, *taskID)
	if err != nil {
		return fmt.Errorf("task not found: %s", *taskID)
	}

	// Cancel the task context.
	h.manager.cancelMu.Lock()
	if cancel, exists := h.manager.cancels[*taskID]; exists {
		cancel()
		delete(h.manager.cancels, *taskID)
	}
	h.manager.cancelMu.Unlock()

	// Clean up subscribers.
	h.manager.cleanSubscribers(*taskID)

	// Delete the task from Redis.
	return h.manager.deleteTask(h.ctx, *taskID)
}

// GetContextID returns the context ID of the current message, if any.
func (h *taskHandler) GetContextID() string {
	msgKey := messagePrefix + h.messageID
	msgBytes, err := h.manager.client.Get(h.ctx, msgKey).Bytes()
	if err != nil {
		return ""
	}

	var msg protocol.Message
	if err := json.Unmarshal(msgBytes, &msg); err != nil {
		return ""
	}

	if msg.ContextID != nil {
		return *msg.ContextID
	}
	return ""
}

// GetMessageHistory returns the conversation history for the current context.
func (h *taskHandler) GetMessageHistory() []protocol.Message {
	contextID := h.GetContextID()
	if contextID == "" {
		return []protocol.Message{}
	}

	history, err := h.manager.getConversationHistory(h.ctx, contextID, h.manager.maxHistoryLength)
	if err != nil {
		log.Errorf("Failed to get message history for context %s: %v", contextID, err)
		return []protocol.Message{}
	}

	return history
}
