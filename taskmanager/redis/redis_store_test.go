// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package redis

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	miniredis "github.com/alicebob/miniredis/v2"
	redisclient "github.com/redis/go-redis/v9"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/retaining"
)

func TestRedisStoreCommitCASIdempotencyAndTerminal(t *testing.T) {
	store, client := newTestStore(t)
	ctx := context.Background()
	key := retaining.TaskKey{Tenant: "tenant", Owner: "owner", ID: "task-1"}

	created := testStoreTask(key.ID, protocol.TaskStateWorking, "2026-08-14T01:00:00Z")
	createReq := retaining.CommitTaskEventRequest{
		Key: key, ExpectedVersion: 0, AllowCreate: true, OperationID: "create",
		Task: created, Event: testStoreStatusEvent(created),
	}
	version, cursor, err := store.CommitTaskEvent(ctx, createReq)
	if err != nil {
		t.Fatalf("CommitTaskEvent(create) error = %v", err)
	}
	if version != 1 || cursor == "" {
		t.Fatalf("CommitTaskEvent(create) = (%d, %q), want (1, non-empty)", version, cursor)
	}

	retryVersion, retryCursor, err := store.CommitTaskEvent(ctx, createReq)
	if err != nil {
		t.Fatalf("CommitTaskEvent(retry) error = %v", err)
	}
	if retryVersion != version || retryCursor != cursor {
		t.Fatalf("CommitTaskEvent(retry) = (%d, %q), want (%d, %q)",
			retryVersion, retryCursor, version, cursor)
	}
	if got := client.XLen(ctx, streamKey(key.Tenant, key.Owner, key.ID)).Val(); got != 1 {
		t.Fatalf("stream length after retry = %d, want 1", got)
	}
	if err := client.ZAdd(ctx, streamDedupeKey(key.Tenant, key.Owner, key.ID),
		redisclient.Z{Score: 1, Member: "legacy-op"}).Err(); err != nil {
		t.Fatalf("seed legacy operation error = %v", err)
	}
	legacyRetry := newStoreTaskRequest(key, 1, "legacy-op", "2026-08-14T01:00:30Z")
	if _, _, err := store.CommitTaskEvent(ctx, legacyRetry); !errors.Is(err, retaining.ErrOperationExpired) {
		t.Fatalf("legacy operation without result error = %v, want ErrOperationExpired", err)
	}

	changed := createReq
	changed.ExpectedVersion = 1
	if _, _, err := store.CommitTaskEvent(ctx, changed); !errors.Is(err, retaining.ErrOperationConflict) {
		t.Fatalf("changed request with reused OperationID error = %v, want ErrOperationConflict", err)
	}

	requests := []retaining.CommitTaskEventRequest{
		newStoreTaskRequest(key, 1, "concurrent-a", "2026-08-14T01:01:00Z"),
		newStoreTaskRequest(key, 1, "concurrent-b", "2026-08-14T01:02:00Z"),
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(requests))
	for i := range requests {
		wg.Add(1)
		go func(req retaining.CommitTaskEventRequest) {
			defer wg.Done()
			_, _, err := store.CommitTaskEvent(ctx, req)
			errs <- err
		}(requests[i])
	}
	wg.Wait()
	close(errs)
	successes, conflicts := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, retaining.ErrVersionConflict):
			conflicts++
		default:
			t.Fatalf("concurrent commit error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent commits successes/conflicts = %d/%d, want 1/1", successes, conflicts)
	}

	terminal := testStoreTask(key.ID, protocol.TaskStateCompleted, "2026-08-14T01:03:00Z")
	terminalReq := retaining.CommitTaskEventRequest{
		Key: key, ExpectedVersion: 2, OperationID: "terminal",
		Task: terminal, Event: testStoreStatusEvent(terminal),
	}
	terminalVersion, terminalCursor, err := store.CommitTaskEvent(ctx, terminalReq)
	if err != nil {
		t.Fatalf("CommitTaskEvent(terminal) error = %v", err)
	}
	if terminalVersion != 3 {
		t.Fatalf("terminal version = %d, want 3", terminalVersion)
	}
	if gotVersion, gotCursor, err := store.CommitTaskEvent(ctx, terminalReq); err != nil ||
		gotVersion != terminalVersion || gotCursor != terminalCursor {
		t.Fatalf("terminal retry = (%d, %q, %v), want (%d, %q, nil)",
			gotVersion, gotCursor, err, terminalVersion, terminalCursor)
	}
	late := newStoreTaskRequest(key, 3, "late", "2026-08-14T01:04:00Z")
	if _, _, err := store.CommitTaskEvent(ctx, late); !errors.Is(err, retaining.ErrTaskTerminal) {
		t.Fatalf("late terminal commit error = %v, want ErrTaskTerminal", err)
	}

	record, err := store.LoadTaskAndCursor(ctx, key)
	if err != nil {
		t.Fatalf("LoadTaskAndCursor error = %v", err)
	}
	if record.Version != 3 || record.Task.Status.State != protocol.TaskStateCompleted {
		t.Fatalf("loaded record = version %d state %s, want 3/completed",
			record.Version, record.Task.Status.State)
	}
}

