// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package redis provides a Redis-based implementation of the A2A TaskManager interface.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"trpc.group/trpc-go/trpc-a2a-go/v2/internal/jsonrpc"
	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

const (
	// Key prefixes for Redis storage.
	messagePrefix          = "msg:"
	conversationPrefix     = "conv:"
	taskPrefix             = "task:"
	pushNotificationPrefix = "push:"

	// Default expiration time for Redis keys (1 hour).
	defaultExpiration = 1 * time.Hour

	// Default configuration values.
	defaultMaxHistoryLength         = 100
	defaultTaskSubscriberBufferSize = 1024
)

// TaskManager provides a concrete, Redis-based implementation of the
// TaskManager interface. It persists messages, conversations, and tasks in
// Redis and delegates the agent logic to an injected Processor: the MessageProcessor
// reports progress on an event channel and the manager owns the task
// lifecycle (lazy creation, persistence, subscriber fan-out).
// It is safe for concurrent use.
type TaskManager struct {
	// processor is the user-provided agent logic.
	processor taskmanager.MessageProcessor
	// client is the Redis client.
	client redis.UniversalClient
	// expiration is the time after which Redis keys expire.
	expiration time.Duration

	// subMu is a mutex for the subscribers map.
	subMu sync.RWMutex
	// subscribers is a map of task IDs to subscriber channels.
	subscribers map[string][]*taskSubscriber

	// cancelMu is a mutex for the executions map and the closed flag.
	cancelMu sync.RWMutex
	// executions maps task IDs to live execution handles so OnCancelTask can
	// cancel the MessageProcessor's context.
	executions map[string]*liveExecution
	// closed rejects new runs once Close has begun tearing the manager down.
	closed bool
	// engineWg counts live drain engines so Close can wait for their final
	// persists before closing the Redis client.
	engineWg sync.WaitGroup
	// closeOnce/closeErr make Close idempotent.
	closeOnce sync.Once
	closeErr  error

	// pushSender delivers task updates to registered webhooks as events occur;
	// nil disables push (the config RPCs return PushNotificationNotSupported).
	pushSender push.Sender
	// pushWg counts in-flight push deliveries so Close waits for them before it
	// closes the Redis client (a delivery reads its config through that client).
	pushWg sync.WaitGroup
	// pushCtx bounds every push delivery; pushCancel is called in Close so a slow
	// or hung webhook cannot stall shutdown — the in-flight send (and its config
	// read) is cancelled rather than awaited to its full timeout.
	pushCtx    context.Context
	pushCancel context.CancelFunc

	// options
	options *TaskManagerOptions
}

// NewTaskManager creates a new Redis-based TaskManager with the provided options.
func NewTaskManager(
	processor taskmanager.MessageProcessor,
	client redis.UniversalClient,
	opts ...TaskManagerOption,
) (*TaskManager, error) {
	if processor == nil {
		return nil, errors.New("processor cannot be nil")
	}
	if client == nil {
		return nil, errors.New("redis client cannot be nil")
	}

	// Test connection.
	if err := client.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	// Apply default options
	options := DefaultRedisTaskManagerOptions()

	// Apply user options
	for _, opt := range opts {
		opt(options)
	}
	if options.Push.ManualDelivery && options.Push.Sender == nil {
		return nil, errors.New("push.Config.ManualDelivery requires a Sender")
	}

	manager := &TaskManager{
		processor:   processor,
		client:      client,
		expiration:  options.ExpireTime,
		subscribers: make(map[string][]*taskSubscriber),
		executions:  make(map[string]*liveExecution),
		pushSender:  options.Push.Sender,
		options:     options,
	}
	manager.pushCtx, manager.pushCancel = context.WithCancel(context.Background())

	return manager, nil
}

