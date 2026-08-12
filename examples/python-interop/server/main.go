// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent. All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements the Go side of the Python SDK interoperability example.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

const (
	tenantA = "tenant-a"
	tenantB = "tenant-b"
)

var (
	host = flag.String("host", "localhost", "Host to listen on")
	port = flag.Int("port", 8080, "Port to listen on")
)

func main() {
	flag.Parse()

	manager, err := memory.NewTaskManager(&tenantProcessor{})
	if err != nil {
		log.Fatalf("Create task manager: %v", err)
	}
	endpoint := fmt.Sprintf("http://%s:%d/", *host, *port)
	srv, err := server.NewA2AServer(
		manager,
		// Default card for /.well-known/agent-card.json when "?tenant=" is absent.
		server.WithAgentCard(defaultCard(endpoint)),
		server.WithTenantCard(tenantA, tenantCard("Go Tenant A Agent", tenantA, endpoint)),
		server.WithTenantCard(tenantB, tenantCard("Go Tenant B Agent", tenantB, endpoint)),
	)
	if err != nil {
		log.Fatalf("Create server: %v", err)
	}

	address := fmt.Sprintf("%s:%d", *host, *port)
	go func() {
		log.Infof("Go multi-tenant A2A server listening on %s", address)
		if err := srv.Start(address); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	<-signals

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Stop(ctx); err != nil {
		log.Errorf("Server shutdown failed: %v", err)
	}
}

type tenantProcessor struct{}

func (p *tenantProcessor) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	if ec.Tenant != tenantA && ec.Tenant != tenantB {
		return nil, fmt.Errorf("unknown tenant %q", ec.Tenant)
	}
	if ec.Task != nil {
		return completeContinuation(ctx, ec), nil
	}

	switch extractText(ec.Message) {
	case "profile":
		return requireProfileInput(ctx, ec), nil
	case "wait":
		return runUntilCanceled(ctx, ec), nil
	default:
		return completeSimple(ctx, ec), nil
	}
}

func requireProfileInput(ctx context.Context, ec *taskmanager.ExecContext) <-chan protocol.StreamEvent {
	handle := taskmanager.NewTaskHandle(ctx, ec)
	defer handle.Close()
	_ = handle.UpdateTaskState(protocol.TaskStateSubmitted, nil)
	_ = handle.UpdateTaskState(
		protocol.TaskStateInputRequired,
		protocol.NewAgentText(fmt.Sprintf("[%s] What name should I use?", ec.Tenant)),
	)
	return handle.Events()
}

func runUntilCanceled(ctx context.Context, ec *taskmanager.ExecContext) <-chan protocol.StreamEvent {
	handle := taskmanager.NewTaskHandle(ctx, ec)
	out := handle.Events()
	go func() {
		defer handle.Close()
		_ = handle.UpdateTaskState(protocol.TaskStateSubmitted, nil)
		_ = handle.UpdateTaskState(
			protocol.TaskStateWorking,
			protocol.NewAgentText(fmt.Sprintf("[%s] Waiting for cancellation", ec.Tenant)),
		)

		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			_ = handle.UpdateTaskState(
				protocol.TaskStateCompleted,
				protocol.NewAgentText(fmt.Sprintf("[%s] Wait finished", ec.Tenant)),
			)
		}
	}()
	return out
}

func completeSimple(ctx context.Context, ec *taskmanager.ExecContext) <-chan protocol.StreamEvent {
	handle := taskmanager.NewTaskHandle(ctx, ec)
	defer handle.Close()

	result := fmt.Sprintf("[%s] %s", ec.Tenant, strings.ToUpper(extractText(ec.Message)))
	_ = handle.UpdateTaskState(protocol.TaskStateSubmitted, nil)
	_ = handle.UpdateTaskState(protocol.TaskStateWorking, nil)
	_ = handle.AddArtifact(protocol.Artifact{
		ArtifactID: "result-" + handle.TaskID(),
		Parts:      []*protocol.Part{protocol.NewTextPart(result)},
	}, true)
	_ = handle.UpdateTaskState(protocol.TaskStateCompleted, protocol.NewAgentText(result))
	return handle.Events()
}

func completeContinuation(ctx context.Context, ec *taskmanager.ExecContext) <-chan protocol.StreamEvent {
	handle := taskmanager.NewTaskHandle(ctx, ec)
	defer handle.Close()

	result := fmt.Sprintf("[%s] Hello, %s. Task %s is complete.", ec.Tenant, extractText(ec.Message), ec.Task.ID)
	_ = handle.UpdateTaskState(protocol.TaskStateWorking, nil)
	_ = handle.AddArtifact(protocol.Artifact{
		ArtifactID: "profile-" + handle.TaskID(),
		Parts:      []*protocol.Part{protocol.NewTextPart(result)},
	}, true)
	_ = handle.UpdateTaskState(protocol.TaskStateCompleted, protocol.NewAgentText(result))
	return handle.Events()
}

func extractText(message protocol.Message) string {
	var texts []string
	for _, part := range message.Parts {
		if text := part.TextContent(); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.TrimSpace(strings.Join(texts, " "))
}

func defaultCard(endpoint string) protocol.AgentCard {
	description := "Shared Go A2A agent for the Python interoperability example"
	return protocol.AgentCard{
		Name:        "Go Shared Agent",
		Description: description,
		SupportedInterfaces: []protocol.AgentInterface{{
			URL:             endpoint,
			ProtocolBinding: "JSONRPC",
			ProtocolVersion: protocol.ProtocolVersionV1,
		}},
		Version: "1.0.0",
		Capabilities: protocol.AgentCapabilities{
			Streaming:              boolPtr(true),
			PushNotifications:      boolPtr(false),
			StateTransitionHistory: boolPtr(true),
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills: []protocol.AgentSkill{{
			ID:          "task-lifecycle",
			Name:        "Task lifecycle",
			Description: &description,
			Tags:        []string{"tasks", "tenant", "interop"},
			Examples:    []string{"hello", "profile", "wait"},
		}},
	}
}

func tenantCard(name, tenant, endpoint string) protocol.AgentCard {
	description := fmt.Sprintf("A2A task lifecycle agent for %s", tenant)
	return protocol.AgentCard{
		Name:        name,
		Description: description,
		SupportedInterfaces: []protocol.AgentInterface{{
			URL:             endpoint,
			ProtocolBinding: "JSONRPC",
			ProtocolVersion: protocol.ProtocolVersionV1,
			Tenant:          tenant,
		}},
		Version: "1.0.0",
		Capabilities: protocol.AgentCapabilities{
			Streaming:              boolPtr(true),
			PushNotifications:      boolPtr(false),
			StateTransitionHistory: boolPtr(true),
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills: []protocol.AgentSkill{{
			ID:          "task-lifecycle-" + tenant,
			Name:        "Task lifecycle",
			Description: &description,
			Tags:        []string{"tasks", "tenant", "interop"},
			Examples:    []string{"hello", "profile", "wait"},
		}},
	}
}

func boolPtr(value bool) *bool { return &value }
