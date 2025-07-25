package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/log"

	pb "chat/protobuf"
	_ "trpc.group/trpc-go/trpc-filter/debuglog"
)

func main() {
	roomID := flag.String("room", "default", "聊天室ID")
	sender := flag.String("name", "anonymous", "用户名")
	target := flag.String("target", "ip://127.0.0.1:8000", "服务地址")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cli := pb.NewChatroomServiceClientProxy(client.WithTarget(*target))
	stream, err := cli.Chat(ctx)
	if err != nil {
		log.Fatalf("连接聊天室失败: %v", err)
	}

	// 发送初始化消息
	initMsg := &pb.ChatroomMessage{
		RoomId:  *roomID,
		Sender:  *sender,
		Content: "加入聊天室",
	}
	if err := stream.Send(initMsg); err != nil {
		log.Fatalf("发送初始消息失败: %v", err)
	}

	// 接收消息协程
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					log.Infof("服务器关闭了连接")
				} else {
					log.Infof("接收消息失败: %v", err)
				}
				cancel()
				return
			}

			// 特别提示被拒绝加入房间
			if msg.Sender == "系统" && strings.Contains(msg.Content, "已在其他房间连接") {
				fmt.Printf("[系统] %s\n", msg.Content)
				cancel()
				os.Exit(0)
			}

			fmt.Printf("[%s] %s\n", msg.Sender, msg.Content)
		}
	}()

	// 捕获退出信号
	go func() {
		sigch := make(chan os.Signal, 1)
		signal.Notify(sigch, syscall.SIGINT, syscall.SIGTERM)
		<-sigch
		fmt.Println("\n已收到退出信号，正在退出...")
		_ = stream.CloseSend()
		cancel()
		os.Exit(0)
	}()

	// 控制台输入循环
	fmt.Println("已加入聊天室，输入消息发送（Ctrl+C退出）：")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		msg := &pb.ChatroomMessage{
			RoomId:  *roomID,
			Sender:  *sender,
			Content: text,
		}
		if err := stream.Send(msg); err != nil {
			log.Infof("发送消息失败: %v", err)
			break
		}
	}

	if err := scanner.Err(); err != nil {
		log.Infof("读取输入失败: %v", err)
	}
}