// OnSendMessage handles the message/send request. It invokes the MessageProcessor
// and derives the result from the emitted events: the final task snapshot
// when task events were emitted, otherwise the last Message. The default is
// blocking (returnImmediately=false); with returnImmediately=true it returns
// on the immediate result while the execution continues in background.
func (m *TaskManager) OnSendMessage(
	ctx context.Context,
	request protocol.SendMessageParams,
) (*protocol.SendMessageResponse, error) {
	log.Debugf("RedisTaskManager: OnSendMessage for message %s", request.Message.MessageID)

	ex, err := m.prepareExecution(ctx, &request, false)
	if err != nil {
		return nil, err
	}

	historyLength := historyLengthFromConfig(request.Configuration)
	if !request.Configuration.IsBlocking() {
		// returnImmediately=true: answer with the immediate result (first
		// persisted task snapshot or first Message); execution continues in
		// background and results stay retrievable via GetTask/subscriptions.
		select {
		case out := <-ex.immediateResult:
			return m.buildSendResponse(out.task, out.message, historyLength)
		case <-ex.done:
			// The stream closed before any immediate result: same derivation as
			// blocking.
			return m.buildSendResponse(ex.finalTask, ex.lastMessage, historyLength)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	select {
	case <-ex.done:
		return m.buildSendResponse(ex.finalTask, ex.lastMessage, historyLength)
	case <-ctx.Done():
		// The request died first. The execution is detached: it keeps running
		// and its results stay retrievable via GetTask/resubscribe.
		return nil, ctx.Err()
	}
}

// OnSendMessageStream handles message/stream requests. Every event emitted
// by the MessageProcessor is persisted and then forwarded, in order, on the returned
// channel; the channel is closed when the round ends.
func (m *TaskManager) OnSendMessageStream(
	ctx context.Context,
	request protocol.SendMessageParams,
) (<-chan protocol.StreamResponse, error) {
	log.Debugf("RedisTaskManager: OnSendMessageStream for message %s", request.Message.MessageID)

	ex, err := m.prepareExecution(ctx, &request, true)
	if err != nil {
		return nil, err
	}
	// Tie the pipe to the request: the execution itself stays detached (a
	// client disconnect must not cancel the work), but once the stream's
	// consumer is gone the pipe has no reader — close it so a blocking-send
	// engine can never park on it and the server-side drain ends instead of
	// leaking.
	pipe := ex.pipe
	go func() {
		select {
		case <-ctx.Done():
			pipe.Close()
		case <-pipe.done:
		}
	}()
	return ex.pipe.Channel(), nil
}

// OnGetTask handles the tasks/get request.
func (m *TaskManager) OnGetTask(
	ctx context.Context,
	params protocol.TaskQueryParams,
) (*protocol.Task, error) {
	task, err := m.getTaskInternal(ctx, params.ID)
	if err != nil {
		return nil, err
	}

	// Fill message history per the v1.0 GetTaskRequest semantics:
	//   - historyLength unset -> no limit (full history)
	//   - historyLength == 0  -> no messages
	//   - historyLength > 0   -> the most recent N messages
	m.fillTaskHistory(ctx, task, params.HistoryLength)

	return task, nil
}

// fillTaskHistory shapes a response task's History per the v1.0 historyLength
// semantics: unset means the full conversation history, 0 (or negative) means
// none, N means the most recent N messages.
func (m *TaskManager) fillTaskHistory(ctx context.Context, task *protocol.Task, historyLength *int) {
	if task.ContextID == "" {
		return
	}
	length := unlimitedHistoryLength
	switch {
	case historyLength == nil:
	case *historyLength > 0:
		length = *historyLength
	default: // == 0 (or negative): no messages
		task.History = nil
		return
	}
	history, err := m.getConversationHistory(ctx, task.ContextID, length)
	if err != nil {
		log.Warnf("Failed to retrieve message history for task %s: %v", task.ID, err)
		// Continue without history rather than failing the whole request.
		return
	}
	task.History = history
}

// historyLengthFromConfig extracts the response historyLength, nil-safe.
func historyLengthFromConfig(config *protocol.SendMessageConfiguration) *int {
	if config == nil {
		return nil
	}
	return config.HistoryLength
}

// buildSendResponse derives the unary result: the task snapshot when a task
// exists (history shaped per the request's historyLength), otherwise the last
// message; an execution that produced neither is an MessageProcessor bug.
func (m *TaskManager) buildSendResponse(
	task *protocol.Task,
	message *protocol.Message,
	historyLength *int,
) (*protocol.SendMessageResponse, error) {
	if task != nil {
		m.fillTaskHistory(context.Background(), task, historyLength)
		return protocol.NewSendMessageResponseTask(task), nil
	}
	if message != nil {
		return protocol.NewSendMessageResponseMessage(message), nil
	}
	return nil, jsonrpc.ErrInternalError("processor produced no result")
}

// OnCancelTask handles the tasks/cancel request. For a live execution it
// cancels the MessageProcessor's context and returns the currently stored snapshot;
// the engine persists the terminal state when the MessageProcessor winds down (the
// MessageProcessor's own terminal event wins if it arrives). Without a live
// execution a non-terminal task is marked CANCELED directly, holding the
// execution slot with a sentinel so no continuation can start (and write)
// concurrently.
func (m *TaskManager) OnCancelTask(
	ctx context.Context,
	params protocol.TaskIDParams,
) (*protocol.Task, error) {
	for {
		live, sentinel := m.claimCancelSlot(params.ID)
		if live == nil {
			defer m.deregisterExecution(params.ID, sentinel)
			return m.cancelWithoutLiveRun(ctx, params)
		}

		// A stored terminal state is immutable even while the round is still
		// draining: canceling it must fail like the no-live path does.
		task, err := m.getTaskInternal(ctx, params.ID)
		if err != nil && !errors.Is(err, taskmanager.ErrTaskNotFoundSentinel) {
			// Storage error, not a missing task: canceling is irreversible, so
			// do not cancel a healthy run because the lookup blipped.
			return nil, err
		}
		if err == nil && isFinalState(task.Status.State) {
			return nil, taskmanager.ErrTaskNotCancelable(params.ID, task.Status.State)
		}
		// Flag before canceling so the engine's close rule sees the request even
		// when the MessageProcessor reacts by closing the channel immediately.
		live.requestCancel()

		if m.liveRun(params.ID) != live {
			// The run yielded (suspend) or finished while we were canceling, so
			// its close rule will not persist CANCELED on our behalf. Reassess:
			// the next pass either claims the free slot and persists CANCELED
			// itself, or cancels the continuation that took the slot.
			continue
		}

		if err != nil {
			// Execution registered but no task materialized yet (lazy creation).
			return nil, err
		}
		return task, nil
	}
}

// cancelWithoutLiveRun persists CANCELED for a task with no live execution:
// persist first, then broadcast, then close the task's subscribers. The caller
// holds the task's execution slot (sentinel), making this the task's single
// writer.
func (m *TaskManager) cancelWithoutLiveRun(
	ctx context.Context,
	params protocol.TaskIDParams,
) (*protocol.Task, error) {
	task, err := m.getTaskInternal(ctx, params.ID)
	if err != nil {
		return nil, err
	}

	// A task already in a terminal state cannot be canceled.
	if isFinalState(task.Status.State) {
		return nil, taskmanager.ErrTaskNotCancelable(params.ID, task.Status.State)
	}

	event := &protocol.TaskStatusUpdateEvent{
		TaskID:    task.ID,
		ContextID: task.ContextID,
		Status: protocol.TaskStatus{
			State:     protocol.TaskStateCanceled,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
		Final: true,
	}
	task.Status = event.Status
	// Persist with a background context (like every engine write): the CANCELED
	// state must land even if the cancel request's own context is already done.
	if err := m.storeTask(context.Background(), task); err != nil {
		log.Errorf("Error storing cancelled task %s: %v", params.ID, err)
		return nil, err
	}
	m.notifySubscribers(params.ID, protocol.NewStreamResponseStatusUpdate(event))
	m.cleanSubscribers(params.ID)

	return task, nil
}

// OnPushNotificationSet handles tasks/pushNotificationConfig/set requests.
func (m *TaskManager) OnPushNotificationSet(
	ctx context.Context,
	params protocol.TaskPushNotificationConfig,
) (*protocol.TaskPushNotificationConfig, error) {
	if m.pushSender == nil {
		return nil, taskmanager.ErrPushNotificationNotSupported()
	}
	if params.ID == "" {
		params.ID = params.TaskID
	}
	stored, err := m.storePushConfig(ctx, params)
	if err != nil {
		return nil, err
	}
	log.Debugf("RedisTaskManager: Push notification config %s set for task %s", stored.ID, stored.TaskID)
	return &stored, nil
}

// OnPushNotificationGet handles tasks/pushNotificationConfig/get requests.
func (m *TaskManager) OnPushNotificationGet(
	ctx context.Context,
	params protocol.GetTaskPushNotificationConfigParams,
) (*protocol.TaskPushNotificationConfig, error) {
	if m.pushSender == nil {
		return nil, taskmanager.ErrPushNotificationNotSupported()
	}
	if params.ID != "" {
		configBytes, err := m.client.HGet(ctx, pushNotificationPrefix+params.TaskID, params.ID).Bytes()
		if err == nil {
			var config protocol.TaskPushNotificationConfig
			if err := json.Unmarshal(configBytes, &config); err != nil {
				return nil, fmt.Errorf("failed to deserialize push notification config: %w", err)
			}
			return &config, nil
		} else if !errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("failed to read push notification config: %w", err)
		}
	} else {
		configs, err := m.readPushConfigs(ctx, params.TaskID)
		if err != nil {
			return nil, err
		}
		if len(configs) > 0 {
			config := configs[0]
			return &config, nil
		}
	}

	if _, err := m.getTaskInternal(ctx, params.TaskID); err != nil {
		return nil, err
	}
	return nil, taskmanager.ErrPushConfigNotFound(params.TaskID)
}

// unlimitedHistoryLength is used to request the full conversation history when
// a v1.0 request leaves historyLength unset (spec: unset means no limit).
const unlimitedHistoryLength = 1 << 30

// OnListTasks handles the v1.0 ListTasks request by scanning stored tasks,
// filtering, and applying offset-based pagination.
func (m *TaskManager) OnListTasks(
	ctx context.Context,
	params protocol.ListTasksParams,
) (*protocol.ListTasksResult, error) {
	afterTime, err := taskmanager.ParseListTasksStatusTimestampAfter(params.StatusTimestampAfter)
	if err != nil {
		return nil, err
	}

	// Scan all task keys and collect matching tasks.
	var filtered []*protocol.Task
	iter := m.client.Scan(ctx, 0, taskPrefix+"*", 0).Iterator()
	for iter.Next(ctx) {
		taskBytes, err := m.client.Get(ctx, iter.Val()).Bytes()
		if err != nil {
			continue // Key expired between SCAN and GET.
		}
		var task protocol.Task
		if err := json.Unmarshal(taskBytes, &task); err != nil {
			log.Errorf("RedisTaskManager: skip malformed task at %s: %v", iter.Val(), err)
			continue
		}
		if taskmanager.TaskMatchesListFilter(&task, params, afterTime) {
			filtered = append(filtered, &task)
		}
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan tasks: %w", err)
	}
	return taskmanager.PaginateTasks(filtered, params)
}

// OnPushNotificationList handles the v1.0 ListTaskPushNotificationConfigs request.
// It returns every push-notification config registered for the task.
func (m *TaskManager) OnPushNotificationList(
	ctx context.Context,
	params protocol.ListTaskPushNotificationConfigsParams,
) (*protocol.ListTaskPushNotificationConfigsResult, error) {
	if m.pushSender == nil {
		return nil, taskmanager.ErrPushNotificationNotSupported()
	}

	configs, err := m.readPushConfigs(ctx, params.TaskID)
	if err != nil {
		return nil, err
	}
	return &protocol.ListTaskPushNotificationConfigsResult{Configs: configs}, nil
}

// OnPushNotificationDelete handles the v1.0 DeleteTaskPushNotificationConfig request.
// Deleting a non-existent configuration is a no-op.
func (m *TaskManager) OnPushNotificationDelete(
	ctx context.Context,
	params protocol.DeleteTaskPushNotificationConfigParams,
) error {
	if m.pushSender == nil {
		return taskmanager.ErrPushNotificationNotSupported()
	}
	pushKey := pushNotificationPrefix + params.TaskID
	if params.ID != "" {
		if err := m.client.HDel(ctx, pushKey, params.ID).Err(); err != nil {
			return fmt.Errorf("failed to delete push notification config: %w", err)
		}
		log.Debugf("RedisTaskManager: Push notification config %s deleted for task %s", params.ID, params.TaskID)
		return nil
	}
	if err := m.client.Del(ctx, pushKey).Err(); err != nil {
		return fmt.Errorf("failed to delete push notification config: %w", err)
	}
	log.Debugf("RedisTaskManager: All push notification configs deleted for task %s", params.TaskID)
	return nil
}

// PushSender returns the Sender configured for push notifications, or nil when
// push is not enabled.
func (m *TaskManager) PushSender() push.Sender {
	return m.pushSender
}

// storePushConfig persists cfg as one field in the task's push-config hash.
// CreatedAt is server-authoritative: it is set on create and preserved when an
// existing config ID is updated. The transaction also refreshes the hash TTL.
func (m *TaskManager) storePushConfig(
	ctx context.Context, cfg protocol.TaskPushNotificationConfig,
) (protocol.TaskPushNotificationConfig, error) {
	if cfg.TaskID == "" {
		return protocol.TaskPushNotificationConfig{}, errors.New("push config store: taskId is required")
	}
	if cfg.ID == "" {
		cfg.ID = cfg.TaskID
	}
	pushKey := pushNotificationPrefix + cfg.TaskID
	for {
		stored := cfg
		err := m.client.Watch(ctx, func(tx *redis.Tx) error {
			existingBytes, err := tx.HGet(ctx, pushKey, cfg.ID).Bytes()
			switch {
			case err == nil:
				var existing protocol.TaskPushNotificationConfig
				if err := json.Unmarshal(existingBytes, &existing); err != nil {
					return fmt.Errorf("failed to deserialize push notification config: %w", err)
				}
				if existing.CreatedAt != "" {
					stored.CreatedAt = existing.CreatedAt
				} else {
					stored.CreatedAt = time.Now().UTC().Format(time.RFC3339)
				}
			case errors.Is(err, redis.Nil):
				stored.CreatedAt = time.Now().UTC().Format(time.RFC3339)
			default:
				return fmt.Errorf("failed to read push notification config: %w", err)
			}

			configBytes, err := json.Marshal(stored)
			if err != nil {
				return fmt.Errorf("failed to serialize push notification config: %w", err)
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.HSet(ctx, pushKey, cfg.ID, configBytes)
				pipe.Expire(ctx, pushKey, m.expiration)
				return nil
			})
			return err
		}, pushKey)
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		if err != nil {
			return protocol.TaskPushNotificationConfig{}, fmt.Errorf("failed to store push notification config: %w", err)
		}
		return stored, nil
	}
}

// readPushConfigs returns all push configs registered for taskID, ordered by
// CreatedAt then ID. A missing hash is represented by a non-nil empty slice.
func (m *TaskManager) readPushConfigs(
	ctx context.Context, taskID string,
) ([]protocol.TaskPushNotificationConfig, error) {
	entries, err := m.client.HGetAll(ctx, pushNotificationPrefix+taskID).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to read push notification configs: %w", err)
	}
	configs := make([]protocol.TaskPushNotificationConfig, 0, len(entries))
	for _, configJSON := range entries {
		var config protocol.TaskPushNotificationConfig
		if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
			return nil, fmt.Errorf("failed to deserialize push notification config: %w", err)
		}
		configs = append(configs, config)
	}
	sort.Slice(configs, func(i, j int) bool {
		if configs[i].CreatedAt != configs[j].CreatedAt {
			return configs[i].CreatedAt < configs[j].CreatedAt
		}
		return configs[i].ID < configs[j].ID
	})
	return configs, nil
}

