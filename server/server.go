// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package server provides the A2A server implementation.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/auth"
	"trpc.group/trpc-go/trpc-a2a-go/internal/jsonrpc"
	"trpc.group/trpc-go/trpc-a2a-go/internal/sse"
	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

var errUnknownEvent = errors.New("unknown event type")

// A2AServer implements the HTTP server for the A2A protocol.
// It handles agent card requests and routes JSON-RPC calls to the TaskManager.
type A2AServer struct {
	agentCard       AgentCard               // Metadata for this agent.
	taskManager     taskmanager.TaskManager // Handles task logic.
	httpServer      *http.Server            // Underlying HTTP server.
	corsEnabled     bool                    // Flag to enable/disable CORS headers.
	jsonRPCEndpoint string                  // Path for the JSON-RPC endpoint.
	agentCardPath   string                  // Path for the agent card endpoint.
	readTimeout     time.Duration           // HTTP server read timeout.
	writeTimeout    time.Duration           // HTTP server write timeout.
	idleTimeout     time.Duration           // HTTP server idle timeout.

	// Authentication related fields
	authProvider   auth.Provider                       // Authentication provider.
	authMiddleware *auth.Middleware                    // Authentication middleware.
	pushAuth       *auth.PushNotificationAuthenticator // Push notification authenticator.
	jwksEnabled    bool                                // Flag to enable/disable JWKS endpoint.
	jwksEndpoint   string                              // Path for the JWKS endpoint.
}

// NewA2AServer creates a new A2AServer instance with the given agent card
// and task manager implementation.
// Exported function.
func NewA2AServer(agentCard AgentCard, taskManager taskmanager.TaskManager, opts ...Option) (*A2AServer, error) {
	if taskManager == nil {
		return nil, errors.New("NewA2AServer requires a non-nil taskManager")
	}
	server := &A2AServer{
		agentCard:       agentCard,
		taskManager:     taskManager,
		corsEnabled:     true, // Enable CORS by default for easier development.
		jsonRPCEndpoint: protocol.DefaultJSONRPCPath,
		agentCardPath:   protocol.AgentCardPath,
		readTimeout:     defaultReadTimeout,
		writeTimeout:    defaultWriteTimeout,
		idleTimeout:     defaultIdleTimeout,
		jwksEnabled:     false,
		jwksEndpoint:    protocol.JWKSPath,
	}
	for _, opt := range opts {
		opt(server)
	}
	// Initialize authentication components if auth provider is set.
	if server.authProvider != nil {
		server.authMiddleware = auth.NewMiddleware(server.authProvider)
	}
	// Initialize push notification authenticator.
	if server.jwksEnabled {
		if server.pushAuth == nil {
			// Only generate a new authenticator if one wasn't supplied
			server.pushAuth = auth.NewPushNotificationAuthenticator()
			if err := server.pushAuth.GenerateKeyPair(); err != nil {
				return nil, fmt.Errorf("failed to generate JWKS key pair: %w", err)
			}
		}
	}
	return server, nil
}

// Start begins listening for HTTP requests on the specified network address.
// It blocks until the server is stopped via Stop() or an error occurs.
func (s *A2AServer) Start(address string) error {
	s.httpServer = &http.Server{
		Addr:         address,
		Handler:      s.Handler(),
		ReadTimeout:  s.readTimeout,
		WriteTimeout: s.writeTimeout,
		IdleTimeout:  s.idleTimeout,
	}

	log.Infof("Starting A2A server listening on %s...", address)
	// ListenAndServe blocks. It returns http.ErrServerClosed on graceful shutdown.
	if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("http server ListenAndServe error: %w", err)
	}
	log.Info("A2A server stopped.")
	return nil
}

// Stop gracefully shuts down the running HTTP server.
// It waits for active connections to finish within the provided context's deadline.
func (s *A2AServer) Stop(ctx context.Context) error {
	if s.httpServer == nil {
		return errors.New("A2A server not running")
	}
	log.Info("Attempting graceful shutdown of A2A server...")
	if err := s.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("http server shutdown failed: %w", err)
	}
	log.Info("A2A server shutdown complete.")
	return nil
}

