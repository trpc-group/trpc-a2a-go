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
	"testing"
	"time"

	redisc "github.com/redis/go-redis/v9"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

type ownerContextKey struct{}

func ownerContext(owner string) context.Context {
	return context.WithValue(context.Background(), ownerContextKey{}, owner)
}

func testOwnerResolver(ctx context.Context) (string, error) {
	owner, _ := ctx.Value(ownerContextKey{}).(string)
	if owner == "" {
		return "", fmt.Errorf("owner is missing")
	}
	return owner, nil
}

func TestTaskCompanionKeysShareRedisClusterSlot(t *testing.T) {
	tests := []struct {
		name   string
		tenant string
		owner  string
		taskID string
	}{
		{name: "generated-style", taskID: "task-123"},
		{name: "tenant-and-owner", tenant: "tenant-a", owner: "alice", taskID: "task-123"},
		{name: "legacy-hash-tag", taskID: "job{evil}"},
		{name: "closing-brace", taskID: "job}evil"},
		{name: "empty-hash-tag", taskID: "legacy{}partition"},
		{name: "unclosed-hash-tag", taskID: "legacy{partition"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			task := taskKey(test.tenant, test.owner, test.taskID)
			want := redisClusterSlot(task)
			for name, key := range map[string]string{
				"stream":    streamKey(test.tenant, test.owner, test.taskID),
				"dedupe":    streamDedupeKey(test.tenant, test.owner, test.taskID),
				"execution": executionKey(test.tenant, test.owner, test.taskID),
			} {
				if got := redisClusterSlot(key); got != want {
					t.Errorf("%s key slot = %d, want task slot %d: task=%q companion=%q", name, got, want, task, key)
				}
			}
		})
	}
}

func TestRedisClusterSlotKnownVectors(t *testing.T) {
	tests := map[string]uint16{
		"123456789":            12739,
		"foo":                  12182,
		"foo{bar}":             5061,
		"foo{}{bar}":           8363,
		"{user1000}.following": 3443,
	}
	for key, want := range tests {
		if got := redisClusterSlot(key); got != want {
			t.Errorf("redisClusterSlot(%q) = %d, want %d", key, got, want)
		}
	}
}

func TestRedisClusterSlotTagsCoverAllSlots(t *testing.T) {
	for slot := uint16(0); slot < 16384; slot++ {
		tag := redisClusterSlotTag(slot)
		if got := redisClusterSlot(tag); got != slot {
			t.Fatalf("redisClusterSlotTag(%d) = %q in slot %d", slot, tag, got)
		}
	}
}

