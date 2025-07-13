// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package taskmanager

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/protobuf/types/known/timestamppb"
	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	v1 "trpc.group/trpc-go/trpc-a2a-go/protocol/a2apb"
)

// =============================================================================
// MessageHandle Implementation
// =============================================================================

// memoryTaskHandler implements TaskHandler interface
type memoryTaskHandler struct {
	manager                *MemoryTaskManager
	messageID              string
	metadata               map[string]interface{}
	ctx                    context.Context
	subscriberBufSize      int
	subscriberBlockingSend bool
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
	var updateMessage *v1.Message
	if message != nil {
		updateMessage = message.Message
	}
	originalTask.Status = &v1.TaskStatus{
		State:     state,
		Update:    updateMessage,
		Timestamp: timestamppb.Now(),
	}
	h.manager.taskMu.Unlock()

	log.Debugf("Updated task %s state to %s", *taskID, state)

	// notify subscribers
	finalState := isFinalState(state)
	event := protocol.NewTaskStatusUpdateEvent(*taskID, originalTask.ContextId, originalTask.Status, finalState)
	streamEvent := protocol.StreamingMessageEvent{Result: &event}
	h.manager.notifySubscribers(*taskID, streamEvent)
	return nil
}

// SubscribeTask subscribes to the task
func (h *memoryTaskHandler) SubscribeTask(taskID *string) (TaskSubscriber, error) {
	if taskID == nil || *taskID == "" {
		return nil, fmt.Errorf("taskID cannot be nil or empty")
	}
	if !h.manager.checkTaskExists(*taskID) {
		return nil, fmt.Errorf("task not found: %s", *taskID)
	}
	ctxID := h.GetContextID()
	sendHook := h.manager.sendStreamingEventHook(ctxID)
	bufSize := h.subscriberBufSize
	if bufSize <= 0 {
		bufSize = defaultSubscriberBufferSize
	}
	subscriber := NewMemoryTaskSubscriber(
		*taskID,
		bufSize,
		WithSubscriberSendHook(sendHook),
		WithSubscriberBlockingSend(h.subscriberBlockingSend),
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
	task.Task().Artifacts = append(task.Task().Artifacts, artifact.Artifact)
	h.manager.taskMu.Unlock()

	log.Debugf("Added artifact %s to task %s", artifact.ArtifactId, *taskID)

	// notify subscribers
	event := protocol.NewTaskArtifactUpdateEvent(*taskID, task.Task().ContextId, artifact, isFinal)
	streamEvent := protocol.StreamingMessageEvent{Result: &event}
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
		taskCopy.Artifacts = make([]*v1.Artifact, len(task.Task().Artifacts))
		copy(taskCopy.Artifacts, task.Task().Artifacts)
	}
	if taskCopy.History != nil {
		taskCopy.History = make([]*v1.Message, len(task.Task().History))
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

	if msg, exists := h.manager.Messages[h.messageID]; exists && msg.ContextId != "" {
		return msg.ContextId
	}
	return ""
}

// GetMetadata returns the metadata of the current request.
func (h *memoryTaskHandler) GetMetadata() (map[string]interface{}, error) {
	if h.metadata == nil {
		return nil, errors.New("metadata is nil")
	}

	return h.metadata, nil
}

// GetMessageHistory gets message history
func (h *memoryTaskHandler) GetMessageHistory() []protocol.Message {
	h.manager.conversationMu.RLock()
	defer h.manager.conversationMu.RUnlock()

	if msg, exists := h.manager.Messages[h.messageID]; exists && msg.ContextId != "" {
		v1Messages := h.manager.getMessageHistory(msg.ContextId)
		result := make([]protocol.Message, len(v1Messages))
		for i, v1Msg := range v1Messages {
			result[i] = protocol.Message{Message: v1Msg}
		}
		return result
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
		Task: &v1.Task{
			Id:        actualTaskID,
			ContextId: actualContextID,
			Status: &v1.TaskStatus{
				State:     protocol.TaskStateSubmitted,
				Timestamp: timestamppb.Now(),
			},
			Artifacts: make([]*v1.Artifact, 0),
			History:   make([]*v1.Message, 0),
		},
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
