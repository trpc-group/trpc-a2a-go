// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package taskmanager

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
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

func ltIntPtr(i int) *int    { return &i }
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

// Spec §3.1.4: "Implementations MUST return tasks sorted by their status
// timestamp time in descending order (most recently updated tasks first)."
func TestPaginateTasksOrdersByStatusTimestampDescending(t *testing.T) {
	mk := func(id, timestamp string) *protocol.Task {
		return &protocol.Task{ID: id, Status: protocol.TaskStatus{Timestamp: timestamp}}
	}
	tasks := []*protocol.Task{
		mk("t-whole", "2024-01-01T00:00:00Z"),
		mk("t-b", "2024-01-01T00:00:00.100Z"),
		mk("t-a", "2024-01-01T00:00:00.100Z"),
		mk("t-empty", ""),
	}
	res, err := PaginateTasks(tasks, protocol.ListTasksParams{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"t-a", "t-b", "t-whole", "t-empty"}
	for i, id := range want {
		if res.Tasks[i].ID != id {
			t.Fatalf("position %d: want %s, got %s (full order %v)", i, id, res.Tasks[i].ID, res.Tasks)
		}
	}

	// Paging must walk the same descending order.
	page, err := PaginateTasks(tasks, protocol.ListTasksParams{PageSize: ltIntPtr(1)})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Tasks) != 1 || page.Tasks[0].ID != "t-a" {
		t.Errorf("first page should hold the most recent task, got %v", page.Tasks)
	}
}

func TestPaginateTasks(t *testing.T) {
	t.Run("equal timestamps fall back to the ID tiebreak", func(t *testing.T) {
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
		var got []string
		token := ""
		for {
			page, err := PaginateTasks(ltSampleTasks(), protocol.ListTasksParams{
				PageSize: ltIntPtr(2), PageToken: token,
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, task := range page.Tasks {
				got = append(got, task.ID)
			}
			if page.NextPageToken == "" {
				break
			}
			if page.NextPageToken == "2" || page.NextPageToken == "4" {
				t.Fatalf("page token must be opaque, got %q", page.NextPageToken)
			}
			token = page.NextPageToken
		}
		want := []string{"t1", "t2", "t3", "t4", "t5"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	})

	t.Run("invalid pageToken errors", func(t *testing.T) {
		tokens := []string{
			"not-base64!",
			base64.RawURLEncoding.EncodeToString([]byte(`not-json`)),
			base64.RawURLEncoding.EncodeToString([]byte(`{"t":"2024-01-01T00:00:00Z"}`)),
			base64.RawURLEncoding.EncodeToString([]byte(`{"t":"not-a-time","i":"task"}`)),
		}
		for _, token := range tokens {
			_, err := PaginateTasks(ltSampleTasks(), protocol.ListTasksParams{PageToken: token})
			if !errors.Is(err, ErrInvalidParamsSentinel) {
				t.Errorf("pageToken %q: got %v, want invalid params", token, err)
			}
		}
	})

	t.Run("cursor continues after its task is deleted", func(t *testing.T) {
		input := ltSampleTasks()
		first, err := PaginateTasks(input, protocol.ListTasksParams{PageSize: ltIntPtr(1)})
		if err != nil {
			t.Fatal(err)
		}
		var remaining []*protocol.Task
		for _, task := range input {
			if task.ID != first.Tasks[0].ID {
				remaining = append(remaining, task)
			}
		}
		next, err := PaginateTasks(remaining, protocol.ListTasksParams{
			PageSize: ltIntPtr(1), PageToken: first.NextPageToken,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(next.Tasks) != 1 || next.Tasks[0].ID != "t2" {
			t.Fatalf("next page = %v, want t2", next.Tasks)
		}
	})

	t.Run("updates before the cursor do not repeat earlier results", func(t *testing.T) {
		mk := func(id, timestamp string) *protocol.Task {
			return &protocol.Task{ID: id, Status: protocol.TaskStatus{Timestamp: timestamp}}
		}
		input := []*protocol.Task{
			mk("a", "2024-01-01T00:00:01Z"),
			mk("b", "2024-01-01T00:00:02Z"),
			mk("c", "2024-01-01T00:00:03Z"),
			mk("d", "2024-01-01T00:00:00Z"),
		}
		first, err := PaginateTasks(input, protocol.ListTasksParams{PageSize: ltIntPtr(2)})
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range input {
			if task.ID == "a" {
				task.Status.Timestamp = "2024-01-01T00:00:04Z"
			}
		}
		next, err := PaginateTasks(input, protocol.ListTasksParams{
			PageSize: ltIntPtr(2), PageToken: first.NextPageToken,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(next.Tasks) != 1 || next.Tasks[0].ID != "d" {
			t.Fatalf("next page = %v, want only d", next.Tasks)
		}
	})

	t.Run("pageSize validates range", func(t *testing.T) {
		for _, pageSize := range []int{-1, 0, ListTasksMaxPageSize + 1} {
			_, err := PaginateTasks(ltSampleTasks(), protocol.ListTasksParams{PageSize: ltIntPtr(pageSize)})
			if !errors.Is(err, ErrInvalidParamsSentinel) {
				t.Errorf("pageSize %d: got %v, want invalid params", pageSize, err)
			}
		}
		for _, pageSize := range []int{1, ListTasksMaxPageSize} {
			res, err := PaginateTasks(ltSampleTasks(), protocol.ListTasksParams{PageSize: ltIntPtr(pageSize)})
			if err != nil || res.PageSize != pageSize {
				t.Errorf("pageSize %d: result=%+v err=%v", pageSize, res, err)
			}
		}
	})

	t.Run("negative historyLength errors", func(t *testing.T) {
		_, err := PaginateTasks(ltSampleTasks(), protocol.ListTasksParams{HistoryLength: ltIntPtr(-1)})
		if !errors.Is(err, ErrInvalidParamsSentinel) {
			t.Errorf("got %v, want invalid params", err)
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