// Handler returns an http.Handler for the server.
// This can be used to integrate the A2A server into existing HTTP servers.
func (s *A2AServer) Handler() http.Handler {
	router := http.NewServeMux()
	// Endpoint for agent metadata (.well-known convention).
	router.HandleFunc(s.agentCardPath, s.handleAgentCard)
	// JWKS endpoint for JWT authentication if enabled.
	if s.jwksEnabled && s.pushAuth != nil {
		router.HandleFunc(s.jwksEndpoint, s.pushAuth.HandleJWKS)
	}
	// Main JSON-RPC endpoint (configurable path) with optional authentication.
	if s.authMiddleware != nil {
		// Apply authentication middleware to JSON-RPC endpoint.
		router.Handle(s.jsonRPCEndpoint, s.authMiddleware.Wrap(http.HandlerFunc(s.handleJSONRPC)))
	} else {
		// No authentication required.
		router.HandleFunc(s.jsonRPCEndpoint, s.handleJSONRPC)
	}
	return router
}

// handleAgentCard serves the agent's metadata card as JSON.
// Corresponds to GET /.well-known/agent.json in A2A Spec.
func (s *A2AServer) handleAgentCard(w http.ResponseWriter, r *http.Request) {
	if s.corsEnabled {
		setCORSHeaders(w)
	}
	if r.Method != http.MethodGet {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(s.agentCard); err != nil {
		log.Errorf("Failed to encode agent card: %v", err)
		// Avoid writing JSON-RPC error here; it's a standard HTTP endpoint.
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
}

// handleJSONRPC is the main handler for all JSON-RPC 2.0 requests.
// Routes methods like tasks/send, tasks/get, etc., as defined in A2A Spec.
func (s *A2AServer) handleJSONRPC(w http.ResponseWriter, r *http.Request) {
	// --- CORS Handling ---
	if s.corsEnabled {
		setCORSHeaders(w)
		// Handle browser preflight requests.
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	// Validate request basics
	if !s.validateJSONRPCRequest(w, r) {
		return
	}

	// Read and parse JSON-RPC request
	request, err := s.parseJSONRPCRequest(w, r.Body)
	if err != nil {
		return
	}

	// Route to appropriate handler based on method
	s.routeJSONRPCMethod(r.Context(), w, request)
}

// validateJSONRPCRequest validates basic HTTP requirements for JSON-RPC.
// Returns true if valid, writes error and returns false if invalid.
func (s *A2AServer) validateJSONRPCRequest(w http.ResponseWriter, r *http.Request) bool {
	// Check HTTP method
	if r.Method != http.MethodPost {
		s.writeJSONRPCError(w, nil,
			jsonrpc.ErrMethodNotFound(fmt.Sprintf("HTTP method %s not allowed, use POST", r.Method)))
		return false
	}

	// Check Content-Type using mime parsing
	contentType := r.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "application/json" {
		log.Warnf("Rejecting request due to invalid Content-Type: '%s' (Parse Err: %v)", contentType, err)
		s.writeJSONRPCError(w, nil,
			jsonrpc.ErrInvalidRequest(
				fmt.Sprintf("Content-Type header must be application/json, got: %s", contentType)))
		return false
	}

	return true
}

// parseJSONRPCRequest reads the request body and parses it into a JSON-RPC request.
// Returns the request and nil if successful, or nil and error if parsing failed.
func (s *A2AServer) parseJSONRPCRequest(w http.ResponseWriter, body io.ReadCloser) (jsonrpc.Request, error) {
	var request jsonrpc.Request

	// Read the request body
	bodyBytes, err := io.ReadAll(body)
	if err != nil {
		s.writeJSONRPCError(w, nil,
			jsonrpc.ErrParseError(fmt.Sprintf("failed to read request body: %v", err)))
		return request, err
	}

	// It's important to close the body, even though ReadAll consumes it
	defer body.Close()

	// Parse the JSON request
	if err := json.Unmarshal(bodyBytes, &request); err != nil {
		s.writeJSONRPCError(w, nil,
			jsonrpc.ErrParseError(fmt.Sprintf("failed to parse JSON request: %v", err)))
		return request, err
	}

	// Validate JSON-RPC version
	if request.JSONRPC != jsonrpc.Version {
		s.writeJSONRPCError(w, request.ID,
			jsonrpc.ErrInvalidRequest(fmt.Sprintf("jsonrpc field must be '%s'", jsonrpc.Version)))
		return request, fmt.Errorf("invalid JSON-RPC version")
	}

	return request, nil
}

// routeJSONRPCMethod routes the request to the appropriate handler based on the method.
func (s *A2AServer) routeJSONRPCMethod(ctx context.Context, w http.ResponseWriter, request jsonrpc.Request) {
	log.Infof("Received JSON-RPC request (ID: %v, Method: %s)", request.ID, request.Method)

	switch request.Method {

	case protocol.MethodMessageSend: // A2A Spec: message/send
		s.handleMessageSend(ctx, w, request)
	case protocol.MethodMessageStream: // A2A Spec: message/stream
		s.handleMessageStream(ctx, w, request)
	case protocol.MethodTasksPushNotificationConfigGet: // A2A Spec: tasks/pushNotification/config/get
		s.handleTasksPushNotificationGet(ctx, w, request)
	case protocol.MethodTasksPushNotificationConfigSet: // A2A Spec: tasks/pushNotification/config/set
		s.handleTasksPushNotificationSet(ctx, w, request)
	case protocol.MethodTasksGet: // A2A Spec: tasks/get
		s.handleTasksGet(ctx, w, request)
	case protocol.MethodTasksCancel: // A2A Spec: tasks/cancel
		s.handleTasksCancel(ctx, w, request)
	case protocol.MethodTasksResubscribe: // A2A Spec: tasks/resubscribe
		s.handleTasksResubscribe(ctx, w, request)

	// deprecated methods:
	case protocol.MethodTasksSend: // A2A Spec: message/send
		s.handleTasksSend(ctx, w, request)
	case protocol.MethodTasksSendSubscribe: // A2A Spec: message/sendSubscribe
		s.handleTasksSendSubscribe(ctx, w, request)
	case protocol.MethodTasksPushNotificationGet: // A2A Spec: tasks/pushNotification/get
		s.handleTasksPushNotificationGet(ctx, w, request)
	case protocol.MethodTasksPushNotificationSet: // A2A Spec: tasks/pushNotification/config/set
		s.handleTasksPushNotificationSet(ctx, w, request)
	default:
		log.Warnf("Method not found: %s (Request ID: %v)", request.Method, request.ID)
		s.writeJSONRPCError(w, request.ID,
			jsonrpc.ErrMethodNotFound(fmt.Sprintf("method '%s' not supported", request.Method)))
	}
}

// unmarshalParams is a helper function to unmarshal JSON-RPC params into the provided struct.
// It returns an error if unmarshalling fails, which is already formatted as a JSON-RPC error.
func (s *A2AServer) unmarshalParams(params json.RawMessage, v interface{}) *jsonrpc.Error {
	if err := json.Unmarshal(params, v); err != nil {
		return jsonrpc.ErrInvalidParams(fmt.Sprintf("failed to parse params: %v", err))
	}
	return nil
}

// handleTasksSend handles the tasks_send method.
func (s *A2AServer) handleTasksSend(ctx context.Context, w http.ResponseWriter, request jsonrpc.Request) {
	var params protocol.SendTaskParams
	if err := s.unmarshalParams(request.Params, &params); err != nil {
		s.writeJSONRPCError(w, request.ID, err)
		return
	}

	// Validate required fields
	if params.ID == "" {
		s.writeJSONRPCError(w, request.ID, jsonrpc.ErrInvalidParams("task ID is required"))
		return
	}
	if params.Message.Role == "" || len(params.Message.Parts) == 0 {
		s.writeJSONRPCError(w, request.ID, jsonrpc.ErrInvalidParams("message with at least one part is required"))
		return
	}

	// Delegate to the task manager.
	task, err := s.taskManager.OnSendTask(ctx, params)
	if err != nil {
		log.Errorf("Error calling OnSendTask for task %s: %v", params.ID, err)
		// Check if it's already a JSON-RPC error
		if rpcErr, ok := err.(*jsonrpc.Error); ok {
			s.writeJSONRPCError(w, request.ID, rpcErr)
		} else {
			// Otherwise, wrap as internal error
			s.writeJSONRPCError(w, request.ID,
				jsonrpc.ErrInternalError(fmt.Sprintf("task processing failed: %v", err)))
		}
		return
	}
	s.writeJSONRPCResponse(w, request.ID, task)
}

// handleTasksGet handles the tasks_get method.
func (s *A2AServer) handleTasksGet(ctx context.Context, w http.ResponseWriter, request jsonrpc.Request) {
	var params protocol.TaskQueryParams
	if err := s.unmarshalParams(request.Params, &params); err != nil {
		s.writeJSONRPCError(w, request.ID, err)
		return
	}
	task, err := s.taskManager.OnGetTask(ctx, params)
	if err != nil {
		// Check if the error is already a JSONRPCError (e.g., TaskNotFound).
		if rpcErr, ok := err.(*jsonrpc.Error); ok {
			log.Errorf("Error calling OnGetTask for task %s: %v", params.ID, rpcErr)
			s.writeJSONRPCError(w, request.ID, rpcErr)
		} else {
			// Otherwise, wrap it as a generic internal error.
			log.Errorf("Unexpected error calling OnGetTask for task %s: %v", params.ID, err)
			s.writeJSONRPCError(w, request.ID,
				jsonrpc.ErrInternalError(fmt.Sprintf("failed to get task: %v", err)))
		}
		return
	}
	s.writeJSONRPCResponse(w, request.ID, task)
}

// handleTasksCancel handles the tasks_cancel method.
func (s *A2AServer) handleTasksCancel(ctx context.Context, w http.ResponseWriter, request jsonrpc.Request) {
	var params protocol.TaskIDParams
	if err := s.unmarshalParams(request.Params, &params); err != nil {
		s.writeJSONRPCError(w, request.ID, err)
		return
	}
	task, err := s.taskManager.OnCancelTask(ctx, params)
	if err != nil {
		if rpcErr, ok := err.(*jsonrpc.Error); ok {
			log.Errorf("Error calling OnCancelTask for task %s: %v", params.ID, rpcErr)
			s.writeJSONRPCError(w, request.ID, rpcErr)
		} else {
			log.Errorf("Unexpected error calling OnCancelTask for task %s: %v", params.ID, err)
			s.writeJSONRPCError(w, request.ID,
				jsonrpc.ErrInternalError(fmt.Sprintf("failed to cancel task: %v", err)))
		}
		return
	}
	s.writeJSONRPCResponse(w, request.ID, task)
}

// handleTasksSendSubscribe handles the tasks_sendSubscribe method using Server-Sent Events (SSE).
func (s *A2AServer) handleTasksSendSubscribe(ctx context.Context, w http.ResponseWriter, request jsonrpc.Request) {
	var params protocol.SendTaskParams
	if err := s.unmarshalParams(request.Params, &params); err != nil {
		s.writeJSONRPCError(w, request.ID, err)
		return
	}

	// Validate required fields.
	if params.ID == "" {
		s.writeJSONRPCError(w, request.ID, jsonrpc.ErrInvalidParams("task ID is required"))
		return
	}
	if params.Message.Role == "" || len(params.Message.Parts) == 0 {
		s.writeJSONRPCError(w, request.ID, jsonrpc.ErrInvalidParams("message with at least one part is required"))
		return
	}

	// Check if client supports SSE.
	// Since we're in a JSON-RPC context, we can't directly access the HTTP Accept header.
	// We'll assume the client wants SSE since they called the sendSubscribe method.
	// In a real implementation, this could be determined by looking at the HTTP request directly.

	// Client wants SSE response.
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Error("Streaming is not supported by the underlying http responseWriter")
		s.writeJSONRPCError(w, request.ID, jsonrpc.ErrInternalError("server does not support streaming"))
		return
	}

	// Get the event channel from the task manager.
	eventsChan, err := s.taskManager.OnSendTaskSubscribe(ctx, params)
	if err != nil {
		log.Errorf("Error calling OnSendTaskSubscribe for task %s: %v", params.ID, err)
		s.writeJSONRPCError(w, request.ID,
			jsonrpc.ErrInternalError(fmt.Sprintf("failed to subscribe to task events: %v", err)))
		return
	}

	// Use the helper function to handle the SSE stream
	handleSSEStream(ctx, s.corsEnabled, w, flusher, eventsChan, request.ID.(string), false)
}

// writeJSONRPCResponse encodes and writes a successful JSON-RPC response.
func (s *A2AServer) writeJSONRPCResponse(w http.ResponseWriter, id interface{}, result interface{}) {
	response := jsonrpc.NewResponse(id, result)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK) // Success is always 200 OK for JSON-RPC itself.
	if err := json.NewEncoder(w).Encode(response); err != nil {
		// Log error, but can't change response if headers are already sent.
		log.Errorf("Failed to write JSON-RPC success response (ID: %v): %v", id, err)
	}
}

// writeJSONRPCError encodes and writes a JSON-RPC error response.
// It attempts to set an appropriate HTTP status code based on the JSON-RPC error code.
func (s *A2AServer) writeJSONRPCError(w http.ResponseWriter, id interface{}, err *jsonrpc.Error) {
	if err == nil {
		// Should not happen, but handle defensively.
		err = jsonrpc.ErrInternalError("writeJSONRPCError called with nil error")
		log.Errorf("Programming ERROR: writeJSONRPCError called with nil error (Request ID: %v)", id)
	}
	response := jsonrpc.NewErrorResponse(id, err)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Map JSON-RPC error codes to HTTP status codes where appropriate.
	httpStatus := http.StatusInternalServerError // Default for Internal errors.
	switch err.Code {
	case jsonrpc.CodeParseError:
		httpStatus = http.StatusBadRequest
	case jsonrpc.CodeInvalidRequest:
		httpStatus = http.StatusBadRequest
	case jsonrpc.CodeMethodNotFound:
		httpStatus = http.StatusNotFound
	case jsonrpc.CodeInvalidParams:
		httpStatus = http.StatusBadRequest
		// Add other mappings for custom server errors (-32000 to -32099) if desired.
	}
	w.WriteHeader(httpStatus)
	if encodeErr := json.NewEncoder(w).Encode(response); encodeErr != nil {
		// Log error, but can't change response now.
		log.Errorf("Failed to write JSON-RPC error response (ID: %v, Code: %d): %v", id, err.Code, encodeErr)
	}
}

// setCORSHeaders adds permissive CORS headers for development/testing.
// WARNING: This is insecure for production. Configure origins explicitly.
func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*") // INSECURE
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	// Max-Age might be useful but not strictly necessary here.
}

