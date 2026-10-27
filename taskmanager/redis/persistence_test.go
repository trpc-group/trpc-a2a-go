// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package redis

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

func TestCoalescedTaskSnapshotKeepsReplayCursor(t *testing.T) {
	cfg := CoalescedTaskPersistenceDefaults()
	cfg.SnapshotFlushInterval = time.Hour
	cfg.MaxBufferedTaskEvents = 100
	release := make(chan struct{})

	manager, _ := setupTest(t, streamingArtifactProcessor(5, release), WithTaskPersistence(cfg))
	defer manager.Close()

	ctx := context.Background()
	stream, err := manager.OnSendMessageStream(ctx, sendParams("start", "ctx-stream-opt"))
	if err != nil {
		t.Fatalf("OnSendMessageStream: %v", err)
	}
	initial := recvEvent(t, stream)
	task := initial.GetTask()
	if task == nil {
		t.Fatalf("initial frame = %+v, want Task", initial.Result)
	}

	for i := 0; i < 5; i++ {
		_ = recvEvent(t, stream)
	}

	storedBytes, err := manager.client.Get(ctx, taskKey("", "", task.ID)).Bytes()
	if err != nil {
		t.Fatalf("GET task during stream: %v", err)
	}
	var stored protocol.Task
	if err := json.Unmarshal(storedBytes, &stored); err != nil {
		t.Fatalf("decode stored task: %v", err)
	}
	if len(stored.Artifacts) != 0 {
		t.Fatalf("stored snapshot advanced before configured flush: %+v", stored.Artifacts)
	}

	snapshot, cursor, err := manager.eventTransport.LoadTaskAndCursor(ctx, "", "", task.ID)
	if err != nil {
		t.Fatalf("LoadTaskAndCursor: %v", err)
	}
	if len(snapshot.Artifacts) != 0 {
		t.Fatalf("loaded snapshot advanced before configured flush: %+v", snapshot.Artifacts)
	}
	events, _, err := manager.eventTransport.ReadAfter(ctx, "", "", task.ID, cursor)
	if err != nil {
		t.Fatalf("ReadAfter snapshot cursor: %v", err)
	}
	artifactEvents := 0
	for _, event := range events {
		if event.GetArtifactUpdate() != nil {
			artifactEvents++
		}
	}
	if artifactEvents != 5 {
		t.Fatalf("artifact events after snapshot cursor = %d, want 5", artifactEvents)
	}
	tail, err := manager.client.XRevRangeN(ctx, streamKey("", "", task.ID), "+", "-", 1).Result()
	if err != nil || len(tail) != 1 {
		t.Fatalf("load stream tail: entries=%d err=%v", len(tail), err)
	}
	if cursor == tail[0].ID {
		t.Fatalf("snapshot cursor %s unexpectedly advanced to unflushed stream tail", cursor)
	}

	subCtx, subCancel := context.WithCancel(ctx)
	resub, err := manager.OnResubscribe(subCtx, protocol.TaskIDParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnResubscribe: %v", err)
	}
	firstResubFrame := recvEvent(t, resub)
	resubSnapshot := firstResubFrame.GetTask()
	if resubSnapshot == nil || len(resubSnapshot.Artifacts) != 0 {
		t.Fatalf("resubscribe first frame = %+v, want stored snapshot without artifacts", resubSnapshot)
	}
	resubArtifacts := 0
	for resubArtifacts < 5 {
		frame := recvEvent(t, resub)
		if frame.GetArtifactUpdate() != nil {
			resubArtifacts++
		}
	}
	subCancel()

	live, err := manager.OnGetTask(ctx, protocol.TaskQueryParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnGetTask while live: %v", err)
	}
	if len(live.Artifacts) == 0 || len(live.Artifacts[0].Parts) == 0 {
		t.Fatalf("live task missing buffered artifact parts: %+v", live.Artifacts)
	}

	close(release)
	if !drainForState(t, stream, protocol.TaskStateCompleted) {
		t.Fatal("stream did not reach COMPLETED")
	}
	deadline := time.Now().Add(2 * time.Second)
	for liveExecutionCount(manager) != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	final, err := manager.OnGetTask(ctx, protocol.TaskQueryParams{ID: task.ID})
	if err != nil {
		t.Fatalf("OnGetTask: %v", err)
	}
	if len(final.Artifacts) == 0 || len(final.Artifacts[0].Parts) == 0 {
		t.Fatalf("final task missing artifact parts: %+v", final.Artifacts)
	}
	finalCursor, err := manager.client.Get(ctx, snapshotCursorKey("", "", task.ID)).Result()
	if err != nil {
		t.Fatalf("GET final snapshot cursor: %v", err)
	}
	finalTail, err := manager.client.XRevRangeN(ctx, streamKey("", "", task.ID), "+", "-", 1).Result()
	if err != nil || len(finalTail) != 1 {
		t.Fatalf("load final stream tail: entries=%d err=%v", len(finalTail), err)
	}
	if finalCursor != finalTail[0].ID {
		t.Fatalf("final snapshot cursor = %s, stream tail = %s", finalCursor, finalTail[0].ID)
	}
}

func streamingArtifactProcessor(chunks int, release <-chan struct{}) taskmanager.MessageProcessor {
	return executorFunc(func(ctx context.Context, ec *taskmanager.ExecContext) (<-chan protocol.StreamEvent, error) {
		out := make(chan protocol.StreamEvent, chunks+2)
		go func() {
			defer close(out)
			out <- statusEvent(protocol.TaskStateWorking, nil)
			for i := 0; i < chunks; i++ {
				text := strings.Repeat("x", 128)
				appendFlag := i > 0
				out <- &protocol.TaskArtifactUpdateEvent{
					TaskID:    ec.TaskID,
					ContextID: ec.ContextID,
					Artifact: protocol.Artifact{
						ArtifactID: "art-stream",
						Parts:      []*protocol.Part{protocol.NewTextPart(text)},
					},
					Append: &appendFlag,
				}
			}
			if release != nil {
				<-release
			}
			out <- statusEvent(protocol.TaskStateCompleted, nil)
		}()
		return out, nil
	})
}
