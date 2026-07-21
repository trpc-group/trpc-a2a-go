// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main sends a message with custom HTTP headers so the middleware
// demo server can echo them back from ProcessMessage.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/client"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

func main() {
	agentURL := flag.String("agent-url", "http://localhost:8080/", "A2A agent base URL")
	requestID := flag.String("request-id", "req-demo-001", "value for X-Request-ID")
	userID := flag.String("user-id", "alice", "value for X-User-ID")
	text := flag.String("text", "hello middleware", "message text")
	flag.Parse()

	c, err := client.NewA2AClient(*agentURL, client.WithTimeout(30*time.Second))
	if err != nil {
		log.Fatalf("create client: %v", err)
	}

	resp, err := c.SendMessage(context.Background(),
		protocol.SendMessageParams{
			Message: protocol.NewMessage(
				protocol.MessageRoleUser,
				[]*protocol.Part{protocol.NewTextPart(*text)},
			),
		},
		client.WithRequestHeader("X-Request-ID", *requestID),
		client.WithRequestHeader("X-User-ID", *userID),
	)
	if err != nil {
		log.Fatalf("SendMessage: %v", err)
	}

	switch {
	case resp.GetMessage() != nil:
		fmt.Printf("reply: %s\n", firstText(resp.GetMessage().Parts))
	case resp.GetTask() != nil:
		fmt.Printf("task state=%s\n", resp.GetTask().Status.State)
		if msg := resp.GetTask().Status.Message; msg != nil {
			fmt.Printf("reply: %s\n", firstText(msg.Parts))
		}
	default:
		log.Fatal("empty SendMessage result")
	}
}

func firstText(parts []*protocol.Part) string {
	for _, part := range parts {
		if t := part.TextContent(); t != "" {
			return t
		}
	}
	return ""
}
