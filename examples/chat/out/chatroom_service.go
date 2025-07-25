package main

import (
	"io"
	"log"
	"strings"
	"sync"

	pb "chat/protobuf"
)

type chatroomServiceImpl struct {
	pb.UnimplementedChatroomService
	clients   map[string]pb.ChatroomService_ChatServer // key: roomId:sender
	senderMap map[string]string                        // sender -> roomId
	mu        sync.RWMutex
}

func NewChatroomService() *chatroomServiceImpl {
	return &chatroomServiceImpl{
		clients:   make(map[string]pb.ChatroomService_ChatServer),
		senderMap: make(map[string]string),
	}
}

func (s *chatroomServiceImpl) Chat(stream pb.ChatroomService_ChatServer) error {
	firstMsg, err := stream.Recv()
	if err != nil {
		log.Printf("初始化连接失败: %v", err)
		return err
	}
	clientID := firstMsg.RoomId + ":" + firstMsg.Sender

	s.mu.Lock()
	// ✅ 用户唯一性检查
	if oldRoom, exists := s.senderMap[firstMsg.Sender]; exists {
		s.mu.Unlock()
		log.Printf("用户 %s 已在房间 %s 中活跃，拒绝重复加入", firstMsg.Sender, oldRoom)
		_ = stream.Send(&pb.ChatroomMessage{
			RoomId:  firstMsg.RoomId,
			Sender:  "系统",
			Content: "你已在其他房间连接，请退出后再加入新房间",
		})
		return nil
	}

	// 注册
	s.clients[clientID] = stream
	s.senderMap[firstMsg.Sender] = firstMsg.RoomId
	s.mu.Unlock()

	log.Printf("%s 加入了房间 %s", firstMsg.Sender, firstMsg.RoomId)

	s.broadcast(firstMsg.RoomId, &pb.ChatroomMessage{
		RoomId:  firstMsg.RoomId,
		Sender:  "系统",
		Content: firstMsg.Sender + " 加入了聊天室",
	}, clientID)

	// 退出处理
	defer func() {
		s.mu.Lock()
		delete(s.clients, clientID)
		delete(s.senderMap, firstMsg.Sender)
		s.mu.Unlock()

		log.Printf("%s 离开了房间 %s", firstMsg.Sender, firstMsg.RoomId)
		s.broadcast(firstMsg.RoomId, &pb.ChatroomMessage{
			RoomId:  firstMsg.RoomId,
			Sender:  "系统",
			Content: firstMsg.Sender + " 离开了聊天室",
		}, clientID)
	}()

	for {
		msg, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				log.Printf("客户端 %s 主动关闭连接", clientID)
			} else {
				log.Printf("接收消息失败 (%s): %v", clientID, err)
			}
			return err
		}
		log.Printf("[%s] %s", msg.Sender, msg.Content)
		s.broadcast(firstMsg.RoomId, msg, clientID)
	}
}

func (s *chatroomServiceImpl) broadcast(roomID string, msg *pb.ChatroomMessage, excludeID string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for id, client := range s.clients {
		if id == excludeID {
			continue
		}
		if strings.HasPrefix(id, roomID+":") {
			if err := client.Send(msg); err != nil {
				log.Printf("广播失败 [%s]: %v", id, err)
			}
		}
	}
}
