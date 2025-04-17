// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package taskmanager

import (
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

// memoryTaskHandle implements the TaskHandle interface, providing callbacks
// for a specific task being processed by a TaskProcessor.
// It holds a reference back to the MemoryTaskManager.
type memoryTaskHandle struct {
	taskID  string
	manager *MemoryTaskManager
}

// UpdateStatus implements TaskHandle.
func (h *memoryTaskHandle) UpdateStatus(state protocol.TaskState, msg *protocol.Message) error {
	return h.manager.UpdateTaskStatus(h.taskID, state, msg)
}

// AddArtifact implements TaskHandle.
func (h *memoryTaskHandle) AddArtifact(artifact protocol.Artifact) error {
	return h.manager.AddArtifact(h.taskID, artifact)
}

// IsStreamingRequest checks if this task was initiated with a streaming request (OnSendTaskSubscribe).
// It returns true if there are active subscribers for this task, indicating it was initiated
// with OnSendTaskSubscribe rather than OnSendTask.
func (h *memoryTaskHandle) IsStreamingRequest() bool {
	h.manager.SubMutex.RLock()
	defer h.manager.SubMutex.RUnlock()

	subscribers, exists := h.manager.Subscribers[h.taskID]
	return exists && len(subscribers) > 0
}

// isFinalState checks if a TaskState represents a terminal state.
// Not exported as it's an internal helper.
func isFinalState(state protocol.TaskState) bool {
	return state == protocol.TaskStateCompleted || state == protocol.TaskStateFailed || state == protocol.TaskStateCanceled
}