// dispatchPush delivers event to the webhook registered for taskID. It is a
// no-op unless a Sender is configured and the event is worth pushing, and
// stays silent in manual delivery mode (the agent pushes on its own schedule).
// The config read and the webhook POST run in the background and best-effort:
// they must not block task-event processing, and a slow or failing webhook
// never fails the task.
func (m *TaskManager) dispatchPush(taskID string, event protocol.StreamResponse) {
	if m.pushSender == nil || m.options.Push.ManualDelivery || !pushWorthy(event) {
		return
	}
	// Reserve the delivery on pushWg under the lock that guards m.closed, which
	// Close sets before it waits: once shutdown has begun no new delivery can
	// race that Wait (dispatch is also reached from the OnCancelTask RPC path,
	// which engineWg does not track).
	m.cancelMu.Lock()
	if m.closed {
		m.cancelMu.Unlock()
		return
	}
	m.pushWg.Add(1)
	m.cancelMu.Unlock()

	go func() {
		defer m.pushWg.Done()
		configs, err := m.readPushConfigs(m.pushCtx, taskID)
		if err != nil {
			log.Warnf("RedisTaskManager: push dispatch: load config for task %s: %v", taskID, err)
			return
		}
		for _, cfg := range configs {
			if err := m.pushSender.SendPush(m.pushCtx, cfg, event); err != nil {
				log.Warnf("RedisTaskManager: push dispatch: send to %s for task %s: %v", cfg.URL, taskID, err)
			}
		}
	}()
}

