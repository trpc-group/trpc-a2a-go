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

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

// ErrorCode identifies a binding-neutral A2A operation error. Protocol
// adapters map these codes to JSON-RPC codes, HTTP statuses, or gRPC statuses.
type ErrorCode string

// ErrCodeInvalidParams and the remaining values identify binding-neutral A2A operation errors.
const (
	ErrCodeInvalidParams                          ErrorCode = "INVALID_PARAMS"
	ErrCodeInternalError                          ErrorCode = "INTERNAL_ERROR"
	ErrCodeTaskNotFound                           ErrorCode = "TASK_NOT_FOUND"
	ErrCodeTaskNotCancelable                      ErrorCode = "TASK_NOT_CANCELABLE"
	ErrCodePushNotificationNotSupported           ErrorCode = "PUSH_NOTIFICATION_NOT_SUPPORTED"
	ErrCodeUnsupportedOperation                   ErrorCode = "UNSUPPORTED_OPERATION"
	ErrCodeContentTypeNotSupported                ErrorCode = "CONTENT_TYPE_NOT_SUPPORTED"
	ErrCodeInvalidAgentResponse                   ErrorCode = "INVALID_AGENT_RESPONSE"
	ErrCodeAuthenticatedExtendedCardNotConfigured ErrorCode = "EXTENDED_AGENT_CARD_NOT_CONFIGURED"
	ErrCodeExtensionSupportRequired               ErrorCode = "EXTENSION_SUPPORT_REQUIRED"
	ErrCodeVersionNotSupported                    ErrorCode = "VERSION_NOT_SUPPORTED"
)

// Deprecated JSON-RPC error codes retained for source compatibility. Protocol
// adapters own transport-specific error mapping; TaskManager implementations
// should return ErrInvalidParams or ErrInternalError instead.
const (
	ErrCodeJSONParse      int = -32700
	ErrCodeInvalidRequest int = -32600
	ErrCodeMethodNotFound int = -32601
)

// ErrCodePushNotificationNotConfigured is deprecated: Use
// ErrCodePushNotificationNotSupported instead.
const ErrCodePushNotificationNotConfigured int = -32003

// Error is the binding-neutral error returned by TaskManager implementations.
// Data carries optional diagnostic context for protocol adapters and logs.
type Error struct {
	Code    ErrorCode
	Message string
	Data    any
	cause   error
}

// Error implements the standard error interface. A string Data is appended so
// the diagnostic detail survives logging and %v formatting: the constructors
// put the generic A2A wording in Message and the specific cause in Data.
func (e *Error) Error() string {
	if e == nil {
		return "<nil taskmanager error>"
	}
	if detail, ok := e.Data.(string); ok && detail != "" {
		return fmt.Sprintf("a2a error %s: %s: %s", e.Code, e.Message, detail)
	}
	return fmt.Sprintf("a2a error %s: %s", e.Code, e.Message)
}

// Unwrap exposes the stable sentinel associated with this error.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func newError(code ErrorCode, message string, data any, cause error) *Error {
	return &Error{Code: code, Message: message, Data: data, cause: cause}
}

// NewError builds an error for an already-known code, attaching the sentinel
// that errors.Is matches. Protocol adapters use it to rebuild the semantic
// error carried by a wire response; the constructors below remain the way to
// raise one. An unrecognized code yields an error with no sentinel.
func NewError(code ErrorCode, message string, data any) *Error {
	return newError(code, message, data, sentinelForCode(code))
}

// SentinelForCode reports the sentinel associated with code, or nil when the
// code is not one of the standard A2A errors.
func SentinelForCode(code ErrorCode) error { return sentinelForCode(code) }

func sentinelForCode(code ErrorCode) error {
	switch code {
	case ErrCodeInvalidParams:
		return ErrInvalidParamsSentinel
	case ErrCodeInternalError:
		return ErrInternalErrorSentinel
	case ErrCodeTaskNotFound:
		return ErrTaskNotFoundSentinel
	case ErrCodeTaskNotCancelable:
		return ErrTaskNotCancelableSentinel
	case ErrCodePushNotificationNotSupported:
		return ErrPushNotificationNotSupportedSentinel
	case ErrCodeUnsupportedOperation:
		return ErrUnsupportedOperationSentinel
	case ErrCodeContentTypeNotSupported:
		return ErrContentTypeNotSupportedSentinel
	case ErrCodeInvalidAgentResponse:
		return ErrInvalidAgentResponseSentinel
	case ErrCodeAuthenticatedExtendedCardNotConfigured:
		return ErrAuthenticatedExtendedCardNotConfiguredSentinel
	case ErrCodeExtensionSupportRequired:
		return ErrExtensionSupportRequiredSentinel
	case ErrCodeVersionNotSupported:
		return ErrVersionNotSupportedSentinel
	default:
		return nil
	}
}

