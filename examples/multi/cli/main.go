// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package main implements the client for the multi-agent example.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/client"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

func main() {
	rootURL := flag.String("url", "http://localhost:8080/", "root agent URL")
	flag.Parse()

	a2aClient, err := client.NewA2AClient(*rootURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create client: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Connected to %s. Type a request, or exit.\n", *rootURL)
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if strings.EqualFold(input, "exit") {
			break
		}
		if input == "" {
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		response, err := a2aClient.SendMessage(ctx, protocol.SendMessageParams{
			Message: protocol.NewMessage(
				protocol.MessageRoleUser,
				[]*protocol.Part{protocol.NewTextPart(input)},
			),
		})
		cancel()
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}

		text, err := responseText(response)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}
		fmt.Printf("%s\n", text)
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "read input: %v\n", err)
	}
}

func responseText(response *protocol.SendMessageResponse) (string, error) {
	if message := response.GetMessage(); message != nil {
		return messageText(*message), nil
	}
	if task := response.GetTask(); task != nil && task.Status.Message != nil {
		return messageText(*task.Status.Message), nil
	}
	return "", fmt.Errorf("agent returned no text")
}

func messageText(message protocol.Message) string {
	var result strings.Builder
	for _, part := range message.Parts {
		result.WriteString(part.TextContent())
	}
	return result.String()
}
