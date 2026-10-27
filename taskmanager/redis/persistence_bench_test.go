// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package redis

import (
	"context"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

func BenchmarkArtifactCommitsPerEvent(b *testing.B) {
	benchmarkArtifactCommits(b, DefaultTaskPersistenceConfig())
}

func BenchmarkArtifactCommitsCoalesced(b *testing.B) {
	cfg := CoalescedTaskPersistenceDefaults()
	cfg.SnapshotFlushInterval = 0
	cfg.MaxBufferedTaskEvents = 1000
	benchmarkArtifactCommits(b, cfg)
}

func benchmarkArtifactCommits(b *testing.B, persistence TaskPersistenceConfig) {
	mr := miniredis.RunT(b)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	b.Cleanup(func() { _ = client.Close() })
	manager, err := NewTaskManager(scriptedExecutor(), client, WithTaskPersistence(persistence))
	if err != nil {
		b.Fatalf("NewTaskManager: %v", err)
	}
	b.Cleanup(func() { _ = manager.Close() })

	ctx := context.Background()
	taskID := "bench-task"
	tenant, owner := "", ""
	runID := "run-bench"
	if _, acquired, _, err := manager.executionLease.AcquireExecution(ctx, tenant, owner, taskID, runID); err != nil || !acquired {
		b.Fatalf("AcquireExecution: acquired=%v err=%v", acquired, err)
	}

	task := protocol.NewTask(taskID, "ctx-bench")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		text := strings.Repeat("a", 256)
		appendFlag := i > 0
		part := protocol.NewTextPart(text)
		next := copyTask(task)
		next.Artifacts, _ = protocol.AppendArtifact(next.Artifacts, protocol.Artifact{
			ArtifactID: "art-bench",
			Parts:      []*protocol.Part{part},
		}, appendFlag)
		ev := &protocol.TaskArtifactUpdateEvent{
			TaskID: taskID, ContextID: "ctx-bench", Artifact: protocol.Artifact{
				ArtifactID: "art-bench",
				Parts:      []*protocol.Part{part},
			},
			Append: &appendFlag,
		}
		commitOpts := manager.buildTaskCommitOptions(i == 0)
		if err := manager.commitExecutionTaskEventWithOptions(
			ctx, tenant, owner, runID, next, protocol.NewStreamResponseArtifactUpdate(ev),
			i == 0, false, false, commitOpts,
		); err != nil {
			b.Fatalf("commit: %v", err)
		}
		task = next
	}
}
