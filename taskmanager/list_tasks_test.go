// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package taskmanager

import (
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

func TestParseListTasksStatusTimestampAfter(t *testing.T) {
	if ts, err := ParseListTasksStatusTimestampAfter(""); err != nil || !ts.IsZero() {
		t.Errorf("empty: got (%v, %v), want (zero, nil)", ts, err)
	}
	if _, err := ParseListTasksStatusTimestampAfter("2024-01-02T03:04:05Z"); err != nil {
		t.Errorf("valid RFC3339: unexpected err %v", err)
	}
	if _, err := ParseListTasksStatusTimestampAfter("not-a-time"); err == nil {
		t.Error("invalid input: expected an error")
	}
}

func ltTask(id, ctx string, state protocol.TaskState, ts string) *protocol.Task {
	return &protocol.Task{ID: id, ContextID: ctx, Status: protocol.TaskStatus{State: state, Timestamp: ts}}
}

func TestTaskMatchesListFilter(t *testing.T) {
	bound := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	boundStr := bound.Format(time.RFC3339)

	tests := []struct {
		name   string
		task   *protocol.Task
		params protocol.ListTasksParams
		after  time.Time
		want   bool
	}{
		{"no filters", ltTask("t", "", protocol.TaskStateWorking, boundStr), protocol.ListTasksParams{}, time.Time{}, true},
		{"contextID match", ltTask("t", "c1", protocol.TaskStateWorking, boundStr), protocol.ListTasksParams{ContextID: "c1"}, time.Time{}, true},
		{"contextID mismatch", ltTask("t", "c1", protocol.TaskStateWorking, boundStr), protocol.ListTasksParams{ContextID: "c2"}, time.Time{}, false},
		{"status match", ltTask("t", "", protocol.TaskStateWorking, boundStr), protocol.ListTasksParams{Status: protocol.TaskStateWorking}, time.Time{}, true},
		{"status mismatch", ltTask("t", "", protocol.TaskStateWorking, boundStr), protocol.ListTasksParams{Status: protocol.TaskStateCompleted}, time.Time{}, false},
		// v1.0 statusTimestampAfter is INCLUSIVE: a status timestamp equal to the
		// bound must be returned (proto: "greater than or equal to this value").
		{"timestamp equal -> included", ltTask("t", "", protocol.TaskStateWorking, boundStr), protocol.ListTasksParams{}, bound, true},
		{"timestamp after -> included", ltTask("t", "", protocol.TaskStateWorking, bound.Add(time.Second).Format(time.RFC3339)), protocol.ListTasksParams{}, bound, true},
		{"timestamp before -> excluded", ltTask("t", "", protocol.TaskStateWorking, bound.Add(-time.Second).Format(time.RFC3339)), protocol.ListTasksParams{}, bound, false},
		{"unparsable timestamp with bound -> excluded", ltTask("t", "", protocol.TaskStateWorking, "garbage"), protocol.ListTasksParams{}, bound, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TaskMatchesListFilter(tt.task, tt.params, tt.after); got != tt.want {
				t.Errorf("TaskMatchesListFilter = %v, want %v", got, tt.want)
			}
		})
	}
}

func ltIntPtr(i int) *int   { return &i }
func ltBoolPtr(b bool) *bool { return &b }

func ltSampleTasks() []*protocol.Task {
	mk := func(id string) *protocol.Task {
		return &protocol.Task{
			ID:        id,
			History:   []protocol.Message{{MessageID: "m1"}, {MessageID: "m2"}, {MessageID: "m3"}},
			Artifacts: []protocol.Artifact{{ArtifactID: "a1"}},
		}
	}
	// Out-of-order IDs to exercise the stable sort.
	return []*protocol.Task{mk("t3"), mk("t1"), mk("t5"), mk("t2"), mk("t4")}
}