// pushWorthy reports whether an event is worth delivering as a push
// notification. A status update carrying a message is always delivered;
// content-less working/submitted heartbeats are skipped to avoid webhook
// storms; terminal, input-required and auth-required transitions, and task,
// message and artifact events, are delivered.
//
// Keep in sync with the memory manager's pushWorthy: both encode one delivery
// policy that could later be lifted into the push package.
func pushWorthy(event protocol.StreamResponse) bool {
	if su := event.GetStatusUpdate(); su != nil {
		if su.Status.Message != nil {
			return true
		}
		switch su.Status.State {
		case protocol.TaskStateWorking, protocol.TaskStateSubmitted, protocol.TaskStateUnspecified:
			return false
		default:
			return true
		}
	}
	return true
}

// OnResubscribe handles tasks/resubscribe requests.
func (m *TaskManager) OnResubscribe(
	ctx context.Context,
	params protocol.TaskIDParams,
) (<-chan protocol.StreamResponse, error) {
	// The snapshot read happens OUTSIDE subMu: getTaskInternal is a network
	// round-trip and subMu sits on every broadcast's fan-out path — holding it
	// across a slow Redis call would stall every stream in the process.
	task, err := m.getTaskInternal(ctx, params.ID)
	if err != nil {
		return nil, err
	}

	// v1.0: a subscription is only valid for a non-terminal task; a task already
	// in a terminal state must be rejected with UnsupportedOperationError.
	if isFinalState(task.Status.State) {
		return nil, taskmanager.ErrUnsupportedOperation(
			fmt.Sprintf("subscribe to task %s in terminal state %s", params.ID, task.Status.State))
	}

	subscriber := newTaskSubscriber(
		params.ID,
		m.options.TaskSubscriberBufSize,
		m.options.TaskSubscriberBlockingSend,
	)

	// v1.0: the first stream event must be the current Task snapshot. The
	// subscriber is not registered yet, so nothing can precede this frame.
	if err := subscriber.Send(protocol.NewStreamResponseTask(task)); err != nil {
		subscriber.Close()
		return nil, err
	}
	m.subMu.Lock()
	m.subscribers[params.ID] = append(m.subscribers[params.ID], subscriber)
	m.subMu.Unlock()

	// The snapshot predates the registration, so an update persisted between
	// the two may be in neither. Re-read and, when anything changed, send a
	// second snapshot superseding whatever was missed: persist-before-broadcast
	// guarantees the re-read covers every event broadcast before registration.
	// (Boundary events may be delivered both in a snapshot and as themselves —
	// at-least-once, as before.)
	current, err := m.getTaskInternal(ctx, params.ID)
	if err == nil && taskChanged(task, current) {
		if isFinalState(current.Status.State) {
			// The task ended between the two reads: its terminal broadcast (and
			// cleanSubscribers) may have run before the registration, which
			// would leave this subscriber open forever. Reject like the
			// up-front terminal check does; nothing was delivered to the caller.
			m.cleanupFailedSubscribers(params.ID, []*taskSubscriber{subscriber})
			return nil, taskmanager.ErrUnsupportedOperation(
				fmt.Sprintf("subscribe to task %s in terminal state %s", params.ID, current.Status.State))
		}
		if err := subscriber.Send(protocol.NewStreamResponseTask(current)); err != nil {
			log.Warnf("RedisTaskManager: failed to send refreshed snapshot for task %s: %v", params.ID, err)
		}
	}

	// Tie the subscription to the request: when the client goes away the
	// subscriber is removed and closed, so the server-side drain ends and the
	// slot is not leaked (a suspended task may never reach a terminal state
	// that would clean it).
	go func() {
		select {
		case <-ctx.Done():
			m.cleanupFailedSubscribers(params.ID, []*taskSubscriber{subscriber})
		case <-subscriber.done:
		}
	}()

	return subscriber.Channel(), nil
}