func TestRedisStoreMessageAdvancesRevisionAndProjectsOnce(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	key := retaining.TaskKey{Tenant: "tenant", Owner: "owner", ID: "task-message"}
	create := newStoreTaskRequest(key, 0, "create", "2026-08-14T02:00:00Z")
	create.AllowCreate = true
	if version, _, err := store.CommitTaskEvent(ctx, create); err != nil || version != 1 {
		t.Fatalf("create = (%d, %v), want version 1", version, err)
	}

	contextID := "context-message"
	taskID := key.ID
	message := protocol.Message{
		MessageID: "message-1", ContextID: &contextID, TaskID: &taskID,
		Role: protocol.MessageRoleAgent, Parts: []*protocol.Part{},
	}
	req := retaining.CommitMessageEventRequest{
		Key: key, ExpectedVersion: 1, OperationID: "message-op", Message: message,
	}
	version, cursor, err := store.CommitMessageEvent(ctx, req)
	if err != nil {
		t.Fatalf("CommitMessageEvent error = %v", err)
	}
	if version != 2 {
		t.Fatalf("message version = %d, want 2", version)
	}
	if gotVersion, gotCursor, err := store.CommitMessageEvent(ctx, req); err != nil ||
		gotVersion != version || gotCursor != cursor {
		t.Fatalf("message retry = (%d, %q, %v), want (%d, %q, nil)",
			gotVersion, gotCursor, err, version, cursor)
	}
	changedRequest := req
	changedRequest.Message.Role = protocol.MessageRoleUser
	if _, _, err := store.CommitMessageEvent(ctx, changedRequest); !errors.Is(err, retaining.ErrOperationConflict) {
		t.Fatalf("changed Message with reused OperationID error = %v, want ErrOperationConflict", err)
	}

	history, err := store.LoadHistory(ctx, key.Tenant, key.Owner, contextID, -1)
	if err != nil {
		t.Fatalf("LoadHistory error = %v", err)
	}
	if len(history) != 1 || history[0].MessageID != message.MessageID {
		t.Fatalf("history = %#v, want one message %q", history, message.MessageID)
	}
	record, err := store.LoadTask(ctx, key)
	if err != nil {
		t.Fatalf("LoadTask error = %v", err)
	}
	if record.Version != 2 {
		t.Fatalf("record version after Message = %d, want 2", record.Version)
	}

	stale := req
	stale.OperationID = "stale-message"
	if _, _, err := store.CommitMessageEvent(ctx, stale); !errors.Is(err, retaining.ErrVersionConflict) {
		t.Fatalf("stale Message error = %v, want ErrVersionConflict", err)
	}
	conflict := message
	conflict.Role = protocol.MessageRoleUser
	if err := store.SaveMessage(ctx, key.Tenant, key.Owner, conflict); !errors.Is(err, retaining.ErrMessageConflict) {
		t.Fatalf("conflicting MessageID error = %v, want ErrMessageConflict", err)
	}
}

