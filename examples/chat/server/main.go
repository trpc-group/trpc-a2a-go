package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/tmc/langchaingo/llms/openai"
	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/server"
	"trpc.group/trpc-go/trpc-a2a-go/taskmanager"
)

type User struct {
	ID   string
	Name string
}

type ChatRoom struct {
	ID       string
	users    map[string]*User
	messages []protocol.Message
	mu       sync.Mutex
}

type ChatRoomManager struct {
	rooms map[string]*ChatRoom
	mu    sync.Mutex
}

func NewChatRoomManager() *ChatRoomManager {
	return &ChatRoomManager{
		rooms: make(map[string]*ChatRoom),
	}
}

func (m *ChatRoomManager) GetOrCreateRoom(roomID string) *ChatRoom {
	m.mu.Lock()
	defer m.mu.Unlock()
	room, ok := m.rooms[roomID]
	if !ok {
		room = &ChatRoom{
			ID:    roomID,
			users: make(map[string]*User),
		}
		m.rooms[roomID] = room
	}
	return room
}

func (room *ChatRoom) AddUser(user *User) bool {
	room.mu.Lock()
	defer room.mu.Unlock()
	if _, exists := room.users[user.ID]; exists {
		return false
	}
	room.users[user.ID] = user
	return true
}

func (room *ChatRoom) RemoveUser(userID string) {
	room.mu.Lock()
	defer room.mu.Unlock()
	delete(room.users, userID)
}

func (room *ChatRoom) Broadcast(msg protocol.Message) {
	room.mu.Lock()
	room.messages = append(room.messages, msg)
	room.mu.Unlock()
}

func (room *ChatRoom) History() []protocol.Message {
	room.mu.Lock()
	defer room.mu.Unlock()
	history := make([]protocol.Message, len(room.messages))
	copy(history, room.messages)
	return history
}

// ----------------- AI -----------------
func callAI(ctx context.Context, input string) (string, error) {
	llm, err := openai.New()
	if err != nil {
		return "", err
	}
	resp, err := llm.Call(ctx, input)
	if err != nil {
		return "", err
	}
	return resp, nil
}

// ----------------- Processor -----------------
type chatMessageProcessor struct {
	roomMgr *ChatRoomManager
}

func (p *chatMessageProcessor) ProcessMessage(
	ctx context.Context,
	message protocol.Message,
	options taskmanager.ProcessOptions,
	handler taskmanager.TaskHandler,
) (*taskmanager.MessageProcessingResult, error) {
	roomID, userID, userName := parseContext(message)
	if roomID == "" || userID == "" {
		errMsg := "room_id and user_id required"
		log.Errorf(errMsg)
		errorMessage := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]protocol.Part{protocol.NewTextPart(errMsg)},
		)
		return &taskmanager.MessageProcessingResult{Result: &errorMessage}, nil
	}
	room := p.roomMgr.GetOrCreateRoom(roomID)
	user := &User{ID: userID, Name: userName}
	room.AddUser(user)

	text := extractText(message)
	if text == "" {
		errMsg := "input message must contain text."
		log.Errorf(errMsg)
		errorMessage := protocol.NewMessage(
			protocol.MessageRoleAgent,
			[]protocol.Part{protocol.NewTextPart(errMsg)},
		)
		return &taskmanager.MessageProcessingResult{Result: &errorMessage}, nil
	}
	fmt.Printf("[%s : %s] message: %s\n", roomID, userID, text)

	// 广播用户消息
	room.Broadcast(message)

	// 检查@ai
	if strings.HasPrefix(text, "@ai") {
		go func() {
			aiInput := strings.TrimSpace(strings.TrimPrefix(text, "@ai"))
			aiReply, err := callAI(ctx, aiInput)
			if err != nil {
				aiReply = "AI error: " + err.Error()
			}
			aiMsg := protocol.NewMessage(
				protocol.MessageRoleAgent,
				[]protocol.Part{protocol.NewTextPart("[AI] " + aiReply)},
			)
			aiMsg.ContextID = message.ContextID
			room.Broadcast(aiMsg)
		}()
	}

	// 流式推送
	if options.Streaming {
		taskID, err := handler.BuildTask(nil, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to build task: %w", err)
		}
		subscriber, err := handler.SubscribeTask(&taskID)
		if err != nil {
			return nil, fmt.Errorf("failed to subscribe to task: %w", err)
		}
		// 首次推送历史
		go func() {
			defer subscriber.Close()
			for _, msg := range room.History() {
				_ = subscriber.Send(protocol.StreamingMessageEvent{Result: &msg})
			}
			// 可选：阻塞直到 context 被取消
			<-ctx.Done()
		}()
		return &taskmanager.MessageProcessingResult{
			StreamingEvents: subscriber,
		}, nil
	}

	// 非流式，返回历史
	history := room.History()
	resp := protocol.NewMessage(
		protocol.MessageRoleAgent,
		[]protocol.Part{protocol.NewTextPart(formatHistory(history))},
	)
	return &taskmanager.MessageProcessingResult{
		Result: &resp,
	}, nil
}