func TestTaskManagerOwnerIsolation(t *testing.T) {
	manager, mr := setupTest(t, scriptedExecutor(
		statusEvent(protocol.TaskStateWorking, nil),
		statusEvent(protocol.TaskStateInputRequired, nil),
	), WithOwnerResolver(testOwnerResolver), WithPushNotifications(push.Config{ManualDelivery: true}))

	const tenant = "shared-tenant"
	aliceCtx := ownerContext("alice")
	bobCtx := ownerContext("bob")
	request := sendParams("hello", "shared-context")
	request.Tenant = tenant
	request.Message.MessageID = "shared-message"

	aliceResult, err := manager.OnSendMessage(aliceCtx, request)
	if err != nil {
		t.Fatalf("alice send: %v", err)
	}
	aliceTask := aliceResult.GetTask()
	if aliceTask == nil {
		t.Fatal("alice send did not materialize a task")
	}

	if _, err := manager.OnGetTask(bobCtx, protocol.TaskQueryParams{Tenant: tenant, ID: aliceTask.ID}); !errors.Is(err, taskmanager.ErrTaskNotFoundSentinel) {
		t.Fatalf("bob must not get alice task: %v", err)
	}
	if _, err := manager.OnCancelTask(bobCtx, protocol.TaskIDParams{Tenant: tenant, ID: aliceTask.ID}); !errors.Is(err, taskmanager.ErrTaskNotFoundSentinel) {
		t.Fatalf("bob must not cancel alice task: %v", err)
	}
	if _, err := manager.OnResubscribe(bobCtx, protocol.TaskIDParams{Tenant: tenant, ID: aliceTask.ID}); !errors.Is(err, taskmanager.ErrTaskNotFoundSentinel) {
		t.Fatalf("bob must not subscribe to alice task: %v", err)
	}
	if _, err := manager.OnPushNotificationSet(bobCtx, protocol.TaskPushNotificationConfig{
		Tenant: tenant, TaskID: aliceTask.ID, URL: "https://bob.example/push",
	}); !errors.Is(err, taskmanager.ErrTaskNotFoundSentinel) {
		t.Fatalf("bob must not register push for alice task: %v", err)
	}

	aliceList, err := manager.OnListTasks(aliceCtx, protocol.ListTasksParams{Tenant: tenant})
	if err != nil || len(aliceList.Tasks) != 1 || aliceList.Tasks[0].ID != aliceTask.ID {
		t.Fatalf("alice list = %+v, err=%v", aliceList, err)
	}
	bobList, err := manager.OnListTasks(bobCtx, protocol.ListTasksParams{Tenant: tenant})
	if err != nil || len(bobList.Tasks) != 0 {
		t.Fatalf("bob list leaked alice task: %+v, err=%v", bobList, err)
	}

	if mr.Exists(taskKey(tenant, "bob", aliceTask.ID)) {
		t.Fatal("bob task key was created by alice's request")
	}
	if taskKey(tenant, "alice", aliceTask.ID) == taskKey(tenant, "bob", aliceTask.ID) {
		t.Fatal("different owners produced the same task key")
	}
	if streamKey(tenant, "alice", aliceTask.ID) == streamKey(tenant, "bob", aliceTask.ID) {
		t.Fatal("different owners produced the same stream key")
	}

	bobTask := &protocol.Task{ID: aliceTask.ID, ContextID: "shared-context",
		Status: protocol.TaskStatus{State: protocol.TaskStateWorking}}
	if err := manager.storeTask(bobCtx, tenant, "bob", bobTask); err != nil {
		t.Fatalf("store same task ID for bob: %v", err)
	}
	bobContextID := "shared-context"
	manager.storeMessage(context.Background(), tenant, "bob", protocol.Message{
		MessageID: "shared-message", ContextID: &bobContextID, Role: protocol.MessageRoleAgent,
	})
	if got, err := manager.OnGetTask(bobCtx, protocol.TaskQueryParams{Tenant: tenant, ID: aliceTask.ID}); err != nil || got.ID != aliceTask.ID || len(got.History) != 1 ||
		got.History[0].Role != protocol.MessageRoleAgent {
		t.Fatalf("same task ID must be usable by another owner: task=%+v err=%v", got, err)
	}
	if got, err := manager.OnGetTask(aliceCtx, protocol.TaskQueryParams{Tenant: tenant, ID: aliceTask.ID}); err != nil || len(got.History) != 1 || got.History[0].Role != protocol.MessageRoleUser {
		t.Fatalf("bob's shared message/context IDs changed alice history: task=%+v err=%v", got, err)
	}

	for _, tc := range []struct {
		ctx   context.Context
		owner string
		url   string
	}{
		{aliceCtx, "alice", "https://alice.example/push"},
		{bobCtx, "bob", "https://bob.example/push"},
	} {
		if _, err := manager.OnPushNotificationSet(tc.ctx, protocol.TaskPushNotificationConfig{
			Tenant: tenant, TaskID: aliceTask.ID, ID: "shared-config", URL: tc.url,
		}); err != nil {
			t.Fatalf("set %s push config: %v", tc.owner, err)
		}
	}
	aliceRegistrations, err := manager.readPushRegistrations(context.Background(), tenant, "alice", aliceTask.ID)
	if err != nil || len(aliceRegistrations) != 1 {
		t.Fatalf("alice registrations=%+v err=%v", aliceRegistrations, err)
	}
	if owner := aliceRegistrations[0].Owner; owner != "alice" {
		t.Fatalf("queued push owner=%q, want alice", owner)
	}
	if current, err := manager.isCurrentPushRegistration(context.Background(), aliceRegistrations[0]); err != nil || !current {
		t.Fatalf("alice push registration current=%v err=%v", current, err)
	}
	if err := manager.OnPushNotificationDelete(bobCtx, protocol.DeleteTaskPushNotificationConfigParams{
		Tenant: tenant, TaskID: aliceTask.ID, ID: "shared-config",
	}); err != nil {
		t.Fatalf("delete bob push config: %v", err)
	}
	if current, err := manager.isCurrentPushRegistration(context.Background(), aliceRegistrations[0]); err != nil || !current {
		t.Fatalf("bob push deletion invalidated alice registration: current=%v err=%v", current, err)
	}

	subCtx, cancel := context.WithCancel(bobCtx)
	defer cancel()
	bobStream, err := manager.OnResubscribe(subCtx, protocol.TaskIDParams{Tenant: tenant, ID: aliceTask.ID})
	if err != nil {
		t.Fatalf("bob subscribe to bob-owned task: %v", err)
	}
	if initial, ok := recvTimeout(t, bobStream); !ok || initial.GetTask() == nil {
		t.Fatalf("bob initial stream frame = %+v", initial)
	}
	if err := manager.appendTaskEvent(context.Background(), tenant, "alice", aliceTask.ID,
		protocol.NewStreamResponseMessage(agentReply("alice-only"))); err != nil {
		t.Fatalf("append alice event: %v", err)
	}
	select {
	case event := <-bobStream:
		t.Fatalf("alice event reached bob stream: %+v", event)
	case <-time.After(250 * time.Millisecond):
	}
	if err := manager.appendTaskEvent(context.Background(), tenant, "bob", aliceTask.ID,
		protocol.NewStreamResponseMessage(agentReply("bob-only"))); err != nil {
		t.Fatalf("append bob event: %v", err)
	}
	if event, ok := recvTimeout(t, bobStream); !ok || event.GetMessage() == nil ||
		event.GetMessage().Parts[0].TextContent() != "bob-only" {
		t.Fatalf("bob stream did not receive bob event: %+v", event)
	}
}

