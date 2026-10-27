// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent. All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Command server runs an A2A agent backed by coalesced Redis Task snapshots.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	redistask "trpc.group/trpc-go/trpc-a2a-go/taskmanager/redis/v2"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/server"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

type streamingArtifactProcessor struct {
	chunks     int
	chunkBytes int
	delay      time.Duration
}

func (p *streamingArtifactProcessor) ProcessMessage(
	ctx context.Context,
	ec *taskmanager.ExecContext,
) (<-chan protocol.StreamEvent, error) {
	handle := taskmanager.NewTaskHandle(ctx, ec)
	go func() {
		defer handle.Close()
		if err := handle.UpdateTaskState(protocol.TaskStateWorking, nil); err != nil {
			log.Printf("set working state: %v", err)
			return
		}
		for i := 0; i < p.chunks; i++ {
			select {
			case <-ctx.Done():
				return
			default:
			}
			artifact := protocol.Artifact{
				ArtifactID: "streamed-result",
				Name:       stringPtr("Coalesced Redis artifact"),
				Parts: []*protocol.Part{
					protocol.NewTextPart(strings.Repeat("x", p.chunkBytes)),
				},
			}
			lastChunk := i == p.chunks-1
			var err error
			if i == 0 {
				err = handle.AddArtifact(artifact, lastChunk)
			} else {
				err = handle.AppendArtifact(artifact, lastChunk)
			}
			if err != nil {
				log.Printf("append artifact chunk %d: %v", i+1, err)
				return
			}
			if !wait(ctx, p.delay) {
				return
			}
		}
		if err := handle.UpdateTaskState(protocol.TaskStateCompleted, nil); err != nil {
			log.Printf("set completed state: %v", err)
		}
	}()
	return handle.Events(), nil
}

func wait(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func main() {
	listenAddr := flag.String("addr", ":8081", "A2A server listen address")
	publicURL := flag.String("public-url", "http://localhost:8081/", "URL advertised in the Agent Card")
	redisAddr := flag.String("redis-addr", "localhost:6379", "Redis address")
	redisPassword := flag.String("redis-password", "", "Redis password")
	chunks := flag.Int("chunks", 64, "number of artifact chunks per request")
	chunkBytes := flag.Int("chunk-bytes", 2048, "text bytes per artifact chunk")
	chunkDelay := flag.Duration("chunk-delay", 10*time.Millisecond, "delay between chunks")
	flushInterval := flag.Duration("flush-interval", 200*time.Millisecond, "Task snapshot flush interval")
	maxBuffered := flag.Int("max-buffered-events", 32, "artifact events before a forced Task snapshot flush")
	streamMaxLen := flag.Int64("stream-max-len", 1000, "approximate maximum Redis Stream length")
	flag.Parse()

	if *chunks <= 0 || *chunkBytes <= 0 || *maxBuffered <= 0 || *streamMaxLen <= 0 {
		log.Fatal("chunks, chunk-bytes, max-buffered-events and stream-max-len must be positive")
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr:     *redisAddr,
		Password: *redisPassword,
	})
	defer redisClient.Close()
	if err := redisClient.Ping(context.Background()).Err(); err != nil {
		log.Fatalf("connect to Redis %s: %v", *redisAddr, err)
	}

	persistence := redistask.CoalescedTaskPersistenceDefaults()
	persistence.SnapshotFlushInterval = *flushInterval
	persistence.MaxBufferedTaskEvents = *maxBuffered

	manager, err := redistask.NewTaskManager(
		&streamingArtifactProcessor{
			chunks:     *chunks,
			chunkBytes: *chunkBytes,
			delay:      *chunkDelay,
		},
		redisClient,
		redistask.WithTaskPersistence(persistence),
		redistask.WithTaskEventStreamMaxLen(*streamMaxLen),
	)
	if err != nil {
		log.Fatalf("create Redis TaskManager: %v", err)
	}
	defer manager.Close()

	agentCard := server.AgentCard{
		Name:        "Redis Coalesced Persistence Example",
		Description: "Streams a growing artifact while coalescing complete Redis Task snapshots",
		URL:         *publicURL,
		Version:     "1.0.0",
		Provider: &server.AgentProvider{
			Organization: "tRPC-A2A-Go Examples",
		},
		Capabilities: server.AgentCapabilities{
			Streaming: boolPtr(true),
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills: []server.AgentSkill{{
			ID:          "stream_large_artifact",
			Name:        "Stream large artifact",
			Description: stringPtr(fmt.Sprintf("Streams %d chunks of %d bytes", *chunks, *chunkBytes)),
			Tags:        []string{"redis", "streaming", "persistence"},
			Examples:    []string{"Generate a streamed artifact"},
			InputModes:  []string{"text"},
			OutputModes: []string{"text"},
		}},
	}

	agentServer, err := server.NewA2AServer(manager, server.WithAgentCard(agentCard))
	if err != nil {
		log.Fatalf("create A2A server: %v", err)
	}
	log.Printf(
		"listening on %s; chunks=%d chunkBytes=%d flushInterval=%s maxBuffered=%d",
		*listenAddr, *chunks, *chunkBytes, *flushInterval, *maxBuffered,
	)
	if err := agentServer.Start(*listenAddr); err != nil {
		log.Fatalf("start A2A server: %v", err)
	}
}

func stringPtr(value string) *string {
	return &value
}

func boolPtr(value bool) *bool {
	return &value
}