func parseContext(msg protocol.Message) (roomID, userID, userName string) {
	if msg.ContextID != nil {
		roomID = *msg.ContextID
	}
	if msg.Metadata != nil {
		if v, ok := msg.Metadata["user_id"].(string); ok {
			userID = v
		}
		if v, ok := msg.Metadata["user_name"].(string); ok {
			userName = v
		}
	}
	return
}

func extractText(message protocol.Message) string {
	for _, part := range message.Parts {
		if textPart, ok := part.(*protocol.TextPart); ok {
			return textPart.Text
		}
	}
	return ""
}

func formatHistory(messages []protocol.Message) string {
	var sb strings.Builder
	sb.WriteString("Chat History:\n")
	for i, msg := range messages {
		var user string
		if msg.Metadata != nil {
			if v, ok := msg.Metadata["user_name"]; ok {
				if name, ok := v.(string); ok && name != "" {
					user = name
				}
			}
		}
		if user == "" {
			user = string(msg.Role)
		}
		sb.WriteString(fmt.Sprintf("[%d][%s]: %s\n", i+1, user, extractText(msg)))
	}
	return sb.String()
}

func stringPtr(s string) *string { return &s }
func boolPtr(b bool) *bool       { return &b }

func main() {
	host := flag.String("host", "localhost", "Host to listen on")
	port := flag.Int("port", 8080, "Port to listen on")
	flag.Parse()

	agentCard := server.AgentCard{
		Name:        "AI Chat Room",
		Description: "A simple chat room with AI bot",
		URL:         fmt.Sprintf("http://%s:%d/", *host, *port),
		Version:     "1.0.0",
		Provider: &server.AgentProvider{
			Organization: "tRPC-A2A-Go Examples",
		},
		Capabilities: server.AgentCapabilities{
			Streaming:              boolPtr(true),
			PushNotifications:      boolPtr(false),
			StateTransitionHistory: boolPtr(true),
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills: []server.AgentSkill{
			{
				ID:          "chat",
				Name:        "Chat Room",
				Description: stringPtr("Group chat with AI bot"),
				Tags:        []string{"chat", "ai"},
				Examples:    []string{"@ai 你好", "大家好"},
				InputModes:  []string{"text"},
				OutputModes: []string{"text"},
			},
		},
	}

	processor := &chatMessageProcessor{
		roomMgr: NewChatRoomManager(),
	}

	taskManager, err := taskmanager.NewMemoryTaskManager(processor)
	if err != nil {
		log.Fatalf("Failed to create task manager: %v", err)
	}

	srv, err := server.NewA2AServer(agentCard, taskManager)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		serverAddr := fmt.Sprintf("%s:%d", *host, *port)
		log.Infof("Starting server on %s...", serverAddr)
		if err := srv.Start(serverAddr); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	sig := <-sigChan
	log.Infof("Received signal %v, shutting down...", sig)
}