func TestTaskManagerOwnerResolverFailsBeforeWrite(t *testing.T) {
	manager, mr := setupTest(t, scriptedExecutor(statusEvent(protocol.TaskStateCompleted, nil)),
		WithOwnerResolver(testOwnerResolver))
	request := sendParams("hello", "context")
	request.Message.MessageID = "must-not-store"
	if _, err := manager.OnSendMessage(context.Background(), request); !errors.Is(err, taskmanager.ErrInternalErrorSentinel) {
		t.Fatalf("send without owner error = %v, want internal error", err)
	}
	if len(mr.Keys()) != 0 || len(manager.executions) != 0 {
		t.Fatalf("owner failure left state: keys=%v executions=%d", mr.Keys(), len(manager.executions))
	}
}

func TestTaskManagerTenantDataIsolationAndTaskIndex(t *testing.T) {
	manager, mr := setupTest(t, scriptedExecutor())
	ctx := context.Background()
	const taskID = "shared-task"
	const contextID = "shared-context"
	const messageID = "shared-message"

	if err := manager.storeTask(ctx, "tenant-a", "", &protocol.Task{
		ID: taskID, ContextID: contextID, Status: protocol.TaskStatus{State: protocol.TaskStateWorking},
	}); err != nil {
		t.Fatalf("store tenant-a task: %v", err)
	}
	if err := manager.storeTask(ctx, "tenant-b", "", &protocol.Task{
		ID: taskID, ContextID: contextID, Status: protocol.TaskStatus{State: protocol.TaskStateInputRequired},
	}); err != nil {
		t.Fatalf("store tenant-b task: %v", err)
	}

	contextA := contextID
	manager.storeMessage(ctx, "tenant-a", "", protocol.Message{
		MessageID: messageID, ContextID: &contextA, Role: protocol.MessageRoleUser,
	})

	contextB := contextID
	manager.storeMessage(ctx, "tenant-b", "", protocol.Message{
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

	indexed, err := manager.client.ZRange(ctx, taskIndexKey("tenant-a", ""), 0, -1).Result()
	if err != nil || len(indexed) != 1 || indexed[0] != taskID {
		t.Fatalf("tenant task index = %v, err=%v", indexed, err)
	}
	if err := manager.client.ZAdd(ctx, taskIndexKey("tenant-a", ""), redisc.Z{Member: "expired-task"}).Err(); err != nil {
		t.Fatalf("seed stale index member: %v", err)
	}
	if _, err := manager.OnListTasks(ctx, protocol.ListTasksParams{Tenant: "tenant-a"}); err != nil {
		t.Fatalf("list with stale member: %v", err)
	}
	if _, err := manager.client.ZScore(ctx, taskIndexKey("tenant-a", ""), "expired-task").Result(); !errors.Is(err, redisc.Nil) {
		t.Fatalf("stale task index member was not pruned: %v", err)
	}

	// An unindexed task key proves ListTasks no longer discovers
	// data through SCAN. Existing deployments must drain or explicitly migrate
	// pre-index tasks before switching list traffic.
	mr.Set(taskKey("", "", "unindexed"), `{"id":"unindexed"}`)
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

	if _, err := manager.storePushConfig(ctx, "", protocol.TaskPushNotificationConfig{
		Tenant: "tenant-a", TaskID: taskID, ID: "shared-config", URL: "https://a.example/push",
	}); err != nil {
		t.Fatalf("store tenant-a push config: %v", err)
	}
	if _, err := manager.storePushConfig(ctx, "", protocol.TaskPushNotificationConfig{
		Tenant: "tenant-b", TaskID: taskID, ID: "shared-config", URL: "https://b.example/push",
	}); err != nil {
		t.Fatalf("store tenant-b push config: %v", err)
	}
	configsA, err := manager.readPushConfigs(ctx, "tenant-a", "", taskID)
	if err != nil || len(configsA) != 1 || configsA[0].URL != "https://a.example/push" {
		t.Fatalf("tenant-a push config leaked: configs=%+v err=%v", configsA, err)
	}

	if got, want := taskKey("", "", taskID), "tenant:~default:"+taskPrefix+taskID; got != want {
		t.Fatalf("empty tenant task key = %q, want %q", got, want)
	}
	if got, want := taskKey("", "alice", taskID),
		"tenant:~default:owner:YWxpY2U:"+taskPrefix+taskID; got != want {
		t.Fatalf("default tenant owner task key = %q, want %q", got, want)
	}
	if taskKey("_default", "", taskID) == taskKey("", "", taskID) {
		t.Fatal("explicit _default tenant collided with the internal default namespace")
	}
	if taskKey(string([]byte{0xfd, 0xd7, 0x9f, 0x6a, 0xe9, 0x6d}), "", taskID) == taskKey("", "", taskID) {
		t.Fatal("base64-encoded tenant collided with the internal default namespace")
	}
	if got, want := streamKey("", "", taskID), "stream:{"+taskKey("", "", taskID)+"}"; got != want {
		t.Fatalf("default tenant stream hash tag = %q, want %q", got, want)
	}
	if taskKey("tenant-a", "", taskID) == taskKey("tenant-b", "", taskID) {
		t.Fatal("different tenants produced the same Redis task key")
	}
	if got, want := streamKey("tenant-a", "", taskID), "stream:{"+taskKey("tenant-a", "", taskID)+"}"; got != want {
		t.Fatalf("tenant stream hash tag = %q, want %q", got, want)
	}

	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	subscriberB, err := manager.OnResubscribe(subCtx, protocol.TaskIDParams{Tenant: "tenant-b", ID: taskID})
	if err != nil {
		t.Fatalf("resubscribe tenant-b: %v", err)
	}
	if initial, ok := recvTimeout(t, subscriberB); !ok || initial.GetTask() == nil {
		t.Fatalf("tenant-b initial snapshot = %+v", initial)
	}
	foreign := protocol.NewStreamResponseMessage(agentReply("tenant-a-only"))
	if err := manager.appendTaskEvent(ctx, "tenant-a", "", taskID, foreign); err != nil {
		t.Fatalf("append tenant-a event: %v", err)
	}
	select {
	case event, ok := <-subscriberB:
		t.Fatalf("tenant-a event reached tenant-b subscriber: event=%+v open=%v", event, ok)
	case <-time.After(250 * time.Millisecond):
	}
	local := protocol.NewStreamResponseMessage(agentReply("tenant-b-only"))
	if err := manager.appendTaskEvent(ctx, "tenant-b", "", taskID, local); err != nil {
		t.Fatalf("append tenant-b event: %v", err)
	}
	if event, ok := recvTimeout(t, subscriberB); !ok || event.GetMessage() == nil ||
		event.GetMessage().Parts[0].TextContent() != "tenant-b-only" {
		t.Fatalf("tenant-b subscriber did not receive its event: %+v", event)
	}

	liveA := &liveExecution{cancel: func() {}}
	liveB := &liveExecution{cancel: func() {}}
	if err := manager.registerExecution(ctx, "tenant-a", "", taskID, liveA); err != nil {
		t.Fatalf("register tenant-a execution: %v", err)
	}
	if err := manager.registerExecution(ctx, "tenant-b", "", taskID, liveB); err != nil {
		t.Fatalf("register tenant-b execution with the same task ID: %v", err)
	}
	manager.releaseExecution("tenant-a", "", taskID, liveA)
	manager.releaseExecution("tenant-b", "", taskID, liveB)
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
	configs, err := manager.readPushConfigs(context.Background(), "tenant-a", "", task.ID)
	if err != nil || len(configs) != 1 || configs[0].TaskID != task.ID || configs[0].Authentication == nil ||
		configs[0].Authentication.Credentials != "original-credentials" {
		t.Fatalf("processor mutation changed stored push scope: configs=%+v err=%v", configs, err)
	}
	configs, err = manager.readPushConfigs(context.Background(), "mutated-tenant", "", "mutated-task")
	if err != nil || len(configs) != 0 {
		t.Fatalf("processor moved push config into another tenant: configs=%+v err=%v", configs, err)
	}
}