func TestRedisStoreCursorAdvancesPastMalformedAndDetectsTrim(t *testing.T) {
	store, client := newTestStore(t)
	ctx := context.Background()
	key := retaining.TaskKey{ID: "task-cursor"}
	create := newStoreTaskRequest(key, 0, "create", "2026-08-14T03:00:00Z")
	create.AllowCreate = true
	_, firstCursor, err := store.CommitTaskEvent(ctx, create)
	if err != nil {
		t.Fatalf("create error = %v", err)
	}
	second := newStoreTaskRequest(key, 1, "second", "2026-08-14T03:01:00Z")
	_, secondCursor, err := store.CommitTaskEvent(ctx, second)
	if err != nil {
		t.Fatalf("second error = %v", err)
	}

	malformedID, err := client.XAdd(ctx, &redisclient.XAddArgs{
		Stream: streamKey(key.Tenant, key.Owner, key.ID), Values: map[string]any{streamField: "not-json"},
	}).Result()
	if err != nil {
		t.Fatalf("XAdd malformed entry error = %v", err)
	}
	events, next, err := store.ReadTaskEvents(ctx, key, secondCursor, 1)
	if err != nil {
		t.Fatalf("ReadTaskEvents(malformed) error = %v", err)
	}
	if len(events) != 0 || string(next) != malformedID {
		t.Fatalf("ReadTaskEvents(malformed) = (%d events, %q), want (0, %q)",
			len(events), next, malformedID)
	}

	third := newStoreTaskRequest(key, 2, "third", "2026-08-14T03:02:00Z")
	_, thirdCursor, err := store.CommitTaskEvent(ctx, third)
	if err != nil {
		t.Fatalf("third error = %v", err)
	}
	events, next, err = store.ReadTaskEvents(ctx, key, next, 2)
	if err != nil {
		t.Fatalf("ReadTaskEvents(after malformed) error = %v", err)
	}
	if len(events) != 1 || events[0].Cursor != thirdCursor || next != thirdCursor {
		t.Fatalf("ReadTaskEvents(after malformed) = %#v, next %q, want third %q",
			events, next, thirdCursor)
	}

	if err := client.XTrimMaxLen(ctx, streamKey(key.Tenant, key.Owner, key.ID), 2).Err(); err != nil {
		t.Fatalf("XTrimMaxLen error = %v", err)
	}
	if _, _, err := store.ReadTaskEvents(ctx, key, firstCursor, 1); !errors.Is(err, retaining.ErrCursorExpired) {
		t.Fatalf("ReadTaskEvents(trimmed cursor) error = %v, want ErrCursorExpired", err)
	}
}

func TestRedisStorePushUsesAuthoritativeScopeAndGeneration(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	key := retaining.TaskKey{Tenant: "tenant", Owner: "owner", ID: "task-push"}
	authentication := &protocol.AuthenticationInfo{Scheme: "Bearer", Credentials: "secret"}
	config := protocol.TaskPushNotificationConfig{
		ID: "config-b", URL: "https://example.test/b", Authentication: authentication,
	}
	if _, err := store.SavePushConfig(ctx, key, config); !errors.Is(err, retaining.ErrTaskNotFound) {
		t.Fatalf("SavePushConfig(missing task) error = %v, want ErrTaskNotFound", err)
	}
	create := newStoreTaskRequest(key, 0, "create", "2026-08-14T04:00:00Z")
	create.AllowCreate = true
	if _, _, err := store.CommitTaskEvent(ctx, create); err != nil {
		t.Fatalf("create error = %v", err)
	}

	first, err := store.SavePushConfig(ctx, key, config)
	if err != nil {
		t.Fatalf("SavePushConfig error = %v", err)
	}
	if first.Config.Tenant != key.Tenant || first.Config.TaskID != key.ID ||
		first.Owner != key.Owner || first.Generation == "" {
		t.Fatalf("stored registration = %#v, want authoritative scope and generation", first)
	}
	authentication.Credentials = "mutated"
	if first.Config.Authentication == nil || first.Config.Authentication.Credentials != "secret" {
		t.Fatalf("SavePushConfig returned caller-aliased authentication: %#v", first.Config.Authentication)
	}
	second, err := store.SavePushConfig(ctx, key, config)
	if err != nil {
		t.Fatalf("SavePushConfig(update) error = %v", err)
	}
	if second.Generation == first.Generation {
		t.Fatal("updated push config reused generation")
	}
	if _, err := store.SavePushConfig(ctx, key, protocol.TaskPushNotificationConfig{
		Tenant: "other", TaskID: key.ID, URL: "https://example.test/conflict",
	}); err == nil {
		t.Fatal("SavePushConfig accepted conflicting tenant")
	}
	if _, err := store.SavePushConfig(ctx, key, protocol.TaskPushNotificationConfig{
		ID: "config-a", URL: "https://example.test/a",
	}); err != nil {
		t.Fatalf("SavePushConfig(config-a) error = %v", err)
	}
	registrations, err := store.ListPushConfigs(ctx, key)
	if err != nil {
		t.Fatalf("ListPushConfigs error = %v", err)
	}
	if len(registrations) != 2 || registrations[0].Config.ID != "config-a" ||
		registrations[1].Config.ID != "config-b" {
		t.Fatalf("ListPushConfigs order = %#v", registrations)
	}
	if err := store.DeletePushConfig(ctx, key, "config-b"); err != nil {
		t.Fatalf("DeletePushConfig error = %v", err)
	}
	if _, err := store.GetPushConfig(ctx, key, "config-b"); !errors.Is(err, retaining.ErrPushConfigNotFound) {
		t.Fatalf("GetPushConfig(deleted) error = %v, want ErrPushConfigNotFound", err)
	}
}

