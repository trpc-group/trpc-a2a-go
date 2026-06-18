package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/google/uuid"
	"trpc.group/trpc-go/trpc-a2a-go/client"
	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

func main() {
	agentURL := flag.String("agent", "http://localhost:8080/", "Target A2A agent URL")
	roomID := flag.String("room", "default", "Chat room ID")
	userName := flag.String("user", "user", "Your name")
	streaming := flag.Bool("streaming", true, "Use streaming mode")
	flag.Parse()

	userID := uuid.New().String()

	a2aClient, err := client.NewA2AClient(*agentURL, client.WithTimeout(60*time.Second))
	if err != nil {
		log.Fatalf("Failed to create A2A client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 捕获 Ctrl+C 退出
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	go func() {
		<-sigChan
		fmt.Println("\nExiting...")
		cancel()
		os.Exit(0)
	}()

	fmt.Printf("Welcome %s! Room: %s\n", *userName, *roomID)

	// 启动流式接收
	go receiveStreaming(ctx, a2aClient, *roomID, userID, *userName)

	// 主循环发送消息
	for {
		var input string
		fmt.Print("> ")
		if _, err := fmt.Scanln(&input); err != nil {
			continue
		}
		sendMessage(ctx, a2aClient, *roomID, userID, *userName, input, *streaming)
	}
}

func sendMessage(ctx context.Context, a2aClient *client.A2AClient, roomID, userID, userName, msg string, streaming bool) {
	userMsg := protocol.NewMessage(
		protocol.MessageRoleUser,
		[]protocol.Part{protocol.NewTextPart(msg)},
	)
	userMsg.ContextID = &roomID // 作为room_id
	userMsg.Metadata = map[string]interface{}{
		"user_id":   userID,
		"user_name": userName,
	}

	params := protocol.SendMessageParams{
		Message: userMsg,
		Configuration: &protocol.SendMessageConfiguration{
			Blocking: boolPtr(false),
		},
	}
	if !streaming {
		result, err := a2aClient.SendMessage(ctx, params)
		if err != nil {
			log.Errorf("Send failed: %v", err)
			return
		}
		if m, ok := result.Result.(*protocol.Message); ok {
			printMessage(*m)
		}
		return
	}
	// 流式模式只需发送，接收由 receiveStreaming 负责
	_, err := a2aClient.SendMessage(ctx, params)
	if err != nil {
		log.Errorf("Send failed: %v", err)
	}
}

func receiveStreaming(ctx context.Context, a2aClient *client.A2AClient, roomID, userID, userName string) {
	// 启动流式订阅
	userMsg := protocol.NewMessage(
		protocol.MessageRoleUser,
		[]protocol.Part{protocol.NewTextPart(fmt.Sprintf("%s joined the chat.", userName))},
	)
	userMsg.ContextID = &roomID
	userMsg.Metadata = map[string]interface{}{
		"user_id":   userID,
		"user_name": userName,
	}

	params := protocol.SendMessageParams{
		Message: userMsg,
		Configuration: &protocol.SendMessageConfiguration{
			Blocking: boolPtr(false),
		},
	}
	eventChan, err := a2aClient.StreamMessage(ctx, params)
	if err != nil {
		log.Fatalf("Failed to start streaming: %v", err)
	}
	for {
		select {
		case event, ok := <-eventChan:
			if !ok {
				log.Infof("Stream closed by server.")
				return
			}
			if m, ok := event.Result.(*protocol.Message); ok {
				printMessage(*m)
			}
		case <-ctx.Done():
			return
		}
	}
}

func printMessage(message protocol.Message) {
	for _, part := range message.Parts {
		if textPart, ok := part.(*protocol.TextPart); ok {
			fmt.Printf("\n%s\n> ", strings.TrimSpace(textPart.Text))
		}
	}
}

func boolPtr(b bool) *bool { return &b }
