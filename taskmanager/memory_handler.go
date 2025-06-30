// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package taskmanager

import (
	"context"
	"fmt"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

// =============================================================================
// MessageHandle Implementation
// =============================================================================

// memoryTaskHandler implements TaskHandler interface
type memoryTaskHandler struct {
	manager   *MemoryTaskManager
	messageID string
	ctx       context.Context
}

var _ TaskHandler = (*memoryTaskHandler)(nil)

// UpdateTaskState updates task state
func (h *memoryTaskHandler) UpdateTaskState(
	taskID *string,
	state protocol.TaskState,
	message *protocol.Message,
) error {
	if taskID == nil || *taskID == "" {
		return fmt.Errorf("taskID cannot be nil or empty")
	}

	h.manager.taskMu.Lock()
	task, exists := h.manager.Tasks[*taskID]
	if !exists {
		h.manager.taskMu.Unlock()
		log.Warnf("UpdateTaskState called for non-existent task %s", *taskID)
		return fmt.Errorf("task not found: %s", *taskID)
	}

	originalTask := task.Task()
	originalTask.Status = protocol.TaskStatus{
		State:     state,
		Message:   message,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	h.manager.taskMu.Unlock()

	log.Debugf("Updated task %s state to %s", *taskID, state)

	// notify subscribers
	finalState := isFinalState(state)
	event := &protocol.TaskStatusUpdateEvent{
		TaskID:    *taskID,
		ContextID: originalTask.ContextID,
		Status:    originalTask.Status,
		Kind:      protocol.KindTaskStatusUpdate,
		Final:     finalState,
	}
	streamEvent := protocol.StreamingMessageEvent{Result: event}
	h.manager.notifySubscribers(*taskID, streamEvent)
	return nil
}

// SubScribeTask subscribes to the task
func (h *memoryTaskHandler) SubScribeTask(taskID *string) (TaskSubscriber, error) {
	if taskID == nil || *taskID == "" {
		return nil, fmt.Errorf("taskID cannot be nil or empty")
	}
	if !h.manager.checkTaskExists(*taskID) {
		return nil, fmt.Errorf("task not found: %s", *taskID)
	}
	ctxID := h.GetContextID()
	sendHook := h.sendStreamingEventHook(ctxID)
	subscriber := NewMemoryTaskSubscriber(
		*taskID,
		defaultTaskSubscriberBufferSize,
		WithMemoryTaskSubscriberSendHook(sendHook),
	)
	h.manager.addSubscriber(*taskID, subscriber)
	return subscriber, nil
}

// AddArtifact adds artifact to specified task
func (h *memoryTaskHandler) AddArtifact(
	taskID *string,
	artifact protocol.Artifact,
	isFinal bool,
	needMoreData bool,
) error {
	if taskID == nil || *taskID == "" {
		return fmt.Errorf("taskID cannot be nil or empty")
	}

	h.manager.taskMu.Lock()
	task, exists := h.manager.Tasks[*taskID]
	if !exists {
		h.manager.taskMu.Unlock()
		return fmt.Errorf("task not found: %s", *taskID)
	}
	task.Task().Artifacts = append(task.Task().Artifacts, artifact)
	h.manager.taskMu.Unlock()

	log.Debugf("Added artifact %s to task %s", artifact.ArtifactID, *taskID)

	// notify subscribers
	event := &protocol.TaskArtifactUpdateEvent{
		TaskID:    *taskID,
		ContextID: task.Task().ContextID,
		Artifact:  artifact,
		Kind:      protocol.KindTaskArtifactUpdate,
		LastChunk: &isFinal,
		Append:    &needMoreData,
	}
	streamEvent := protocol.StreamingMessageEvent{Result: event}
	h.manager.notifySubscribers(*taskID, streamEvent)

	return nil
}

// GetTask gets task
func (h *memoryTaskHandler) GetTask(taskID *string) (CancellableTask, error) {
	if taskID == nil || *taskID == "" {
		return nil, fmt.Errorf("taskID cannot be nil or empty")
	}

	h.manager.taskMu.RLock()
	defer h.manager.taskMu.RUnlock()

	task, err := h.manager.getTask(*taskID)
	if err != nil {
		return nil, err
	}

	// return task copy to avoid external modification
	taskCopy := *task.Task()
	if taskCopy.Artifacts != nil {
		taskCopy.Artifacts = make([]protocol.Artifact, len(task.Task().Artifacts))
		copy(taskCopy.Artifacts, task.Task().Artifacts)
	}
	if taskCopy.History != nil {
		taskCopy.History = make([]protocol.Message, len(task.Task().History))
		copy(taskCopy.History, task.Task().History)
	}

	return &MemoryCancellableTask{
		task:       taskCopy,
		cancelFunc: task.cancelFunc,
		ctx:        task.ctx,
	}, nil
}

// GetContextID gets context ID
func (h *memoryTaskHandler) GetContextID() string {
	h.manager.conversationMu.RLock()
	defer h.manager.conversationMu.RUnlock()

	if msg, exists := h.manager.Messages[h.messageID]; exists && msg.ContextID != nil {
		return *msg.ContextID
	}
	return ""
}

// GetMessageHistory gets message history
func (h *memoryTaskHandler) GetMessageHistory() []protocol.Message {
	h.manager.conversationMu.RLock()
	defer h.manager.conversationMu.RUnlock()

	if msg, exists := h.manager.Messages[h.messageID]; exists && msg.ContextID != nil {
		return h.manager.getMessageHistory(*msg.ContextID)
	}
	return []protocol.Message{}
}

// BuildTask creates a new task and returns task object
func (h *memoryTaskHandler) BuildTask(specificTaskID *string, contextID *string) (string, error) {
	h.manager.taskMu.Lock()
	defer h.manager.taskMu.Unlock()

	// if no taskID provided, generate one
	var actualTaskID string
	if specificTaskID == nil || *specificTaskID == "" {
		actualTaskID = protocol.GenerateTaskID()
	} else {
		actualTaskID = *specificTaskID
	}

	// Check if task already exists to avoid duplicate WithCancel calls
	if _, exists := h.manager.Tasks[actualTaskID]; exists {
		log.Warnf("Task %s already exists, returning existing task", actualTaskID)
		return "", fmt.Errorf("task already exists: %s", actualTaskID)
	}

	var actualContextID string
	if contextID == nil || *contextID == "" {
		actualContextID = ""
	} else {
		actualContextID = *contextID
	}

	// create new task
	task := protocol.Task{
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

	cancellableTask := NewCancellableTask(task)

	// store task
	h.manager.Tasks[actualTaskID] = cancellableTask

	log.Debugf("Created new task %s with context %s", actualTaskID, actualContextID)

	return actualTaskID, nil
}

// CancelTask cancels the task.
func (h *memoryTaskHandler) CleanTask(taskID *string) error {
	if taskID == nil || *taskID == "" {
		return fmt.Errorf("taskID cannot be nil or empty")
	}

	h.manager.taskMu.Lock()
	task, exists := h.manager.Tasks[*taskID]
	if !exists {
		h.manager.taskMu.Unlock()
		return fmt.Errorf("task not found: %s", *taskID)
	}

	// Cancel the task and remove from Tasks map while holding the lock
	task.Cancel()
	delete(h.manager.Tasks, *taskID)

	// Clean up subscribers while holding the lock to avoid another lock acquisition
	for _, sub := range h.manager.Subscribers[*taskID] {
		sub.Close()
	}
	delete(h.manager.Subscribers, *taskID)

	h.manager.taskMu.Unlock()

	return nil
}

func (h *memoryTaskHandler) sendStreamingEventHook(ctxID string) func(event protocol.StreamingMessageEvent) error {
	return func(event protocol.StreamingMessageEvent) error {
		switch event.Result.(type) {
		case *protocol.TaskStatusUpdateEvent:
			event := event.Result.(*protocol.TaskStatusUpdateEvent)
			if event.ContextID == "" {
				event.ContextID = ctxID
			}
		case *protocol.TaskArtifactUpdateEvent:
			event := event.Result.(*protocol.TaskArtifactUpdateEvent)
			if event.ContextID == "" {
				event.ContextID = ctxID
			}
		case *protocol.Message:
			event := event.Result.(*protocol.Message)
			// store message
			h.manager.processReplyMessage(&ctxID, event)
		case *protocol.Task:
			event := event.Result.(*protocol.Task)
			if event.ContextID == "" {
				event.ContextID = ctxID
			}
		}
		return nil
	}
}