func TestRedisStoreScopeIsolationAndClose(t *testing.T) {
	server := miniredis.RunT(t)
	ctx := context.Background()
	keys := []retaining.TaskKey{
		{Tenant: "", Owner: "owner-a", ID: "same"},
		{Tenant: "~default", Owner: "owner-a", ID: "same"},
		{Tenant: "", Owner: "owner-b", ID: "same"},
	}
	stores := make([]*retainingStore, 0, len(keys))
	for i, key := range keys {
		client := redisclient.NewClient(&redisclient.Options{Addr: server.Addr()})
		store, err := newRetainingStore(client)
		if err != nil {
			t.Fatalf("newRetainingStore(%d) error = %v", i, err)
		}
		stores = append(stores, store)
		req := newStoreTaskRequest(key, 0, fmt.Sprintf("create-%d", i),
			fmt.Sprintf("2026-08-14T05:0%d:00Z", i))
		req.AllowCreate = true
		if version, _, err := store.CommitTaskEvent(ctx, req); err != nil || version != 1 {
			t.Fatalf("scope %d create = (%d, %v), want version 1", i, version, err)
		}
	}
	for i, key := range keys {
		record, err := stores[i].LoadTask(ctx, key)
		if err != nil {
			t.Fatalf("scope %d LoadTask error = %v", i, err)
		}
		if record.Task.Status.Timestamp != fmt.Sprintf("2026-08-14T05:0%d:00Z", i) {
			t.Fatalf("scope %d loaded timestamp = %q", i, record.Task.Status.Timestamp)
		}
	}
	for i, store := range stores {
		if err := store.Close(); err != nil {
			t.Fatalf("scope %d Close error = %v", i, err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("scope %d second Close error = %v", i, err)
		}
		if _, err := store.LoadTask(ctx, keys[i]); !errors.Is(err, retaining.ErrStoreClosed) {
			t.Fatalf("scope %d LoadTask after Close error = %v, want ErrStoreClosed", i, err)
		}
	}
}

func TestRedisStoreTaskOwnedKeysShareTaskHashInput(t *testing.T) {
	key := retaining.TaskKey{Tenant: "tenant", Owner: "owner", ID: "task-slot"}
	want := taskKey(key.Tenant, key.Owner, key.ID)
	keys := []string{
		streamKey(key.Tenant, key.Owner, key.ID),
		streamDedupeKey(key.Tenant, key.Owner, key.ID),
		storeOperationResultsKey(key.Tenant, key.Owner, key.ID),
		storePushKey(key.Tenant, key.Owner, key.ID),
	}
	for _, redisKey := range keys {
		start := len(redisKey) - len(want) - 2
		if start < 0 || redisKey[start:] != "{"+want+"}" {
			t.Fatalf("key %q does not use task hash input %q", redisKey, want)
		}
	}
}

func newTestStore(t *testing.T) (*retainingStore, *redisclient.Client) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redisclient.NewClient(&redisclient.Options{Addr: server.Addr()})
	store, err := newRetainingStore(client, WithExpireTime(time.Hour), WithMaxHistoryLength(10))
	if err != nil {
		t.Fatalf("newRetainingStore error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, client
}

func newStoreTaskRequest(
	key retaining.TaskKey, expected uint64, operationID, timestamp string,
) retaining.CommitTaskEventRequest {
	task := testStoreTask(key.ID, protocol.TaskStateWorking, timestamp)
	return retaining.CommitTaskEventRequest{
		Key: key, ExpectedVersion: expected, OperationID: operationID,
		Task: task, Event: testStoreStatusEvent(task),
	}
}

func testStoreTask(id string, state protocol.TaskState, timestamp string) *protocol.Task {
	return &protocol.Task{
		ID: id, ContextID: "context-" + id,
		Status: protocol.TaskStatus{State: state, Timestamp: timestamp},
	}
}

func testStoreStatusEvent(task *protocol.Task) protocol.StreamResponse {
	return protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{
		TaskID: task.ID, ContextID: task.ContextID, Status: task.Status,
	})
}
