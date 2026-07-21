// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main shows how WithMiddleware can put HTTP headers into the request
// context so MessageProcessor.ProcessMessage can read them via ctx.Value.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

// Header names the client sends and the middleware copies into context.
const (
	headerRequestID = "X-Request-ID"
	headerUserID    = "X-User-ID"
)

// Typed context keys keep values from colliding with other packages.
type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyUserID
)

// headerMiddleware copies selected HTTP headers onto the request context.
// TaskManager detaches cancellation from the HTTP request but keeps values,
// so ProcessMessage can still read them.
type headerMiddleware struct{}

func (headerMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if v := r.Header.Get(headerRequestID); v != "" {
			ctx = context.WithValue(ctx, ctxKeyRequestID, v)
		}
		if v := r.Header.Get(headerUserID); v != "" {
			ctx = context.WithValue(ctx, ctxKeyUserID, v)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type echoProcessor struct{}

func (echoProcessor) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	handle := taskmanager.NewTaskHandle(ctx, ec)
	defer handle.Close()

	requestID, _ := ctx.Value(ctxKeyRequestID).(string)
	userID, _ := ctx.Value(ctxKeyUserID).(string)
	text := extractText(ec.Message)

	log.Infof("processor got headers: requestID=%q userID=%q text=%q",
		requestID, userID, text)

	reply := fmt.Sprintf(
		"echo=%q requestID=%q userID=%q",
		text, requestID, userID,
	)
	handle.Reply(protocol.NewAgentText(reply))
	return handle.Events(), nil
}

func extractText(message protocol.Message) string {
	for _, part := range message.Parts {
		if t := part.TextContent(); t != "" {
			return t
		}
	}
	return ""
}

func boolPtr(b bool) *bool { return &b }

func main() {
	host := flag.String("host", "localhost", "Host to listen on")
	port := flag.Int("port", 8080, "Port to listen on")
	flag.Parse()

	agentCard := server.AgentCard{
		Name:        "Middleware Header Demo",
		Description: "Shows WithMiddleware forwarding HTTP headers to ProcessMessage",
		URL:         fmt.Sprintf("http://%s:%d/", *host, *port),
		Version:     "1.0.0",
		Capabilities: server.AgentCapabilities{
			Streaming: boolPtr(false),
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills: []server.AgentSkill{
			{
				ID:   "echo_with_headers",
				Name: "Echo with headers",
				Tags: []string{"demo", "middleware"},
			},
		},
	}

	tm, err := memory.NewTaskManager(echoProcessor{})
	if err != nil {
		log.Fatalf("create task manager: %v", err)
	}

	srv, err := server.NewA2AServer(tm,
		server.WithAgentCard(agentCard),
		server.WithMiddleware(headerMiddleware{}),
	)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		addr := fmt.Sprintf("%s:%d", *host, *port)
		log.Infof("listening on %s (send %s / %s)", addr, headerRequestID, headerUserID)
		if err := srv.Start(addr); err != nil {
			log.Fatalf("server failed: %v", err)
		}
	}()

	sig := <-sigChan
	log.Infof("received %v, shutting down", sig)
}