func (s *A2AServer) handleTasksPushNotificationSet(
	ctx context.Context,
	w http.ResponseWriter,
	request jsonrpc.Request,
) {
	var params protocol.TaskPushNotificationConfig
	if err := s.unmarshalParams(request.Params, &params); err != nil {
		s.writeJSONRPCError(w, request.ID, err)
		return
	}
	// Validate required fields.
	if params.TaskID == "" {
		s.writeJSONRPCError(w, request.ID, jsonrpc.ErrInvalidParams("task ID is required"))
		return
	}
	if params.PushNotificationConfig.URL == "" {
		s.writeJSONRPCError(w, request.ID, jsonrpc.ErrInvalidParams("push notification URL is required"))
		return
	}
	// Process authentication related fields for push notifications.
	if s.jwksEnabled && s.pushAuth != nil {
		// Add JWT support by indicating the auth scheme in the config.
		if params.PushNotificationConfig.Authentication == nil {
			params.PushNotificationConfig.Authentication = &protocol.AuthenticationInfo{
				Schemes: []string{"bearer"},
			}
		} else {
			// Ensure "bearer" is in the list of supported schemes.
			hasBearer := false
			for _, scheme := range params.PushNotificationConfig.Authentication.Schemes {
				if scheme == "bearer" {
					hasBearer = true
					break
				}
			}
			if !hasBearer {
				params.PushNotificationConfig.Authentication.Schemes = append(
					params.PushNotificationConfig.Authentication.Schemes,
					"bearer",
				)
			}
		}
		// Set JWKS endpoint information.
		// This will be used by the client to verify JWTs sent by this server.
		jwksURL := s.composeJWKSURL()
		log.Infof("JWKS URL for push notifications: %s", jwksURL)
		// Store JWKS URL in the params for the task manager to use.
		if params.PushNotificationConfig.Metadata == nil {
			params.PushNotificationConfig.Metadata = make(map[string]interface{})
		}
		params.PushNotificationConfig.Metadata["jwksUrl"] = jwksURL
	}

	// Delegate to the task manager.
	result, err := s.taskManager.OnPushNotificationSet(ctx, params)
	if err != nil {
		log.Errorf("Error calling OnPushNotificationSet for task %s: %v", params.TaskID, err)
		// Check if the error is already a JSONRPCError.
		if rpcErr, ok := err.(*jsonrpc.Error); ok {
			s.writeJSONRPCError(w, request.ID, rpcErr)
		} else {
			s.writeJSONRPCError(w, request.ID,
				jsonrpc.ErrInternalError(fmt.Sprintf("push notification setup failed: %v", err)))
		}
		return
	}

	s.writeJSONRPCResponse(w, request.ID, result)
}

