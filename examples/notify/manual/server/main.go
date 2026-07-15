// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Command server runs an A2A agent that delivers push notifications manually.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push/pushauth"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

const defaultPort = 8000

// worker sends notifications itself at moments chosen by the agent. The
// TaskManager runs with automatic delivery disabled.
type worker struct {
	sender *pushauth.SignedSender
}

func (p *worker) ProcessMessage(
	ctx context.Context, ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	h := taskmanager.NewTaskHandle(ctx, ec)
	cfg := ec.PushConfig
	log.Printf("task %s received; agent controls delivery", ec.TaskID)
	go func() {
		defer h.Close()
		h.UpdateTaskState(protocol.TaskStateWorking, protocol.NewAgentText("working..."))
		log.Printf("task %s -> working", ec.TaskID)

		time.Sleep(300 * time.Millisecond)
		log.Printf("task %s milestone reached; sending manual push", ec.TaskID)
		p.send(ctx, cfg, ec.TaskID, protocol.TaskStateWorking, "milestone: halfway done")

		time.Sleep(300 * time.Millisecond)
		h.UpdateTaskState(protocol.TaskStateCompleted, protocol.NewAgentText("done"))
		log.Printf("task %s -> completed; sending final manual push", ec.TaskID)
		p.send(ctx, cfg, ec.TaskID, protocol.TaskStateCompleted, "all done")
	}()
	return h.Events(), nil
}

func (p *worker) send(
	ctx context.Context,
	cfg *protocol.TaskPushNotificationConfig,
	taskID string,
	state protocol.TaskState,
	note string,
) {
	if cfg == nil {
		log.Printf("task %s has no webhook; skipping push", taskID)
		return
	}
	event := protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{
		TaskID: taskID,
		Status: protocol.TaskStatus{State: state, Message: protocol.NewAgentText(note)},
	})
	if err := p.sender.SendPush(ctx, *cfg, event); err != nil {
		log.Printf("manual push failed: %v", err)
	}
}

func main() {
	port := flag.Int("port", defaultPort, "A2A server port")
	flag.Parse()
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	// This local demo intentionally posts to a loopback client webhook.
	sender, err := pushauth.NewSignedSender(
		pushauth.WithSenderOptions(push.WithUnsafeAllowPrivateNetworks()))
	if err != nil {
		log.Fatalf("create signed sender: %v", err)
	}
	tm, err := memory.NewTaskManager(&worker{sender: sender},
		memory.WithPushNotifications(push.Config{ManualDelivery: true}),
	)
	if err != nil {
		log.Fatalf("create task manager: %v", err)
	}
	defer tm.Close()

	agentURL := fmt.Sprintf("http://localhost:%d", *port)
	card := server.AgentCard{Name: "manual-notify", URL: agentURL, Version: "1.0.0"}
	srv, err := server.NewA2AServer(tm,
		server.WithAgentCard(card),
		server.WithPushNotificationJWKSHandler(sender.JWKSHandler()),
	)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	log.Printf("agent listening at %s (automatic delivery disabled)", agentURL)
	log.Printf("JWKS published at %s", agentURL+protocol.JWKSPath)
	if err := srv.Start(fmt.Sprintf(":%d", *port)); err != nil {
		log.Fatalf("start server: %v", err)
	}
}
