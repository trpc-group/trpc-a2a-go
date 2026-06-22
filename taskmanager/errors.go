// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package taskmanager defines task management interfaces, types, and implementations.
package taskmanager

import (
	"errors"
	"fmt"

	"trpc.group/trpc-go/trpc-a2a-go/v2/internal/jsonrpc"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

// JSON-RPC standard error codes
const (
	ErrCodeJSONParse      int = -32700 // Invalid JSON was received by the server
	ErrCodeInvalidRequest int = -32600 // The JSON sent is not a valid Request object
	ErrCodeMethodNotFound int = -32601 // The method does not exist or is not available
	ErrCodeInvalidParams  int = -32602 // Invalid method parameter(s)
	ErrCodeInternalError  int = -32603 // Internal JSON-RPC error
)

// Custom JSON-RPC error codes specific to the A2A specification
const (
	ErrCodeTaskNotFound                           int = -32001 // Task not found
	ErrCodeTaskNotCancelable                      int = -32002 // Task cannot be canceled
	ErrCodePushNotificationNotSupported           int = -32003 // Push Notification is not supported
	ErrCodeUnsupportedOperation                   int = -32004 // This operation is not supported
	ErrCodeContentTypeNotSupported                int = -32005 // Incompatible content types
	ErrCodeInvalidAgentResponse                   int = -32006 // Invalid agent response
	ErrCodeAuthenticatedExtendedCardNotConfigured int = -32007 // Authenticated extended card not configured
	ErrCodeExtensionSupportRequired               int = -32008 // A required extension was not opted into by the client
	ErrCodeVersionNotSupported                    int = -32009 // The requested A2A protocol version is not supported
)

// ErrCodePushNotificationNotConfigured is deprecated: Use ErrCodePushNotificationNotSupported instead
const ErrCodePushNotificationNotConfigured int = -32003

// Sentinel errors for type checking with errors.Is()
var (
	// ErrTaskNotFoundSentinel is a sentinel error for task not found
	ErrTaskNotFoundSentinel = errors.New("task not found")
	// ErrTaskNotCancelableSentinel is a sentinel error for task not cancelable
	ErrTaskNotCancelableSentinel = errors.New("task not cancelable")
	// ErrPushNotificationNotSupportedSentinel is a sentinel error for push notification not supported
	ErrPushNotificationNotSupportedSentinel = errors.New("push notification not supported")
	// ErrUnsupportedOperationSentinel is a sentinel error for unsupported operation
	ErrUnsupportedOperationSentinel = errors.New("unsupported operation")
	// ErrContentTypeNotSupportedSentinel is a sentinel error for content type not supported
	ErrContentTypeNotSupportedSentinel = errors.New("content type not supported")
	// ErrInvalidAgentResponseSentinel is a sentinel error for invalid agent response
	ErrInvalidAgentResponseSentinel = errors.New("invalid agent response")
	// ErrAuthenticatedExtendedCardNotConfiguredSentinel is a sentinel error for authenticated extended card not configured
	ErrAuthenticatedExtendedCardNotConfiguredSentinel = errors.New("authenticated extended card not configured")
	// ErrExtensionSupportRequiredSentinel is a sentinel error for a required extension not opted into
	ErrExtensionSupportRequiredSentinel = errors.New("extension support required")
	// ErrVersionNotSupportedSentinel is a sentinel error for an unsupported A2A protocol version
	ErrVersionNotSupportedSentinel = errors.New("version not supported")
)

// A2A specific error functions

// ErrTaskNotFound creates a JSON-RPC error for task not found.
// The returned error wraps ErrTaskNotFoundSentinel for use with errors.Is().
func ErrTaskNotFound(taskID string) *jsonrpc.Error {
	return (&jsonrpc.Error{
		Code:    ErrCodeTaskNotFound,
		Message: "Task not found",
		Data:    fmt.Sprintf("Task with ID '%s' was not found.", taskID),
	}).WithWrappedError(ErrTaskNotFoundSentinel)
}

// ErrTaskNotCancelable creates a JSON-RPC error for task that cannot be canceled.
// The returned error wraps ErrTaskNotCancelableSentinel for use with errors.Is().
func ErrTaskNotCancelable(taskID string, state protocol.TaskState) *jsonrpc.Error {
	return (&jsonrpc.Error{
		Code:    ErrCodeTaskNotCancelable,
		Message: "Task cannot be canceled",
		Data:    fmt.Sprintf("Task '%s' is in state '%s' and cannot be canceled", taskID, state),
	}).WithWrappedError(ErrTaskNotCancelableSentinel)
}

// ErrPushNotificationNotSupported creates a JSON-RPC error for unsupported push notifications.
// The returned error wraps ErrPushNotificationNotSupportedSentinel for use with errors.Is().
func ErrPushNotificationNotSupported() *jsonrpc.Error {
	return (&jsonrpc.Error{
		Code:    ErrCodePushNotificationNotSupported,
		Message: "Push Notification is not supported",
		Data:    "This agent does not support push notifications",
	}).WithWrappedError(ErrPushNotificationNotSupportedSentinel)
}

// ErrUnsupportedOperation creates a JSON-RPC error for unsupported operations.
// The returned error wraps ErrUnsupportedOperationSentinel for use with errors.Is().
func ErrUnsupportedOperation(operation string) *jsonrpc.Error {
	return (&jsonrpc.Error{
		Code:    ErrCodeUnsupportedOperation,
		Message: "This operation is not supported",
		Data:    fmt.Sprintf("Operation '%s' is not supported by this agent", operation),
	}).WithWrappedError(ErrUnsupportedOperationSentinel)
}

// ErrContentTypeNotSupported creates a JSON-RPC error for incompatible content types.
// The returned error wraps ErrContentTypeNotSupportedSentinel for use with errors.Is().
func ErrContentTypeNotSupported(contentType string) *jsonrpc.Error {
	return (&jsonrpc.Error{
		Code:    ErrCodeContentTypeNotSupported,
		Message: "Incompatible content types",
		Data:    fmt.Sprintf("Content type '%s' is not supported", contentType),
	}).WithWrappedError(ErrContentTypeNotSupportedSentinel)
}

// ErrInvalidAgentResponse creates a JSON-RPC error for invalid agent response.
// The returned error wraps ErrInvalidAgentResponseSentinel for use with errors.Is().
func ErrInvalidAgentResponse(details string) *jsonrpc.Error {
	return (&jsonrpc.Error{
		Code:    ErrCodeInvalidAgentResponse,
		Message: "Invalid agent response",
		Data:    details,
	}).WithWrappedError(ErrInvalidAgentResponseSentinel)
}

// ErrAuthenticatedExtendedCardNotConfigured creates a JSON-RPC error for authenticated extended card not configured.
// The returned error wraps ErrAuthenticatedExtendedCardNotConfiguredSentinel for use with errors.Is().
func ErrAuthenticatedExtendedCardNotConfigured() *jsonrpc.Error {
	return (&jsonrpc.Error{
		Code:    ErrCodeAuthenticatedExtendedCardNotConfigured,
		Message: "Authenticated extended card not configured",
		Data:    "This agent does not have an authenticated extended card configured",
	}).WithWrappedError(ErrAuthenticatedExtendedCardNotConfiguredSentinel)
}

// ErrExtensionSupportRequired creates a JSON-RPC error for a required extension the client did not opt into.
// The returned error wraps ErrExtensionSupportRequiredSentinel for use with errors.Is().
func ErrExtensionSupportRequired(extensionURI string) *jsonrpc.Error {
	return (&jsonrpc.Error{
		Code:    ErrCodeExtensionSupportRequired,
		Message: "Extension support required",
		Data:    fmt.Sprintf("Required extension '%s' was not opted into by the client", extensionURI),
	}).WithWrappedError(ErrExtensionSupportRequiredSentinel)
}

// ErrVersionNotSupported creates a JSON-RPC error for an unsupported A2A protocol version.
// The returned error wraps ErrVersionNotSupportedSentinel for use with errors.Is().
func ErrVersionNotSupported(requested string) *jsonrpc.Error {
	return (&jsonrpc.Error{
		Code:    ErrCodeVersionNotSupported,
		Message: "Version not supported",
		Data:    fmt.Sprintf("Requested A2A protocol version '%s' is not supported by this agent", requested),
	}).WithWrappedError(ErrVersionNotSupportedSentinel)
}
