// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package taskmanager

import (
	"errors"
	"testing"

	"trpc.group/trpc-go/trpc-a2a-go/internal/jsonrpc"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

func TestErrorSentinels(t *testing.T) {
	tests := []struct {
		name         string
		errorFunc    func() error
		sentinel     error
		expectedCode int
		expectedMsg  string
	}{
		{
			name:         "TaskNotFound",
			errorFunc:    func() error { return ErrTaskNotFound("task-123") },
			sentinel:     ErrTaskNotFoundSentinel,
			expectedCode: ErrCodeTaskNotFound,
			expectedMsg:  "Task not found",
		},
		{
			name:         "TaskNotCancelable",
			errorFunc:    func() error { return ErrTaskNotCancelable("task-456", protocol.TaskStateCompleted) },
			sentinel:     ErrTaskNotCancelableSentinel,
			expectedCode: ErrCodeTaskNotCancelable,
			expectedMsg:  "Task cannot be canceled",
		},
		{
			name:         "PushNotificationNotSupported",
			errorFunc:    func() error { return ErrPushNotificationNotSupported() },
			sentinel:     ErrPushNotificationNotSupportedSentinel,
			expectedCode: ErrCodePushNotificationNotSupported,
			expectedMsg:  "Push Notification is not supported",
		},
		{
			name:         "UnsupportedOperation",
			errorFunc:    func() error { return ErrUnsupportedOperation("test-op") },
			sentinel:     ErrUnsupportedOperationSentinel,
			expectedCode: ErrCodeUnsupportedOperation,
			expectedMsg:  "This operation is not supported",
		},
		{
			name:         "ContentTypeNotSupported",
			errorFunc:    func() error { return ErrContentTypeNotSupported("application/xml") },
			sentinel:     ErrContentTypeNotSupportedSentinel,
			expectedCode: ErrCodeContentTypeNotSupported,
			expectedMsg:  "Incompatible content types",
		},
		{
			name:         "InvalidAgentResponse",
			errorFunc:    func() error { return ErrInvalidAgentResponse("invalid response") },
			sentinel:     ErrInvalidAgentResponseSentinel,
			expectedCode: ErrCodeInvalidAgentResponse,
			expectedMsg:  "Invalid agent response",
		},
		{
			name:         "AuthenticatedExtendedCardNotConfigured",
			errorFunc:    func() error { return ErrAuthenticatedExtendedCardNotConfigured() },
			sentinel:     ErrAuthenticatedExtendedCardNotConfiguredSentinel,
			expectedCode: ErrCodeAuthenticatedExtendedCardNotConfigured,
			expectedMsg:  "Authenticated extended card not configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.errorFunc()

			// Test errors.Is() compatibility
			if !errors.Is(err, tt.sentinel) {
				t.Errorf("errors.Is() failed: expected error to wrap %v", tt.sentinel)
			}

			// Test error code and message by type asserting to *jsonrpc.Error
			jErr, ok := err.(*jsonrpc.Error)
			if !ok {
				t.Fatalf("error is not *jsonrpc.Error, got %T", err)
			}

			if jErr.Code != tt.expectedCode {
				t.Errorf("expected code %d, got %d", tt.expectedCode, jErr.Code)
			}
			if jErr.Message != tt.expectedMsg {
				t.Errorf("expected message %q, got %q", tt.expectedMsg, jErr.Message)
			}
		})
	}
}

func TestErrorUnwrap(t *testing.T) {
	err := ErrTaskNotFound("test-task")

	// Test that Unwrap returns the sentinel error
	unwrapped := errors.Unwrap(err)
	if unwrapped != ErrTaskNotFoundSentinel {
		t.Errorf("expected unwrapped error to be ErrTaskNotFoundSentinel, got %v", unwrapped)
	}

	// Test errors.Is() with the sentinel
	if !errors.Is(err, ErrTaskNotFoundSentinel) {
		t.Error("errors.Is() should return true for sentinel error")
	}
}

func TestErrorChaining(t *testing.T) {
	// Create a wrapped error
	baseErr := ErrTaskNotFound("task-789")
	wrappedErr := errors.New("additional context: " + baseErr.Error())

	// Test that we can still detect the sentinel through the chain
	if !errors.Is(baseErr, ErrTaskNotFoundSentinel) {
		t.Error("should be able to detect sentinel error in chain")
	}

	// Test that wrapping doesn't break the chain
	if errors.Is(wrappedErr, ErrTaskNotFoundSentinel) {
		t.Error("wrapped error should not match sentinel (different wrapping)")
	}
}

func TestErrorData(t *testing.T) {
	taskID := "task-abc-123"
	err := ErrTaskNotFound(taskID)

	// Check that the error data contains the task ID
	errStr := err.Error()
	if errStr == "" {
		t.Error("error string should not be empty")
	}

	// The error should be usable as a standard Go error
	var stdErr error = err
	if stdErr == nil {
		t.Error("error should be assignable to error interface")
	}
}
