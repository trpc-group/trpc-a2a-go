// Package main is the entry point for a multi-agent A2A server.
//
// It hosts several agents in ONE process using the A2A v1.0 tenant model: all
// agents share a single endpoint, and the request's "tenant" field (carried in
// the JSON body) selects which agent handles it. The agent-card endpoint serves
// the right card for "?tenant=<tenant>". This replaces the older approach of
// distinguishing agents by URL path (which needed a placeholder card, a custom
// AgentCardHandler, a {agentName} path template and a chi router).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/memory"
)

var host = flag.String("host", "localhost:8080", "host")

func main() {
	flag.Parse()

	taskManager, err := memory.NewTaskManager(&multiAgentProcessor{})
	if err != nil {
		log.Fatalf("Failed to create task manager: %v", err)
	}

	baseURL := fmt.Sprintf("http://%s/", *host)

	// No default ("directory") card is needed: each agent's real card is
	// registered per tenant with WithTenantCard, and the server is addressed by
	// the v1.0 tenant field. For a set of agents not known at startup, use
	// server.WithTenantCardProvider instead. (WithAgentCard would add an optional
	// default card served when no "?tenant=" is given.)
	a2aServer, err := server.NewA2AServer(taskManager,
		server.WithTenantCard("chatAgent", agentCardFor("ChatAgent", "I am a chatbot", "chat", baseURL)),
		server.WithTenantCard("workerAgent", agentCardFor("WorkerAgent", "I am a worker", "worker", baseURL)),
	)
	if err != nil {
		log.Fatalf("Failed to create A2A server: %v", err)
	}

	fmt.Println("Starting A2A server on", *host)
	fmt.Printf("  Per-tenant card: http://%s/.well-known/agent-card.json?tenant=chatAgent|workerAgent\n", *host)
	fmt.Printf("  JSON-RPC: http://%s/  (set the \"tenant\" field in the request to pick an agent)\n", *host)

	if err := a2aServer.Start(*host); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

// agentCardFor builds a simple single-skill card sharing the common endpoint.
func agentCardFor(name, desc, skill, url string) server.AgentCard {
	return server.AgentCard{
		Name:        name,
		Description: desc,
		URL:         url,
		Skills: []server.AgentSkill{{
			Name:        skill,
			Description: stringPtr(desc),
			InputModes:  []string{"text"},
			OutputModes: []string{"text"},
			Tags:        []string{skill},
			Examples:    []string{"Hello"},
		}},
		Capabilities:       server.AgentCapabilities{Streaming: boolPtr(false)},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
	}
}

// multiAgentProcessor dispatches on the v1.0 tenant carried in the request body
// (taskmanager.ProcessOptions.Tenant) — no ctx-key, no URL parsing.
type multiAgentProcessor struct{}

func (p *multiAgentProcessor) ProcessMessage(
	_ context.Context,
	_ protocol.Message,
	options taskmanager.ProcessOptions,
	_ taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
	switch options.Tenant {
	case "chatAgent":
		return reply("Hello from chat agent!"), nil
	case "workerAgent":
		return reply("Hello from worker agent!"), nil
	default:
		return nil, fmt.Errorf("no such tenant %q (use chatAgent or workerAgent)", options.Tenant)
	}
}

func reply(text string) *taskmanager.MessageProcessingResult {
	msg := &protocol.Message{
		Role:      protocol.MessageRoleAgent,
		MessageID: protocol.GenerateMessageID(),
		Parts:     []*protocol.Part{protocol.NewTextPart(text)},
	}
	return &taskmanager.MessageProcessingResult{Result: protocol.NewSendMessageResponseMessage(msg)}
}

func stringPtr(s string) *string { return &s }
func boolPtr(b bool) *bool       { return &b }