func TestPaginateTasks(t *testing.T) {
	t.Run("default page: sorted by ID, artifacts stripped, no next page", func(t *testing.T) {
		res, err := PaginateTasks(ltSampleTasks(), protocol.ListTasksParams{})
		if err != nil {
			t.Fatal(err)
		}
		if res.TotalSize != 5 || res.PageSize != ListTasksDefaultPageSize {
			t.Errorf("total=%d pageSize=%d", res.TotalSize, res.PageSize)
		}
		if len(res.Tasks) != 5 || res.Tasks[0].ID != "t1" || res.Tasks[4].ID != "t5" {
			t.Errorf("not sorted: %v", res.Tasks)
		}
		if res.Tasks[0].Artifacts != nil {
			t.Error("artifacts should be stripped when IncludeArtifacts is unset")
		}
		if res.NextPageToken != "" {
			t.Errorf("unexpected nextPageToken %q", res.NextPageToken)
		}
	})

	t.Run("pageSize + pageToken walk pages", func(t *testing.T) {
		p1, err := PaginateTasks(ltSampleTasks(), protocol.ListTasksParams{PageSize: ltIntPtr(2)})
		if err != nil {
			t.Fatal(err)
		}
		if len(p1.Tasks) != 2 || p1.Tasks[0].ID != "t1" || p1.Tasks[1].ID != "t2" || p1.NextPageToken != "2" {
			t.Errorf("page1 wrong: ids=%s,%s next=%q", p1.Tasks[0].ID, p1.Tasks[1].ID, p1.NextPageToken)
		}
		p2, err := PaginateTasks(ltSampleTasks(), protocol.ListTasksParams{PageSize: ltIntPtr(2), PageToken: p1.NextPageToken})
		if err != nil {
			t.Fatal(err)
		}
		if p2.Tasks[0].ID != "t3" || p2.NextPageToken != "4" {
			t.Errorf("page2 wrong: id=%s next=%q", p2.Tasks[0].ID, p2.NextPageToken)
		}
	})

	t.Run("invalid pageToken errors", func(t *testing.T) {
		if _, err := PaginateTasks(ltSampleTasks(), protocol.ListTasksParams{PageToken: "abc"}); err == nil {
			t.Error("non-numeric pageToken should error")
		}
		if _, err := PaginateTasks(ltSampleTasks(), protocol.ListTasksParams{PageToken: "-1"}); err == nil {
			t.Error("negative pageToken should error")
		}
	})

	t.Run("offset past end -> empty page", func(t *testing.T) {
		res, err := PaginateTasks(ltSampleTasks(), protocol.ListTasksParams{PageToken: "100"})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Tasks) != 0 || res.NextPageToken != "" {
			t.Errorf("expected empty final page, got %d tasks next=%q", len(res.Tasks), res.NextPageToken)
		}
	})

	t.Run("pageSize capped at max", func(t *testing.T) {
		res, err := PaginateTasks(ltSampleTasks(), protocol.ListTasksParams{PageSize: ltIntPtr(99999)})
		if err != nil {
			t.Fatal(err)
		}
		if res.PageSize != ListTasksMaxPageSize {
			t.Errorf("pageSize=%d want %d", res.PageSize, ListTasksMaxPageSize)
		}
	})

	t.Run("historyLength trims; includeArtifacts keeps", func(t *testing.T) {
		res, err := PaginateTasks(ltSampleTasks(), protocol.ListTasksParams{HistoryLength: ltIntPtr(1), IncludeArtifacts: ltBoolPtr(true)})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Tasks[0].History) != 1 || res.Tasks[0].History[0].MessageID != "m3" {
			t.Errorf("history not trimmed to last 1: %v", res.Tasks[0].History)
		}
		if len(res.Tasks[0].Artifacts) != 1 {
			t.Error("artifacts should be kept when IncludeArtifacts is true")
		}
		zero, _ := PaginateTasks(ltSampleTasks(), protocol.ListTasksParams{HistoryLength: ltIntPtr(0)})
		if len(zero.Tasks[0].History) != 0 {
			t.Errorf("historyLength 0 should drop all messages, got %d", len(zero.Tasks[0].History))
		}
	})

	t.Run("pagination copies do not mutate the input tasks", func(t *testing.T) {
		input := ltSampleTasks()
		if _, err := PaginateTasks(input, protocol.ListTasksParams{HistoryLength: ltIntPtr(1)}); err != nil {
			t.Fatal(err)
		}
		for _, task := range input {
			if len(task.History) != 3 || task.Artifacts == nil {
				t.Errorf("input task %s mutated: history=%d artifacts=%v", task.ID, len(task.History), task.Artifacts)
			}
		}
	})
}
