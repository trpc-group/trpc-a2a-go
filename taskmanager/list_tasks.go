// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package taskmanager

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

// ListTasksDefaultPageSize is the default ListTasks page size (v1.0 spec: 1-100, default 50).
const ListTasksDefaultPageSize = 50

// ListTasksMaxPageSize is the maximum ListTasks page size.
const ListTasksMaxPageSize = 100

// ParseListTasksStatusTimestampAfter parses the optional RFC3339 filter bound.
// An empty string yields the zero time (no bound).
func ParseListTasksStatusTimestampAfter(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid statusTimestampAfter %q: %w", s, err)
	}
	return t, nil
}

// TaskMatchesListFilter reports whether a task passes the ListTasks filters
// (contextId, status, and the status timestamp bound).
func TaskMatchesListFilter(task *protocol.Task, params protocol.ListTasksParams, afterTime time.Time) bool {
	if params.ContextID != "" && task.ContextID != params.ContextID {
		return false
	}
	if params.Status != "" && task.Status.State != params.Status {
		return false
	}
	if !afterTime.IsZero() {
		ts, err := time.Parse(time.RFC3339, task.Status.Timestamp)
		// v1.0 statusTimestampAfter is inclusive: keep tasks whose status
		// timestamp is greater than OR EQUAL to the bound (proto: "greater than
		// or equal to this value").
		if err != nil || ts.Before(afterTime) {
			return false
		}
	}
	return true
}

type listTasksCursor struct {
	Timestamp string `json:"t"`
	ID        string `json:"i"`
}

func listTaskSortTime(timestamp string) time.Time {
	if timestamp == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		// Retained tasks created by an application may contain a malformed
		// timestamp. Keep ListTasks available and put those tasks in the stable
		// zero-time bucket rather than failing the entire result set.
		return time.Time{}
	}
	return parsed
}

func encodeListTasksCursor(task *protocol.Task) (string, error) {
	timestamp := ""
	if parsed := listTaskSortTime(task.Status.Timestamp); !parsed.IsZero() {
		timestamp = parsed.UTC().Format(time.RFC3339Nano)
	}
	payload, err := json.Marshal(listTasksCursor{Timestamp: timestamp, ID: task.ID})
	if err != nil {
		return "", fmt.Errorf("encode ListTasks pageToken: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeListTasksCursor(token string) (listTasksCursor, time.Time, error) {
	payload, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return listTasksCursor{}, time.Time{}, ErrInvalidParams("invalid pageToken")
	}
	var cursor listTasksCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.ID == "" {
		return listTasksCursor{}, time.Time{}, ErrInvalidParams("invalid pageToken")
	}
	if cursor.Timestamp == "" {
		return cursor, time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, cursor.Timestamp)
	if err != nil {
		return listTasksCursor{}, time.Time{}, ErrInvalidParams("invalid pageToken")
	}
	return cursor, parsed, nil
}

// PaginateTasks sorts the filtered tasks most-recently-updated first, applies
// keyset pagination from params.PageToken/PageSize, trims each returned
// task's history per params.HistoryLength and strips artifacts unless
// params.IncludeArtifacts is set. Returned tasks are copies, so the caller's
// stored tasks are never mutated. It is shared by the in-memory and Redis
// task managers.
func PaginateTasks(filtered []*protocol.Task, params protocol.ListTasksParams) (*protocol.ListTasksResult, error) {
	if params.PageSize != nil && (*params.PageSize < 1 || *params.PageSize > ListTasksMaxPageSize) {
		return nil, ErrInvalidParams("pageSize must be between 1 and 100")
	}
	if params.HistoryLength != nil && *params.HistoryLength < 0 {
		return nil, ErrInvalidParams("historyLength must be non-negative")
	}

	// Spec §3.1.4: "Implementations MUST return tasks sorted by their status
	// timestamp time in descending order". Parse the timestamps because valid
	// RFC3339 representations with different fractional precision do not compare
	// correctly as strings. The task ID breaks ties.
	sort.Slice(filtered, func(i, j int) bool {
		iTime := listTaskSortTime(filtered[i].Status.Timestamp)
		jTime := listTaskSortTime(filtered[j].Status.Timestamp)
		if !iTime.Equal(jTime) {
			return iTime.After(jTime)
		}
		return filtered[i].ID < filtered[j].ID
	})

	pageSize := ListTasksDefaultPageSize
	if params.PageSize != nil {
		pageSize = *params.PageSize
	}
	start := 0
	if params.PageToken != "" {
		cursor, cursorTime, err := decodeListTasksCursor(params.PageToken)
		if err != nil {
			return nil, err
		}
		// Find the first task strictly after the cursor in the sorted order.
		// This remains valid when the cursor task has since been deleted.
		start = sort.Search(len(filtered), func(i int) bool {
			taskTime := listTaskSortTime(filtered[i].Status.Timestamp)
			return taskTime.Before(cursorTime) ||
				(taskTime.Equal(cursorTime) && filtered[i].ID > cursor.ID)
		})
	}

	totalSize := len(filtered)
	end := start + pageSize
	if end > totalSize {
		end = totalSize
	}

	tasks := make([]*protocol.Task, 0, end-start)
	for _, task := range filtered[start:end] {
		cp := *task // copy so trimming never mutates stored tasks
		if params.HistoryLength != nil && len(cp.History) > *params.HistoryLength {
			cp.History = cp.History[len(cp.History)-*params.HistoryLength:]
		}
		if params.IncludeArtifacts == nil || !*params.IncludeArtifacts {
			cp.Artifacts = nil
		}
		tasks = append(tasks, &cp)
	}

	result := &protocol.ListTasksResult{
		Tasks:     tasks,
		PageSize:  pageSize,
		TotalSize: totalSize,
	}
	if end < totalSize {
		token, err := encodeListTasksCursor(filtered[end-1])
		if err != nil {
			return nil, err
		}
		result.NextPageToken = token
	}
	return result, nil
}
