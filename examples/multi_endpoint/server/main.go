// Package main is the entry point for the A2A server.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

const ctxAgentNameKey = "agentName"

var host = flag.String("host", "localhost:8080", "host")

func main() {
	flag.Parse()
	// Create chi router
	router := chi.NewMux()

	processor := &multiAgentProecssor{}

	taskManager, err := taskmanager.NewMemoryTaskManager(processor)
	if err != nil {
		log.Fatalf("Failed to create task manager: %v", err)
	}

	agentCard := server.AgentCard{
		Name:        "MultiAgent",
		Description: "This agent card will not be used while AgentCardHandler is provided",
	}

	a2aServer, err := server.NewA2AServer(
		agentCard,
		taskManager,
		server.WithMiddleWare(&middleWare{}),
		server.WithHTTPRouter(router),
		server.WithAgentCardHandler(&multiAgentCardHandler{}),
		server.WithBasePath("/api/v1/agent/{agentName}/"),
	)
	if err != nil {
		log.Fatalf("Failed to create A2A server: %v", err)
	}

	fmt.Println("Starting A2A server listening on", *host)
	fmt.Println("Chat agent card url :", fmt.Sprintf("http://%s/api/v1/agent/chatAgent/.well-known/agent.json:", *host))
	fmt.Println("Chat agent interfaces:", fmt.Sprintf("http://%s/api/v1/agent/chatAgent/", *host))
	fmt.Println("Worker agent card url:", fmt.Sprintf("http://%s/api/v1/agent/workerAgent/.well-known/agent.json", *host))
	fmt.Println("Worker agent interfaces:", fmt.Sprintf("http://%s/api/v1/agent/workerAgent/", *host))

	if err := a2aServer.Start(*host); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}

}

type middleWare struct{}

func (m *middleWare) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		ctx = context.WithValue(ctx, ctxAgentNameKey, chi.URLParam(r, "agentName"))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type multiAgentCardHandler struct{}

func (h *multiAgentCardHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Get agent name from url param, because middleware not task effect on agent card handler
	agentName := chi.URLParam(r, "agentName")
	var agentCard server.AgentCard
	switch agentName {
	case "chatAgent":
		agentCard = server.AgentCard{
			Name:        "ChatAgent",
			Description: "I am a chatbot",
			URL:         fmt.Sprintf("http://%s/api/v1/agent/chatAgent/", *host),
			Skills: []server.AgentSkill{
				{
					Name:        "chat",
					Description: stringPtr("I am a chatbot"),
					InputModes:  []string{"text"},
					OutputModes: []string{"text"},
					Tags:        []string{"chat"},
					Examples:    []string{"Hello"},
				},
			},
			Capabilities: server.AgentCapabilities{
				Streaming: boolPtr(false),
			},
			DefaultInputModes:  []string{"text"},
			DefaultOutputModes: []string{"text"},
		}
	case "workerAgent":
		agentCard = server.AgentCard{
			Name:        "WorkerAgent",
			Description: "I am a worker",
			URL:         fmt.Sprintf("http://%s/api/v1/agent/workerAgent/", *host),
			Skills: []server.AgentSkill{
				{
					Name:        "worker",
					Description: stringPtr("I am a worker"),
					InputModes:  []string{"text"},
					OutputModes: []string{"text"},
					Tags:        []string{"worker"},
					Examples:    []string{"Hello"},
				},
			},
			Capabilities: server.AgentCapabilities{
				Streaming: boolPtr(false),
			},
			DefaultInputModes:  []string{"text"},
			DefaultOutputModes: []string{"text"},
		}
	default:
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(agentCard); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
}

type multiAgentProecssor struct {
}

func (u *multiAgentProecssor) ProcessMessage(
	ctx context.Context,
	message protocol.Message,
	options taskmanager.ProcessOptions,
	taskHandler taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
	agentName := ctx.Value(ctxAgentNameKey)
	if agentName == nil {
		return nil, errors.New("agent name not found")
	}

	switch agentName.(string) {
	case "chatAgent":
		return u.chatAgentProcessMessage(ctx, message, options, taskHandler)
	case "workerAgent":
		return u.workerAgentProcessMessage(ctx, message, options, taskHandler)
	default:
		return nil, errors.New("unknown agent name")
	}
}

func (u *multiAgentProecssor) chatAgentProcessMessage(
	ctx context.Context,
	message protocol.Message,
	options taskmanager.ProcessOptions,
	taskHandler taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
	msg := &protocol.Message{
		Role:      protocol.MessageRoleAgent,
		Kind:      protocol.KindMessage,
		MessageID: protocol.GenerateMessageID(),
		Parts: []protocol.Part{
			&protocol.TextPart{
				Kind: protocol.KindText,
				Text: "Hello from chat agent!",
			},
		},
	}

	return &taskmanager.MessageProcessingResult{
		Result: msg,
	}, nil
}

func (u *multiAgentProecssor) workerAgentProcessMessage(
	ctx context.Context,
	message protocol.Message,
	options taskmanager.ProcessOptions,
	taskHandler taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
	msg := &protocol.Message{
		Role:      protocol.MessageRoleAgent,
		Kind:      protocol.KindMessage,
		MessageID: protocol.GenerateMessageID(),
		Parts: []protocol.Part{
			&protocol.TextPart{
				Kind: protocol.KindText,
				Text: "Hello from worker agent!",
			},
		},
	}

	return &taskmanager.MessageProcessingResult{
		Result: msg,
	}, nil
}

func stringPtr(s string) *string {
	return &s
}

func boolPtr(b bool) *bool {
	return &b
}
