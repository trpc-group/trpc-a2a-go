// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package redis

import (
	"context"
	"errors"
	"testing"

	redisc "github.com/redis/go-redis/v9"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

func TestTaskManagerTenantDataIsolationAndTaskIndex(t *testing.T) {
	manager, mr := setupTest(t, scriptedExecutor())
	ctx := context.Background()
	const taskID = "shared-task"
	const contextID = "shared-context"
	const messageID = "shared-message"

	if err := manager.storeTask(ctx, "tenant-a", &protocol.Task{
		ID: taskID, ContextID: contextID, Status: protocol.TaskStatus{State: protocol.TaskStateWorking},
	}); err != nil {
		t.Fatalf("store tenant-a task: %v", err)
	}
	if err := manager.storeTask(ctx, "tenant-b", &protocol.Task{
		ID: taskID, ContextID: contextID, Status: protocol.TaskStatus{State: protocol.TaskStateInputRequired},
	}); err != nil {
		t.Fatalf("store tenant-b task: %v", err)
	}

	contextA := contextID
	manager.storeMessage(ctx, "tenant-a", protocol.Message{
		MessageID: messageID, ContextID: &contextA, Role: protocol.MessageRoleUser,
	})
	contextB := contextID
	manager.storeMessage(ctx, "tenant-b", protocol.Message{
		MessageID: messageID, ContextID: &contextB, Role: protocol.MessageRoleAgent,
	})

	taskA, err := manager.OnGetTask(ctx, protocol.TaskQueryParams{Tenant: "tenant-a", ID: taskID})
	if err != nil {
		t.Fatalf("get tenant-a task: %v", err)
	}
	if taskA.Status.State != protocol.TaskStateWorking || len(taskA.History) != 1 ||
		taskA.History[0].Role != protocol.MessageRoleUser {
		t.Fatalf("tenant-a received foreign data: %+v", taskA)
	}
	taskB, err := manager.OnGetTask(ctx, protocol.TaskQueryParams{Tenant: "tenant-b", ID: taskID})
	if err != nil {
		t.Fatalf("get tenant-b task: %v", err)
	}
	if taskB.Status.State != protocol.TaskStateInputRequired || len(taskB.History) != 1 ||
		taskB.History[0].Role != protocol.MessageRoleAgent {
		t.Fatalf("tenant-b received foreign data: %+v", taskB)
	}

	if _, err := manager.OnGetTask(ctx, protocol.TaskQueryParams{Tenant: "tenant-c", ID: taskID}); !errors.Is(err, taskmanager.ErrTaskNotFoundSentinel) {
		t.Fatalf("tenant-c must not see another tenant's task, got %v", err)
	}
	listA, err := manager.OnListTasks(ctx, protocol.ListTasksParams{Tenant: "tenant-a"})
	if err != nil || len(listA.Tasks) != 1 || listA.Tasks[0].Status.State != protocol.TaskStateWorking {
		t.Fatalf("tenant-a list leaked another tenant: result=%+v err=%v", listA, err)
	}

	indexed, err := manager.client.ZRange(ctx, taskIndexKey("tenant-a"), 0, -1).Result()
	if err != nil || len(indexed) != 1 || indexed[0] != taskID {
		t.Fatalf("tenant task index = %v, err=%v", indexed, err)
	}
	if err := manager.client.ZAdd(ctx, taskIndexKey("tenant-a"), redisc.Z{Member: "expired-task"}).Err(); err != nil {
		t.Fatalf("seed stale index member: %v", err)
	}
	if _, err := manager.OnListTasks(ctx, protocol.ListTasksParams{Tenant: "tenant-a"}); err != nil {
		t.Fatalf("list with stale member: %v", err)
	}
	if _, err := manager.client.ZScore(ctx, taskIndexKey("tenant-a"), "expired-task").Result(); !errors.Is(err, redisc.Nil) {
		t.Fatalf("stale task index member was not pruned: %v", err)
	}

	// An unindexed legacy-looking task key proves ListTasks no longer discovers
	// data through SCAN. Existing deployments must drain or explicitly migrate
	// pre-index tasks before switching list traffic.
	mr.Set(taskPrefix+"unindexed", `{"id":"unindexed"}`)
	defaultList, err := manager.OnListTasks(ctx, protocol.ListTasksParams{})
	if err != nil || len(defaultList.Tasks) != 0 {
		t.Fatalf("ListTasks unexpectedly scanned unindexed keys: result=%+v err=%v", defaultList, err)
	}

	if _, err := manager.OnCancelTask(ctx, protocol.TaskIDParams{Tenant: "tenant-a", ID: taskID}); err != nil {
		t.Fatalf("cancel tenant-a task: %v", err)
	}
	taskB, err = manager.OnGetTask(ctx, protocol.TaskQueryParams{Tenant: "tenant-b", ID: taskID})
	if err != nil || taskB.Status.State != protocol.TaskStateInputRequired {
		t.Fatalf("canceling tenant-a changed tenant-b: task=%+v err=%v", taskB, err)
	}

	if _, err := manager.storePushConfig(ctx, protocol.TaskPushNotificationConfig{
		Tenant: "tenant-a", TaskID: taskID, ID: "shared-config", URL: "https://a.example/push",
	}); err != nil {
		t.Fatalf("store tenant-a push config: %v", err)
	}
	if _, err := manager.storePushConfig(ctx, protocol.TaskPushNotificationConfig{
		Tenant: "tenant-b", TaskID: taskID, ID: "shared-config", URL: "https://b.example/push",
	}); err != nil {
		t.Fatalf("store tenant-b push config: %v", err)
	}
	configsA, err := manager.readPushConfigs(ctx, "tenant-a", taskID)
	if err != nil || len(configsA) != 1 || configsA[0].URL != "https://a.example/push" {
		t.Fatalf("tenant-a push config leaked: configs=%+v err=%v", configsA, err)
	}

	if taskKey("", taskID) != taskPrefix+taskID {
		t.Fatalf("empty tenant must preserve legacy Redis keys, got %q", taskKey("", taskID))
	}
	if taskKey("tenant-a", taskID) == taskKey("tenant-b", taskID) {
		t.Fatal("different tenants produced the same Redis task key")
	}
	if got, want := streamKey("tenant-a", taskID), "stream:{"+taskKey("tenant-a", taskID)+"}"; got != want {
		t.Fatalf("tenant stream hash tag = %q, want %q", got, want)
	}

	subscriberA := newTaskSubscriber(taskID, 1, false)
	manager.subMu.Lock()
	manager.subscribers[newScopedID("tenant-a", taskID)] = []*taskSubscriber{subscriberA}
	manager.subMu.Unlock()
	manager.notifySubscribers("tenant-b", taskID, protocol.NewStreamResponseTask(taskB))
	select {
	case <-subscriberA.Channel():
		t.Fatal("tenant-b event reached tenant-a subscriber")
	default:
	}

	liveA := &liveExecution{cancel: func() {}}
	liveB := &liveExecution{cancel: func() {}}
	if err := manager.registerExecution(ctx, "tenant-a", taskID, liveA); err != nil {
		t.Fatalf("register tenant-a execution: %v", err)
	}
	if err := manager.registerExecution(ctx, "tenant-b", taskID, liveB); err != nil {
		t.Fatalf("register tenant-b execution with the same task ID: %v", err)
	}
	manager.releaseExecution("tenant-a", taskID, liveA)
	manager.releaseExecution("tenant-b", taskID, liveB)
}

func TestTaskManagerProcessorCannotMutateTenantScope(t *testing.T) {
	manager, _ := setupTest(t, executorFunc(func(
		_ context.Context,
		ec *taskmanager.ExecContext,
	) (<-chan protocol.StreamEvent, error) {
		ec.Tenant = "mutated-tenant"
		ec.TaskID = "mutated-task"
		ec.ContextID = "mutated-context"
		if ec.PushConfig != nil {
			ec.PushConfig.Tenant = "mutated-tenant"
			ec.PushConfig.TaskID = "mutated-task"
			if ec.PushConfig.Authentication != nil {
				ec.PushConfig.Authentication.Credentials = "mutated-credentials"
			}
		}
		events := make(chan protocol.StreamEvent, 1)
		events <- statusEvent(protocol.TaskStateCompleted, nil)
		close(events)
		return events, nil
	}), WithPushNotifications(push.Config{ManualDelivery: true}))

	message := protocol.NewMessage(protocol.MessageRoleUser, []*protocol.Part{protocol.NewTextPart("hello")})
	result, err := manager.OnSendMessage(context.Background(), protocol.SendMessageParams{
		Tenant: "tenant-a",
		Configuration: &protocol.SendMessageConfiguration{PushConfig: &protocol.TaskPushNotificationConfig{
			URL:            "https://example.com/push",
			Authentication: &protocol.AuthenticationInfo{Scheme: "Bearer", Credentials: "original-credentials"},
		}},
		Message: message,
	})
	if err != nil {
		t.Fatalf("send message: %v", err)
	}
	task := result.GetTask()
	if task == nil || task.ID == "mutated-task" || task.ContextID == "mutated-context" {
		t.Fatalf("processor mutation changed framework scope: %+v", task)
	}
	if _, err := manager.OnGetTask(context.Background(), protocol.TaskQueryParams{
		Tenant: "tenant-a", ID: task.ID,
	}); err != nil {
		t.Fatalf("task was not stored in the request tenant: %v", err)
	}
	if _, err := manager.OnGetTask(context.Background(), protocol.TaskQueryParams{
		Tenant: "mutated-tenant", ID: task.ID,
	}); !errors.Is(err, taskmanager.ErrTaskNotFoundSentinel) {
		t.Fatalf("processor moved task into another tenant: %v", err)
	}
	configs, err := manager.readPushConfigs(context.Background(), "tenant-a", task.ID)
	if err != nil || len(configs) != 1 || configs[0].TaskID != task.ID || configs[0].Authentication == nil ||
		configs[0].Authentication.Credentials != "original-credentials" {
		t.Fatalf("processor mutation changed stored push scope: configs=%+v err=%v", configs, err)
	}
	configs, err = manager.readPushConfigs(context.Background(), "mutated-tenant", "mutated-task")
	if err != nil || len(configs) != 0 {
		t.Fatalf("processor moved push config into another tenant: configs=%+v err=%v", configs, err)
	}
}