// taskChanged reports whether two snapshots of the same task differ in what a
// stream conveys: status or artifacts. It deep-compares Artifacts rather than
// counting them — an append=true chunk merges into an existing artifact without
// growing the slice, so a length check would miss it and the registration-race
// compensation in OnResubscribe would drop that chunk from the stream.
// Resubscribe is a low-frequency control op, so the deep compare is cheap, and
// an occasional redundant snapshot is already documented as acceptable.
func taskChanged(before, after *protocol.Task) bool {
	return before.Status.State != after.Status.State ||
		before.Status.Timestamp != after.Status.Timestamp ||
		!reflect.DeepEqual(before.Artifacts, after.Artifacts)
}

// =============================================================================
// Internal helper methods
// =============================================================================

// processReplyMessage processes and stores the reply message.
func (m *TaskManager) processReplyMessage(ctxID *string, message *protocol.Message) {
	message.ContextID = ctxID
	message.Role = protocol.MessageRoleAgent
	if message.MessageID == "" {
		message.MessageID = protocol.GenerateMessageID()
	}
	if message.ContextID == nil || *message.ContextID == "" {
		contextID := protocol.GenerateContextID()
		message.ContextID = &contextID
	}

	m.storeMessage(context.Background(), *message)
}

// storeMessage stores a message in Redis and updates conversation history.
func (m *TaskManager) storeMessage(ctx context.Context, message protocol.Message) {
	// Store the message.
	msgKey := messagePrefix + message.MessageID
	msgBytes, err := json.Marshal(message)
	if err != nil {
		log.Errorf("Failed to serialize message %s: %v", message.MessageID, err)
		return
	}

	if err := m.client.Set(ctx, msgKey, msgBytes, m.expiration).Err(); err != nil {
		log.Errorf("Failed to store message %s in Redis: %v", message.MessageID, err)
		return
	}

	// If the message has a contextID, add it to conversation history.
	if message.ContextID != nil {
		contextID := *message.ContextID
		convKey := conversationPrefix + contextID

		// Idempotent by MessageID: the same message may reach storeMessage from
		// both the reply path and a status roll (or be re-emitted); indexing it
		// twice would duplicate it in history. Skip the append when it is already
		// present. (If LPos is unavailable the error path falls through to RPush,
		// preserving the previous append-only behavior.)
		if _, err := m.client.LPos(ctx, convKey, message.MessageID, redis.LPosArgs{}).Result(); err == nil {
			m.client.Expire(ctx, convKey, m.expiration)
			return
		}

		// Add message ID to conversation history using Redis list.
		if err := m.client.RPush(ctx, convKey, message.MessageID).Err(); err != nil {
			log.Errorf("Failed to add message %s to conversation %s: %v", message.MessageID, contextID, err)
			return
		}

		// Set expiration on the conversation list.
		m.client.Expire(ctx, convKey, m.expiration).Err()

		// Limit history length by trimming the list.
		if err := m.client.LTrim(ctx, convKey, -int64(m.options.MaxHistoryLength), -1).Err(); err != nil {
			log.Errorf("Failed to trim conversation %s: %v", contextID, err)
		}
	}
}