// composeJWKSURL returns the fully qualified URL to the JWKS endpoint.
func (s *A2AServer) composeJWKSURL() string {
	// Extract the base URL from the agent card.
	baseURL := s.agentCard.URL
	// If the URL already has a scheme, use it directly.
	if baseURL == "" {
		// This is a fallback, but ideally the agent card should have a proper URL.
		log.Warn("Agent card URL is empty, using relative JWKS endpoint")
		return s.jwksEndpoint
	}
	// Make sure the URL doesn't have a trailing slash.
	if len(baseURL) > 0 && baseURL[len(baseURL)-1] == '/' {
		baseURL = baseURL[:len(baseURL)-1]
	}
	// Make sure the JWKS endpoint starts with a slash.
	jwksPath := s.jwksEndpoint
	if len(jwksPath) > 0 && jwksPath[0] != '/' {
		jwksPath = "/" + jwksPath
	}
	return baseURL + jwksPath
}

func (s *A2AServer) handleTasksPushNotificationGet(
	ctx context.Context,
	w http.ResponseWriter,
	request jsonrpc.Request,
) {
	var params protocol.TaskIDParams
	if err := s.unmarshalParams(request.Params, &params); err != nil {
		s.writeJSONRPCError(w, request.ID, err)
		return
	}

	// Validate required fields.
	if params.ID == "" {
		s.writeJSONRPCError(w, request.ID, jsonrpc.ErrInvalidParams("task ID is required"))
		return
	}

	// Delegate to the task manager.
	result, err := s.taskManager.OnPushNotificationGet(ctx, params)
	if err != nil {
		log.Errorf("Error calling OnPushNotificationGet for task %s: %v", params.ID, err)
		// Check if the error is already a JSONRPCError.
		if rpcErr, ok := err.(*jsonrpc.Error); ok {
			s.writeJSONRPCError(w, request.ID, rpcErr)
		} else {
			s.writeJSONRPCError(w, request.ID,
				jsonrpc.ErrInternalError(fmt.Sprintf("failed to get push notification config: %v", err)))
		}
		return
	}

	s.writeJSONRPCResponse(w, request.ID, result)
}

