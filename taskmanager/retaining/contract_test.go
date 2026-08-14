// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package retaining

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

var storeErrors = []error{
	ErrTaskNotFound,
	ErrVersionConflict,
	ErrTaskTerminal,
	ErrOperationConflict,
	ErrOperationExpired,
	ErrCommitUncertain,
	ErrCursorExpired,
	ErrPushConfigNotFound,
	ErrStoreClosed,
}

func TestStoreErrorsSupportErrorsIs(t *testing.T) {
	for i, target := range storeErrors {
		wrapped := fmt.Errorf("backend context: %w", target)
		if !errors.Is(wrapped, target) {
			t.Fatalf("errors.Is(%q) = false", target)
		}
		for j, other := range storeErrors {
			if i != j && errors.Is(wrapped, other) {
				t.Fatalf("error %q unexpectedly matches %q", target, other)
			}
		}
	}
}

func TestCursorPreservesOpaqueValue(t *testing.T) {
	const value = "1741597012345-7"
	cursor := Cursor(value)
	if got := string(cursor); got != value {
		t.Fatalf("Cursor round trip = %q, want %q", got, value)
	}
}

// Compile-time implementations pin the initial public contract without adding
// a production mock or another abstraction.
var (
	_ Store    = (*contractStore)(nil)
	_ Notifier = (*contractNotifier)(nil)
)

type contractStore struct{}

func (*contractStore) LoadTask(context.Context, TaskKey) (*TaskRecord, error) {
	return nil, nil
}

func (*contractStore) LoadTaskAndCursor(context.Context, TaskKey) (*TaskRecord, error) {
	return nil, nil
}

func (*contractStore) CommitTaskEvent(
	context.Context,
	CommitTaskEventRequest,
) (uint64, Cursor, error) {
	return 0, "", nil
}

func (*contractStore) CommitMessageEvent(
	context.Context,
	CommitMessageEventRequest,
) (uint64, Cursor, error) {
	return 0, "", nil
}

func (*contractStore) ReadTaskEvents(
	context.Context,
	TaskKey,
	Cursor,
	int,
) ([]StoredEvent, error) {
	return nil, nil
}

func (*contractStore) RefreshTaskLease(context.Context, TaskKey) error {
	return nil
}

func (*contractStore) SaveMessage(context.Context, string, string, protocol.Message) error {
	return nil
}

func (*contractStore) LoadHistory(
	context.Context,
	string,
	string,
	string,
	int,
) ([]protocol.Message, error) {
	return nil, nil
}

func (*contractStore) ListTasks(
	context.Context,
	string,
	string,
	protocol.ListTasksParams,
) (*protocol.ListTasksResult, error) {
	return nil, nil
}

func (*contractStore) SavePushConfig(
	context.Context,
	TaskKey,
	protocol.TaskPushNotificationConfig,
) (StoredPushConfig, error) {
	return StoredPushConfig{}, nil
}

func (*contractStore) GetPushConfig(
	context.Context,
	TaskKey,
	string,
) (StoredPushConfig, error) {
	return StoredPushConfig{}, nil
}

func (*contractStore) ListPushConfigs(context.Context, TaskKey) ([]StoredPushConfig, error) {
	return nil, nil
}

func (*contractStore) DeletePushConfig(context.Context, TaskKey, string) error {
	return nil
}

func (*contractStore) Close() error {
	return nil
}

type contractNotifier struct{}

func (*contractNotifier) Notify(context.Context, TaskKey, Cursor) error {
	return nil
}

func (*contractNotifier) Wait(context.Context, TaskKey, Cursor) error {
	return nil
}

func (*contractNotifier) Close() error {
	return nil
}