// getConversationHistory retrieves conversation history for a context.
func (m *TaskManager) getConversationHistory(
	ctx context.Context,
	contextID string,
	length int,
) ([]protocol.Message, error) {
	if contextID == "" {
		return nil, nil
	}

	convKey := conversationPrefix + contextID

	// Get the message count.
	count, err := m.client.LLen(ctx, convKey).Result()
	if err != nil {
		return nil, nil // No messages found.
	}

	// Calculate range for LRANGE (get the latest messages).
	start := int64(0)
	if count > int64(length) {
		start = count - int64(length)
	}

	// Get message IDs.
	messageIDs, err := m.client.LRange(ctx, convKey, start, count-1).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve message IDs: %w", err)
	}

	// Retrieve messages.
	messages := make([]protocol.Message, 0, len(messageIDs))
	for _, msgID := range messageIDs {
		msgKey := messagePrefix + msgID
		msgBytes, err := m.client.Get(ctx, msgKey).Bytes()
		if err != nil {
			log.Warnf("Message %s not found in Redis", msgID)
			continue // Skip missing messages.
		}

		var msg protocol.Message
		if err := json.Unmarshal(msgBytes, &msg); err != nil {
			log.Errorf("Failed to deserialize message %s: %v", msgID, err)
			continue // Skip invalid messages.
		}

		messages = append(messages, msg)
	}

	return messages, nil
}

