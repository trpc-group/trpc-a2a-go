// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main demonstrates the client side of an input-required continuation.
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
	agentURL := flag.String("agent", "http://localhost:8080/", "A2A agent URL")
	answer := flag.String("answer", "Alice", "answer sent in the continuation")
	flag.Parse()

	a2aClient, err := client.NewA2AClient(
		*agentURL,
		client.WithTimeout(30*time.Second),
	)
	if err != nil {
		log.Fatalf("create A2A client: %v", err)
	}

	ctx := context.Background()
	contextID := protocol.GenerateContextID()

	// Round 1 starts a new task. There is no task ID on the first message.
	first, err := a2aClient.SendMessage(ctx, protocol.SendMessageParams{
		Message: protocol.NewMessageWithContext(
			protocol.MessageRoleUser,
			[]*protocol.Part{protocol.NewTextPart("Please introduce me")},
			nil,
			&contextID,
		),
	})
	if err != nil {
		log.Fatalf("send first message: %v", err)
	}
	suspended := first.GetTask()
	if suspended == nil {
		log.Fatalf("first response is not a task: %+v", first)
	}
	if suspended.Status.State != protocol.TaskStateInputRequired {
		log.Fatalf("first task state = %s, want %s",
			suspended.Status.State, protocol.TaskStateInputRequired)
	}

	fmt.Printf("round 1: task=%s state=%s\n", suspended.ID, suspended.Status.State)
	fmt.Printf("agent: %s\n", messageText(suspended.Status.Message))

	// Round 2 is a continuation because it carries the suspended task's ID.
	// Reusing only ContextID would start a separate task instead.
	taskID := suspended.ID
	contextID = suspended.ContextID
	second, err := a2aClient.SendMessage(ctx, protocol.SendMessageParams{
		Message: protocol.NewMessageWithContext(
			protocol.MessageRoleUser,
			[]*protocol.Part{protocol.NewTextPart(*answer)},
			&taskID,
			&contextID,
		),
	})
	if err != nil {
		log.Fatalf("send continuation: %v", err)
	}
	completed := second.GetTask()
	if completed == nil {
		log.Fatalf("continuation response is not a task: %+v", second)
	}

	fmt.Printf("round 2: task=%s state=%s\n", completed.ID, completed.Status.State)
	fmt.Printf("agent: %s\n", messageText(completed.Status.Message))
}

func messageText(message *protocol.Message) string {
	if message == nil {
		return ""
	}
	for _, part := range message.Parts {
		if text := part.TextContent(); text != "" {
			return text
		}
	}
	return ""
}
