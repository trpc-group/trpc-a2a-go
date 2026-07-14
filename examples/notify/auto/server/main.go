// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Command server runs an A2A agent with automatic push delivery.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push/pushauth"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

const defaultPort = 8000

// worker completes the task asynchronously. It does not send push
// notifications; the TaskManager dispatches significant events automatically.
type worker struct{}

func (worker) ProcessMessage(
	ctx context.Context, ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	h := taskmanager.NewTaskHandle(ctx, ec)
	log.Printf("task %s received; processing asynchronously", ec.TaskID)
	go func() {
		defer h.Close()
		h.UpdateTaskState(protocol.TaskStateWorking, nil)
		log.Printf("task %s -> working (heartbeat, not pushed)", ec.TaskID)
		time.Sleep(300 * time.Millisecond)
		h.UpdateTaskState(protocol.TaskStateCompleted, protocol.NewAgentText("done"))
		log.Printf("task %s -> completed; framework will auto-push", ec.TaskID)
	}()
	return h.Events(), nil
}

func main() {
	port := flag.Int("port", defaultPort, "A2A server port")
	flag.Parse()
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	notifier, err := pushauth.NewSignedSender(pushauth.WithJWT())
	if err != nil {
		log.Fatalf("create notifier: %v", err)
	}
	tm, err := memory.NewTaskManager(worker{}, memory.WithPushNotifications(notifier))
	if err != nil {
		log.Fatalf("create task manager: %v", err)
	}
	defer tm.Close()

	agentURL := fmt.Sprintf("http://localhost:%d", *port)
	card := server.AgentCard{Name: "auto-notify", URL: agentURL, Version: "1.0.0"}
	srv, err := server.NewA2AServer(tm,
		server.WithAgentCard(card),
		server.WithPushNotificationAuthenticator(notifier.Authenticator()),
	)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	log.Printf("agent listening at %s", agentURL)
	log.Printf("JWKS published at %s", agentURL+protocol.JWKSPath)
	if err := srv.Start(fmt.Sprintf(":%d", *port)); err != nil {
		log.Fatalf("start server: %v", err)
	}
}