// getTaskInternal retrieves a task from Redis. A missing key maps to
// ErrTaskNotFound; any other failure is a storage error and is reported as
// such — callers that take irreversible actions on not-found (cancel paths)
// must be able to tell the two apart.
func (m *TaskManager) getTaskInternal(ctx context.Context, taskID string) (*protocol.Task, error) {
	taskKey := taskPrefix + taskID
	taskBytes, err := m.client.Get(ctx, taskKey).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, taskmanager.ErrTaskNotFound(taskID)
		}
		return nil, fmt.Errorf("failed to load task %s: %w", taskID, err)
	}

	var task protocol.Task
	if err := json.Unmarshal(taskBytes, &task); err != nil {
		return nil, fmt.Errorf("failed to deserialize task: %w", err)
	}

	return &task, nil
}

// storeTask stores a task in Redis.
func (m *TaskManager) storeTask(ctx context.Context, task *protocol.Task) error {
	taskKey := taskPrefix + task.ID
	taskBytes, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("failed to serialize task: %w", err)
	}

	if err := m.client.Set(ctx, taskKey, taskBytes, m.expiration).Err(); err != nil {
		return fmt.Errorf("failed to store task: %w", err)
	}

	return nil
}

// isFinalState checks if a TaskState represents a terminal state.
func isFinalState(state protocol.TaskState) bool {
	return state == protocol.TaskStateCompleted ||
		state == protocol.TaskStateFailed ||
		state == protocol.TaskStateCanceled ||
		state == protocol.TaskStateRejected
}

// isSuspendedState reports whether the state suspends the task awaiting a
// follow-up message (§3.4): the round has stopped working, but the task lives
// on for a continuation.
func isSuspendedState(state protocol.TaskState) bool {
	return state == protocol.TaskStateInputRequired ||
		state == protocol.TaskStateAuthRequired
}

// registerExecution publishes the cancel handle of a starting run, or rejects
// the round: a task admits at most one live run — concurrent rounds would
// interleave their writes and close rules (and orphan cancel handles).
func (m *TaskManager) registerExecution(taskID string, live *liveExecution) error {
	m.cancelMu.Lock()
	defer m.cancelMu.Unlock()
	if m.closed {
		return jsonrpc.ErrInternalError("task manager is closed")
	}
	if _, exists := m.executions[taskID]; exists {
		return jsonrpc.ErrInvalidParams(
			fmt.Sprintf("task %s already has an active execution", taskID))
	}
	m.executions[taskID] = live
	// Counted under the registry lock so Close (which flips m.closed first)
	// can never begin waiting before a just-admitted run is counted.
	m.engineWg.Add(1)
	return nil
}

// releaseExecution aborts a registered run whose engine never started: it
// undoes registerExecution's registration and engine count.
func (m *TaskManager) releaseExecution(taskID string, live *liveExecution) {
	m.deregisterExecution(taskID, live)
	m.engineWg.Done()
}

// claimCancelSlot atomically returns the task's live run or — when there is
// none — claims the execution slot with a sentinel, so a no-live cancel's
// CANCELED write gets the same single-writer guarantee as a run: no
// continuation can register (and then write) concurrently with it.
func (m *TaskManager) claimCancelSlot(taskID string) (live *liveExecution, sentinel *liveExecution) {
	m.cancelMu.Lock()
	defer m.cancelMu.Unlock()
	if exec, ok := m.executions[taskID]; ok {
		return exec, nil
	}
	sentinel = &liveExecution{cancel: func() {}}
	m.executions[taskID] = sentinel
	return nil, sentinel
}

// liveRun returns the task's currently registered execution handle, if any.
func (m *TaskManager) liveRun(taskID string) *liveExecution {
	m.cancelMu.RLock()
	defer m.cancelMu.RUnlock()
	return m.executions[taskID]
}

// deregisterExecution removes the handle at engine end. It only removes its
// own entry so a follow-up round on the same task is never evicted.
func (m *TaskManager) deregisterExecution(taskID string, live *liveExecution) {
	m.cancelMu.Lock()
	defer m.cancelMu.Unlock()
	if current, ok := m.executions[taskID]; ok && current == live {
		delete(m.executions, taskID)
	}
}

// cleanSubscribers closes and removes all subscribers for a task. Subscribers
// are closed outside subMu so a stuck blocking send can never wedge the
// manager-wide lock.
func (m *TaskManager) cleanSubscribers(taskID string) {
	m.subMu.Lock()
	subs, exists := m.subscribers[taskID]
	if !exists {
		m.subMu.Unlock()
		return
	}
	delete(m.subscribers, taskID)
	m.subMu.Unlock()

	for _, sub := range subs {
		sub.Close()
	}
	log.Debugf("Cleaned subscribers for task %s", taskID)
}

