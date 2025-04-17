// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package taskmanager defines task management interfaces, types, and implementations.
package taskmanager

import (
	"fmt"

	"trpc.group/trpc-go/trpc-a2a-go/jsonrpc"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

// Custom JSON-RPC error codes specific to the TaskManager.
const (
	ErrCodeTaskNotFound                  int = -32001 // Custom server error code range.
	ErrCodeTaskFinal                     int = -32002
	ErrCodePushNotificationNotConfigured int = -32003
)

// ErrTaskNotFound creates a JSON-RPC error for task not found.
// Exported function.
func ErrTaskNotFound(taskID string) *jsonrpc.Error {
	return &jsonrpc.Error{
		Code:    ErrCodeTaskNotFound,
		Message: "Task not found",
		Data:    fmt.Sprintf("Task with ID '%s' was not found.", taskID),
	}
}

// ErrTaskFinalState creates a JSON-RPC error for attempting an operation on a task
// that is already in a final state (completed, failed, cancelled).
// Exported function.
func ErrTaskFinalState(taskID string, state protocol.TaskState) *jsonrpc.Error {
	return &jsonrpc.Error{
		Code:    ErrCodeTaskFinal,
		Message: "Task is in final state",
		Data:    fmt.Sprintf("Task '%s' is already in final state: %s", taskID, state),
	}
}

// ErrPushNotificationNotConfigured creates a JSON-RPC error for when push notifications
// haven't been configured for a task.
// Exported function.
func ErrPushNotificationNotConfigured(taskID string) *jsonrpc.Error {
	return &jsonrpc.Error{
		Code:    ErrCodePushNotificationNotConfigured,
		Message: "Push Notification not configured",
		Data:    fmt.Sprintf("Task '%s' does not have push notifications configured.", taskID),
	}
}