func (s *A2AServer) handleTasksResubscribe(ctx context.Context, w http.ResponseWriter, request jsonrpc.Request) {
	var params protocol.TaskIDParams
	if err := s.unmarshalParams(request.Params, &params); err != nil {
		s.writeJSONRPCError(w, request.ID, err)
		return
	}

	// Validate required fields.
	if params.ID == "" {
		s.writeJSONRPCError(w, request.ID, jsonrpc.ErrInvalidParams("task ID is required"))
		return
	}

	// Ensure client is accepting SSE.
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Error("Streaming is not supported by the underlying http responseWriter")
		s.writeJSONRPCError(w, request.ID, jsonrpc.ErrInternalError("server does not support streaming"))
		return
	}

	// Get the event channel from the task manager.
	eventsChan, err := s.taskManager.OnResubscribe(ctx, params)
	if err != nil {
		log.Errorf("Error calling OnResubscribe for task %s: %v", params.ID, err)
		if rpcErr, ok := err.(*jsonrpc.Error); ok {
			s.writeJSONRPCError(w, request.ID, rpcErr)
		} else {
			s.writeJSONRPCError(w, request.ID,
				jsonrpc.ErrInternalError(fmt.Sprintf("failed to resubscribe to task events: %v", err)))
		}
		return
	}

	// Use the helper function to handle the SSE stream
	handleSSEStream[protocol.StreamingMessageEvent](ctx, s.corsEnabled, w, flusher, eventsChan, request.ID.(string), true)
}