// notifySubscribers notifies all subscribers of a task.
func (m *TaskManager) notifySubscribers(taskID string, event protocol.StreamResponse) {
	// Deliver push notifications independently of live SSE subscribers: reaching
	// clients that are not currently streaming is the whole point of push.
	m.dispatchPush(taskID, event)

	m.subMu.RLock()
	subs, exists := m.subscribers[taskID]
	if !exists || len(subs) == 0 {
		m.subMu.RUnlock()
		return
	}

	subsCopy := make([]*taskSubscriber, len(subs))
	copy(subsCopy, subs)
	m.subMu.RUnlock()

	log.Debugf("Notifying %d subscribers for task %s", len(subsCopy), taskID)

	var failedSubscribers []*taskSubscriber

	for _, sub := range subsCopy {
		if sub.Closed() {
			log.Debugf("Subscriber for task %s is already closed, marking for removal", taskID)
			failedSubscribers = append(failedSubscribers, sub)
			continue
		}

		err := sub.Send(event)
		if err != nil {
			log.Warnf("Failed to send event to subscriber for task %s: %v", taskID, err)
			failedSubscribers = append(failedSubscribers, sub)
		}
	}

	// Clean up failed or closed subscribers.
	if len(failedSubscribers) > 0 {
		m.cleanupFailedSubscribers(taskID, failedSubscribers)
	}
}

// cleanupFailedSubscribers removes failed or closed subscribers from the map
// and closes them. Removed subscribers are closed outside subMu so a stuck
// blocking send can never wedge the manager-wide lock; an evicted subscriber
// must be closed or its consumer's range loop never ends.
func (m *TaskManager) cleanupFailedSubscribers(taskID string, failedSubscribers []*taskSubscriber) {
	m.subMu.Lock()

	subs, exists := m.subscribers[taskID]
	if !exists {
		m.subMu.Unlock()
		return
	}

	// Filter out failed subscribers.
	filteredSubs := make([]*taskSubscriber, 0, len(subs))
	removedSubs := make([]*taskSubscriber, 0, len(failedSubscribers))

	for _, sub := range subs {
		shouldRemove := false
		for _, failedSub := range failedSubscribers {
			if sub == failedSub {
				shouldRemove = true
				removedSubs = append(removedSubs, sub)
				break
			}
		}
		if !shouldRemove {
			filteredSubs = append(filteredSubs, sub)
		}
	}

	if len(removedSubs) > 0 {
		m.subscribers[taskID] = filteredSubs
		log.Debugf("Removed %d failed subscribers for task %s", len(removedSubs), taskID)

		// If there are no subscribers left, delete the entire entry.
		if len(filteredSubs) == 0 {
			delete(m.subscribers, taskID)
		}
	}
	m.subMu.Unlock()

	for _, sub := range removedSubs {
		sub.Close()
	}
}

// Close tears the manager down: it refuses new runs, cancels every live
// MessageProcessor run, closes all streams, waits for the detached engines to wind
// down (their close-rule persists land while the client is still open), and
// only then closes the Redis client. A MessageProcessor is expected to close its
// channel once its ctx is canceled; Close blocks until every run has.
// It is safe to call Close multiple times.
func (m *TaskManager) Close() error {
	m.closeOnce.Do(func() {
		// Refuse new runs, request cancellation of every live one, and collect
		// their stream pipes: closing a pipe unblocks an engine parked on a
		// blocking pipe send.
		m.cancelMu.Lock()
		m.closed = true
		pipes := make([]*taskSubscriber, 0, len(m.executions))
		for _, live := range m.executions {
			live.requestCancel()
			if live.pipe != nil {
				pipes = append(pipes, live.pipe)
			}
		}
		m.cancelMu.Unlock()
		for _, pipe := range pipes {
			pipe.Close()
		}

		// Close all fan-out subscribers (outside subMu, so a stuck blocking
		// send can never wedge the manager-wide lock) — engine broadcasts must
		// not be able to block either.
		m.subMu.Lock()
		subsToClose := make([]*taskSubscriber, 0)
		for _, subscribers := range m.subscribers {
			subsToClose = append(subsToClose, subscribers...)
		}
		m.subscribers = make(map[string][]*taskSubscriber)
		m.subMu.Unlock()
		for _, sub := range subsToClose {
			sub.Close()
		}

		// Wait for the detached engines: their final persists (close-rule
		// CANCELED) must land while the Redis client is still usable.
		m.engineWg.Wait()

		// Cancel and drain in-flight push deliveries: cancelling first means a slow
		// webhook (or config read) cannot stall shutdown, and draining keeps the
		// Redis client open until every delivery goroutine has returned.
		m.pushCancel()
		m.pushWg.Wait()

		m.closeErr = m.client.Close()
	})
	return m.closeErr
}
