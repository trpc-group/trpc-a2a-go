// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package taskmanager

import (
	"fmt"
	"sort"
	"strconv"
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

// PaginateTasks sorts the filtered tasks by ID for stable pagination, applies
// offset-based pagination from params.PageToken/PageSize, trims each returned
// task's history per params.HistoryLength and strips artifacts unless
// params.IncludeArtifacts is set. Returned tasks are copies, so the caller's
// stored tasks are never mutated. It is shared by the in-memory and Redis
// task managers.
func PaginateTasks(filtered []*protocol.Task, params protocol.ListTasksParams) (*protocol.ListTasksResult, error) {
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].ID < filtered[j].ID })

	pageSize := ListTasksDefaultPageSize
	if params.PageSize != nil && *params.PageSize > 0 {
		pageSize = *params.PageSize
		if pageSize > ListTasksMaxPageSize {
			pageSize = ListTasksMaxPageSize
		}
	}
	offset := 0
	if params.PageToken != "" {
		o, err := strconv.Atoi(params.PageToken)
		if err != nil || o < 0 {
			return nil, fmt.Errorf("invalid pageToken %q", params.PageToken)
		}
		offset = o
	}

	totalSize := len(filtered)
	if offset > totalSize {
		offset = totalSize
	}
	end := offset + pageSize
	if end > totalSize {
		end = totalSize
	}

	tasks := make([]*protocol.Task, 0, end-offset)
	for _, task := range filtered[offset:end] {
		cp := *task // copy so trimming never mutates stored tasks
		if params.HistoryLength != nil && *params.HistoryLength >= 0 && len(cp.History) > *params.HistoryLength {
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
		result.NextPageToken = strconv.Itoa(end)
	}
	return result, nil
}