// handleMessageSend handles the message_send method.
func (s *A2AServer) handleMessageSend(ctx context.Context, w http.ResponseWriter, request jsonrpc.Request) {
	var params protocol.SendMessageParams
	if err := s.unmarshalParams(request.Params, &params); err != nil {
		s.writeJSONRPCError(w, request.ID, err)
		return
	}
	// Delegate to the task manager.
	message, err := s.taskManager.OnSendMessage(ctx, params)
	if err != nil {
		log.Errorf("Error calling OnSendMessage for message %s: %v", params.RPCID, err)
		// Check if it's already a JSON-RPC error
		if rpcErr, ok := err.(*jsonrpc.Error); ok {
			s.writeJSONRPCError(w, request.ID, rpcErr)
		} else {
			// Otherwise, wrap as internal error
			s.writeJSONRPCError(w, request.ID,
				jsonrpc.ErrInternalError(fmt.Sprintf("message processing failed: %v", err)))
		}
		return
	}
	s.writeJSONRPCResponse(w, request.ID, message)
}

// handleMessageStream handles the message_stream method using Server-Sent Events (SSE).
func (s *A2AServer) handleMessageStream(ctx context.Context, w http.ResponseWriter, request jsonrpc.Request) {
	var params protocol.SendMessageParams
	if err := s.unmarshalParams(request.Params, &params); err != nil {
		s.writeJSONRPCError(w, request.ID, err)
		return
	}

	if params.Message.Role == "" || len(params.Message.Parts) == 0 {
		s.writeJSONRPCError(w, request.ID, jsonrpc.ErrInvalidParams("message with at least one part is required"))
		return
	}

	// Check if client supports SSE.
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Error("Streaming is not supported by the underlying http responseWriter")
		s.writeJSONRPCError(w, request.ID, jsonrpc.ErrInternalError("server does not support streaming"))
		return
	}

	// Get the event channel from the task manager.
	eventsChan, err := s.taskManager.OnSendMessageStream(ctx, params)
	if err != nil {
		log.Errorf("Error calling OnSendMessageStream for message %s: %v", params.RPCID, err)
		s.writeJSONRPCError(w, request.ID,
			jsonrpc.ErrInternalError(fmt.Sprintf("failed to subscribe to message events: %v", err)))
		return
	}

	// Use the helper function to handle the SSE stream
	handleSSEStream(ctx, s.corsEnabled, w, flusher, eventsChan, request.ID.(string), false)
}

