// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements the root agent in the multi-agent example.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/client"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

type rootProcessor struct {
	creative      *client.A2AClient
	exchange      *client.A2AClient
	reimbursement *client.A2AClient
}

func (p *rootProcessor) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	handle := taskmanager.NewTaskHandle(ctx, ec)
	defer handle.Close()

	text := strings.TrimSpace(messageText(ec.Message))
	if text == "" {
		_ = handle.Reply(protocol.NewAgentText("input message must contain text"))
		return handle.Events(), nil
	}

	agentName, agentClient := p.route(text)
	if agentClient == nil {
		_ = handle.Reply(protocol.NewAgentText(
			"Choose a creative-writing, currency-exchange, or reimbursement request.",
		))
		return handle.Events(), nil
	}

	contextID := handle.GetContextID()
	message := protocol.NewMessageWithContext(
		protocol.MessageRoleUser,
		[]*protocol.Part{protocol.NewTextPart(text)},
		nil,
		&contextID,
	)
	response, err := agentClient.SendMessage(ctx, protocol.SendMessageParams{Message: message})
	if err != nil {
		_ = handle.Reply(protocol.NewAgentText(fmt.Sprintf("%s agent call failed: %v", agentName, err)))
		return handle.Events(), nil
	}

	result, err := responseText(response)
	if err != nil {
		_ = handle.Reply(protocol.NewAgentText(fmt.Sprintf("%s agent returned no text: %v", agentName, err)))
		return handle.Events(), nil
	}
	_ = handle.Reply(protocol.NewAgentText(fmt.Sprintf("Routed to %s agent:\n%s", agentName, result)))
	return handle.Events(), nil
}

func (p *rootProcessor) route(text string) (string, *client.A2AClient) {
	lower := strings.ToLower(text)
	switch {
	case containsAny(lower, "write", "story", "poem", "creative"):
		return "creative", p.creative
	case containsAny(lower, "exchange", "currency", "convert", "rate"):
		return "exchange", p.exchange
	case containsAny(lower, "reimburse", "expense", "receipt", "payment"):
		return "reimbursement", p.reimbursement
	default:
		return "", nil
	}
}

func containsAny(text string, keywords ...string) bool {
	for _, keyword := range keywords {
		if strings.Contains(text, keyword) {
			return true
		}
	}
	return false
}

func responseText(response *protocol.SendMessageResponse) (string, error) {
	if message := response.GetMessage(); message != nil {
		return messageText(*message), nil
	}
	if task := response.GetTask(); task != nil {
		if task.Status.Message != nil {
			return messageText(*task.Status.Message), nil
		}
		for i := len(task.Artifacts) - 1; i >= 0; i-- {
			if text := partText(task.Artifacts[i].Parts); text != "" {
				return text, nil
			}
		}
	}
	return "", fmt.Errorf("empty response")
}

func messageText(message protocol.Message) string {
	return partText(message.Parts)
}

func partText(parts []*protocol.Part) string {
	var result strings.Builder
	for _, part := range parts {
		result.WriteString(part.TextContent())
	}
	return result.String()
}

func main() {
	host := flag.String("host", "localhost", "host to listen on")
	port := flag.Int("port", 8080, "port to listen on")
	creativeURL := flag.String("creative-url", "http://localhost:8082/", "creative agent URL")
	exchangeURL := flag.String("exchange-url", "http://localhost:8081/", "exchange agent URL")
	reimbursementURL := flag.String("reimbursement-url", "http://localhost:8083/", "reimbursement agent URL")
	flag.Parse()

	creativeClient, err := client.NewA2AClient(*creativeURL)
	if err != nil {
		log.Fatalf("create creative agent client: %v", err)
	}
	exchangeClient, err := client.NewA2AClient(*exchangeURL)
	if err != nil {
		log.Fatalf("create exchange agent client: %v", err)
	}
	reimbursementClient, err := client.NewA2AClient(*reimbursementURL)
	if err != nil {
		log.Fatalf("create reimbursement agent client: %v", err)
	}

	processor := &rootProcessor{
		creative:      creativeClient,
		exchange:      exchangeClient,
		reimbursement: reimbursementClient,
	}
	taskManager, err := memory.NewTaskManager(processor)
	if err != nil {
		log.Fatalf("create task manager: %v", err)
	}

	address := fmt.Sprintf("%s:%d", *host, *port)
	agentURL := "http://" + address + "/"
	streaming := false
	pushNotifications := false
	description := "Routes a request to another A2A agent"
	agentCard := protocol.AgentCard{
		Name:        "Multi-Agent Router",
		Description: "Routes requests to deterministic example sub-agents",
		SupportedInterfaces: []protocol.AgentInterface{{
			URL:             agentURL,
			ProtocolBinding: "JSONRPC",
			ProtocolVersion: protocol.ProtocolVersionV1,
		}},
		Version: "2.0.0",
		Capabilities: protocol.AgentCapabilities{
			Streaming:         &streaming,
			PushNotifications: &pushNotifications,
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills: []protocol.AgentSkill{{
			ID:          "route-request",
			Name:        "Route request",
			Description: &description,
			Tags:        []string{"routing", "multi-agent", "orchestration"},
			Examples: []string{
				"Write a poem about autumn",
				"Convert USD to EUR",
				"Submit an expense receipt",
			},
		}},
	}

	srv, err := server.NewA2AServer(taskManager, server.WithAgentCard(agentCard))
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	go func() {
		log.Printf("multi-agent router listening at %s", agentURL)
		if err := srv.Start(address); err != nil {
			log.Printf("server stopped: %v", err)
		}
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	<-signals

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Stop(shutdownCtx); err != nil {
		log.Printf("stop server: %v", err)
	}
}
