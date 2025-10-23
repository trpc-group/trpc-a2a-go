// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package jsonrpc defines types and helpers for JSON-RPC 2.0 communication,
// adhering to the specification at https://www.jsonrpc.org/specification.
package jsonrpc

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Version is the JSON-RPC version.
const Version = "2.0"

// Standard JSON-RPC 2.0 error codes.
const (
	// CodeParseError indicates invalid JSON was received by the server.
	// An error occurred on the server while parsing the JSON text.
	CodeParseError = -32700
	// CodeInvalidRequest indicates the JSON sent is not a valid Request object.
	CodeInvalidRequest = -32600
	// CodeMethodNotFound indicates the method does not exist / is not available.
	CodeMethodNotFound = -32601
	// CodeInvalidParams indicates invalid method parameter(s).
	CodeInvalidParams = -32602
	// CodeInternalError indicates an internal JSON-RPC error.
	CodeInternalError = -32603
	// -32000 to -32099 are reserved for implementation-defined server-errors.
)

// Message is the base structure embedding common fields for JSON-RPC
// requests and responses.
type Message struct {
	// JSONRPC specifies the version of the JSON-RPC protocol. MUST be "2.0".
	JSONRPC string `json:"jsonrpc"`
	// ID is an identifier established by the Client that MUST contain a String,
	// Number, or NULL value if included. If it is not included it is assumed
	// to be a notification. The value SHOULD normally not be Null and Numbers
	// SHOULD NOT contain fractional parts.
	ID interface{} `json:"id,omitempty"`
}

// RawResponse is a JSON-RPC response that includes the raw result as a
// json.RawMessage. This is useful for APIs that return arbitrary JSON data.
type RawResponse struct {
	Response                 // Embed base fields (id, jsonrpc, error).
	Result   json.RawMessage `json:"result"` // Get result as raw JSON first.
}

// Error represents a JSON-RPC error object, included in responses when
// an error occurs.
type Error struct {
	// Code is a Number that indicates the error type that occurred.
	// This MUST be an integer.
	Code int `json:"code"`
	// Message is a String providing a short description of the error.
	// The message SHOULD be limited to a concise single sentence.
	Message string `json:"message"`
	// Data is a Primitive or Structured value that contains additional
	// information about the error. This may be omitted.
	// The value of this member is defined by the Server (e.g. detailed error
	// information, nested errors etc.).
	Data interface{} `json:"data,omitempty"`
	// wrappedErr is an optional underlying error for error chain support.
	// This enables error wrapping and use with errors.Is() and errors.As().
	// This field is not serialized to JSON.
	wrappedErr error
}

// Error implements the standard Go error interface for JSONRPCError, providing
// a basic string representation of the error.
func (e *Error) Error() string {
	if e == nil {
		return "<nil jsonrpc error>"
	}
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// Unwrap returns the underlying error if one exists, enabling error chain traversal.
// This method is used by errors.Is() and errors.As() for error matching.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.wrappedErr
}

// WithWrappedError sets the underlying error for error chain support.
// This enables use with errors.Is() and errors.As().
func (e *Error) WithWrappedError(err error) *Error {
	if e != nil {
		e.wrappedErr = err
	}
	return e
}

// --- Standard Error Constructors ---

// Sentinel errors for standard JSON-RPC errors
var (
	// ErrParseErrorSentinel is a sentinel error for JSON parse errors
	ErrParseErrorSentinel = errors.New("parse error")
	// ErrInvalidRequestSentinel is a sentinel error for invalid requests
	ErrInvalidRequestSentinel = errors.New("invalid request")
	// ErrMethodNotFoundSentinel is a sentinel error for method not found
	ErrMethodNotFoundSentinel = errors.New("method not found")
	// ErrInvalidParamsSentinel is a sentinel error for invalid params
	ErrInvalidParamsSentinel = errors.New("invalid params")
	// ErrInternalErrorSentinel is a sentinel error for internal errors
	ErrInternalErrorSentinel = errors.New("internal error")
)

// ErrParseError creates a standard Parse Error (-32700) JSONRPCError.
// Use this when the server fails to parse the JSON request.
// The returned error wraps ErrParseErrorSentinel for use with errors.Is().
func ErrParseError(data interface{}) *Error {
	return (&Error{Code: CodeParseError, Message: "Parse error", Data: data}).
		WithWrappedError(ErrParseErrorSentinel)
}

// ErrInvalidRequest creates a standard Invalid Request error (-32600) JSONRPCError.
// Use this when the JSON is valid, but the request object is not a valid
// JSON-RPC Request (e.g., missing "jsonrpc" or "method").
// The returned error wraps ErrInvalidRequestSentinel for use with errors.Is().
func ErrInvalidRequest(data interface{}) *Error {
	return (&Error{Code: CodeInvalidRequest, Message: "Invalid Request", Data: data}).
		WithWrappedError(ErrInvalidRequestSentinel)
}

// ErrMethodNotFound creates a standard Method Not Found error (-32601) JSONRPCError.
// Use this when the requested method does not exist on the server.
// The returned error wraps ErrMethodNotFoundSentinel for use with errors.Is().
func ErrMethodNotFound(data interface{}) *Error {
	return (&Error{Code: CodeMethodNotFound, Message: "Method not found", Data: data}).
		WithWrappedError(ErrMethodNotFoundSentinel)
}

// ErrInvalidParams creates a standard Invalid Params error (-32602) JSONRPCError.
// Use this when the method parameters are invalid (e.g., wrong type, missing fields).
// The returned error wraps ErrInvalidParamsSentinel for use with errors.Is().
func ErrInvalidParams(data interface{}) *Error {
	return (&Error{Code: CodeInvalidParams, Message: "Invalid params", Data: data}).
		WithWrappedError(ErrInvalidParamsSentinel)
}

// ErrInternalError creates a standard Internal Error (-32603) JSONRPCError.
// Use this for generic internal server errors not covered by other codes.
// The returned error wraps ErrInternalErrorSentinel for use with errors.Is().
func ErrInternalError(data interface{}) *Error {
	return (&Error{Code: CodeInternalError, Message: "Internal error", Data: data}).
		WithWrappedError(ErrInternalErrorSentinel)
}