// Sentinel errors for type checking with errors.Is().
var (
	ErrInvalidParamsSentinel                          = errors.New("invalid params")
	ErrInternalErrorSentinel                          = errors.New("internal error")
	ErrTaskNotFoundSentinel                           = errors.New("task not found")
	ErrTaskNotCancelableSentinel                      = errors.New("task not cancelable")
	ErrPushNotificationNotSupportedSentinel           = errors.New("push notification not supported")
	ErrUnsupportedOperationSentinel                   = errors.New("unsupported operation")
	ErrContentTypeNotSupportedSentinel                = errors.New("content type not supported")
	ErrInvalidAgentResponseSentinel                   = errors.New("invalid agent response")
	ErrAuthenticatedExtendedCardNotConfiguredSentinel = errors.New("authenticated extended card not configured")
	ErrExtensionSupportRequiredSentinel               = errors.New("extension support required")
	ErrVersionNotSupportedSentinel                    = errors.New("version not supported")
	ErrPushConfigNotFoundSentinel                     = errors.New("push notification config not found")
)

// ErrTaskNotFound creates an A2A task-not-found error.
func ErrTaskNotFound(taskID string) *Error {
	return newError(
		ErrCodeTaskNotFound,
		"Task not found",
		fmt.Sprintf("Task with ID '%s' was not found.", taskID),
		ErrTaskNotFoundSentinel,
	)
}

// ErrTaskNotCancelable creates an A2A task-not-cancelable error.
func ErrTaskNotCancelable(taskID string, state protocol.TaskState) *Error {
	return newError(
		ErrCodeTaskNotCancelable,
		"Task cannot be canceled",
		fmt.Sprintf("Task '%s' is in state '%s' and cannot be canceled", taskID, state),
		ErrTaskNotCancelableSentinel,
	)
}

// ErrPushNotificationNotSupported creates an unsupported-push error.
func ErrPushNotificationNotSupported() *Error {
	return newError(
		ErrCodePushNotificationNotSupported,
		"Push Notification is not supported",
		"This agent does not support push notifications",
		ErrPushNotificationNotSupportedSentinel,
	)
}

// ErrPushConfigNotFound creates the TaskNotFoundError required by the A2A
// push-config methods when the addressed configuration does not exist.
func ErrPushConfigNotFound(taskID string) *Error {
	return newError(
		ErrCodeTaskNotFound,
		"Task not found",
		fmt.Sprintf("Task '%s' has no push notification config.", taskID),
		ErrPushConfigNotFoundSentinel,
	)
}

// ErrUnsupportedOperation creates an unsupported-operation error.
func ErrUnsupportedOperation(operation string) *Error {
	return newError(
		ErrCodeUnsupportedOperation,
		"This operation is not supported",
		fmt.Sprintf("Operation '%s' is not supported by this agent", operation),
		ErrUnsupportedOperationSentinel,
	)
}

// ErrContentTypeNotSupported creates an incompatible-content-type error.
func ErrContentTypeNotSupported(contentType string) *Error {
	return newError(
		ErrCodeContentTypeNotSupported,
		"Incompatible content types",
		fmt.Sprintf("Content type '%s' is not supported", contentType),
		ErrContentTypeNotSupportedSentinel,
	)
}

// ErrInvalidAgentResponse creates an invalid-agent-response error.
func ErrInvalidAgentResponse(details string) *Error {
	return newError(
		ErrCodeInvalidAgentResponse,
		"Invalid agent response",
		details,
		ErrInvalidAgentResponseSentinel,
	)
}

// ErrAuthenticatedExtendedCardNotConfigured creates an extended-card error.
func ErrAuthenticatedExtendedCardNotConfigured() *Error {
	return newError(
		ErrCodeAuthenticatedExtendedCardNotConfigured,
		"Authenticated extended card not configured",
		"This agent does not have an authenticated extended card configured",
		ErrAuthenticatedExtendedCardNotConfiguredSentinel,
	)
}

// ErrExtensionSupportRequired creates an error for a required extension that
// the client did not opt into.
func ErrExtensionSupportRequired(extensionURI string) *Error {
	return newError(
		ErrCodeExtensionSupportRequired,
		"Extension support required",
		fmt.Sprintf("Required extension '%s' was not opted into by the client", extensionURI),
		ErrExtensionSupportRequiredSentinel,
	)
}

// ErrVersionNotSupported creates an unsupported-version error.
func ErrVersionNotSupported(requested string) *Error {
	return newError(
		ErrCodeVersionNotSupported,
		"Version not supported",
		fmt.Sprintf("Requested A2A protocol version '%s' is not supported by this agent", requested),
		ErrVersionNotSupportedSentinel,
	)
}

// ErrInvalidParams creates a binding-neutral invalid-parameters error.
func ErrInvalidParams(details string) error {
	return newError(ErrCodeInvalidParams, "Invalid params", details, ErrInvalidParamsSentinel)
}

// ErrInternalError creates a binding-neutral internal error.
func ErrInternalError(details string) error {
	return newError(ErrCodeInternalError, "Internal error", details, ErrInternalErrorSentinel)
}