// handleSSEStream handles an SSE stream for a task, including setup and event forwarding.
// It sets the appropriate headers, logs connection status, and forwards events to the client.
func handleSSEStream[T interface{}](
	ctx context.Context,
	corsEnabled bool,
	w http.ResponseWriter,
	flusher http.Flusher,
	eventsChan <-chan T,
	rpcID string,
	isResubscribe bool,
) {
	// Set headers for SSE.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	if corsEnabled {
		setCORSHeaders(w)
	}

	// Indicate successful subscription setup.
	w.WriteHeader(http.StatusOK)
	flusher.Flush() // Send headers immediately.

	// Log appropriate message based on whether this is a new subscription or resubscribe
	if isResubscribe {
		log.Infof("SSE stream reopened for request ID: %v)", rpcID)
	} else {
		log.Infof("SSE stream opened for request ID: %v)", rpcID)
	}

	// Use request context to detect client disconnection.
	clientClosed := ctx.Done()

	// --- Event Forwarding Loop ---
	for {
		select {
		case event, ok := <-eventsChan:
			if !ok {
				// Channel closed by task manager (task finished or error).
				log.Infof("SSE stream closing request ID: %s", rpcID)
				// Send a final SSE event indicating closure.
				closeData := sse.CloseEventData{
					ID:     rpcID,
					Reason: "task ended",
				}
				// Use JSON-RPC format for the close event
				if err := sse.FormatJSONRPCEvent(w, protocol.EventClose, rpcID, closeData); err != nil {
					log.Errorf("Error writing SSE JSON-RPC close event for request ID: %s: %v", rpcID, err)
				} else {
					flusher.Flush()
				}
				return // End the handler.
			}
			if err := sendSSEEvent(w, rpcID, flusher, event); err != nil {
				if err == errUnknownEvent {
					log.Warnf("Unknown event type received for request ID: %s: %T. Skipping.", rpcID, event)
					continue
				}
				log.Errorf("Error writing SSE JSON-RPC event for request ID: %s (client likely disconnected): %v", rpcID, err)
				return
			}
			// Flush the buffer to ensure the event is sent immediately.
			flusher.Flush()
		case <-clientClosed:
			// Client disconnected (request context canceled).
			log.Infof("SSE client disconnected for request ID: %s. Closing stream.", rpcID)
			return // Exit the handler.
		}
	}
}

func sendSSEEvent(w http.ResponseWriter, rpcID string, flusher http.Flusher, event interface{}) error {
	// Determine event type string for SSE.
	var eventType string
	var actualEvent protocol.Event

	// Handle StreamingMessageEvent by extracting the inner Result
	if streamEvent, ok := event.(protocol.StreamingMessageEvent); ok {
		actualEvent = streamEvent.Result
	} else if directEvent, ok := event.(protocol.Event); ok {
		actualEvent = directEvent
	} else {
		return errUnknownEvent
	}

	switch actualEvent.(type) {
	case *protocol.TaskStatusUpdateEvent:
		eventType = protocol.EventStatusUpdate
	case *protocol.TaskArtifactUpdateEvent:
		eventType = protocol.EventArtifactUpdate
	case *protocol.Message:
		eventType = protocol.EventMessage
	case *protocol.Task:
		eventType = protocol.EventTask
	default:
		return errUnknownEvent
	}

	// For StreamMessage API, we need to send the event wrapped in StreamingMessageEvent
	// Write the event to the SSE stream using JSON-RPC format.
	if err := sse.FormatJSONRPCEvent(w, eventType, rpcID, event); err != nil {
		return err
	}
	return nil
}
