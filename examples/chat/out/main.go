package main

import (
	pb "chat/protobuf"

	_ "trpc.group/trpc-go/trpc-filter/debuglog"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

func main() {
	s := trpc.NewServer()
	pb.RegisterChatroomServiceService(s.Service("chat.ChatroomService"), NewChatroomService())
	if err := s.Serve(); err != nil {
		log.Fatal(err)
	}
}
