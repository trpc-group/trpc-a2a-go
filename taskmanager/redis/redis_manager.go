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

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"trpc.group/trpc-go/trpc-a2a-go/v2/internal/jsonrpc"
	"trpc.group/trpc-go/trpc-a2a-go/v2/internal/pushdispatch"
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
	// streamPrefix keys the per-task event stream used for cross-node resubscribe.
	streamPrefix = "stream:"
	// streamField is the single XADD field carrying the JSON-encoded StreamResponse.
	streamField = "e"
	// streamMaxLen caps each task's event stream (XADD MAXLEN ~).
	streamMaxLen = 10000
	// streamReadCount caps entries returned per XREAD.
	streamReadCount = 64
	// streamBlockTimeout bounds one XREAD BLOCK slice so tailers re-check context.
	streamBlockTimeout = 5 * time.Second

	// Default expiration time for Redis keys (1 hour).
	defaultExpiration = 1 * time.Hour

	// Default configuration values.
	defaultMaxHistoryLength         = 100
	defaultTaskSubscriberBufferSize = 1024
)

// appendConversationMessageScript appends a MessageID exactly once, trims the
// history window, and refreshes its TTL as one atomic Redis operation. It uses
// only commands available since Redis 2.6, avoiding LPOS's Redis 6.0.6 minimum.
var appendConversationMessageScript = redis.NewScript(`
local message_ids = redis.call('LRANGE', KEYS[1], 0, -1)
for _, message_id in ipairs(message_ids) do
    if message_id == ARGV[1] then
        redis.call('PEXPIRE', KEYS[1], ARGV[2])
        return 0
    end
end
redis.call('RPUSH', KEYS[1], ARGV[1])
redis.call('LTRIM', KEYS[1], -tonumber(ARGV[3]), -1)
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1
`)

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

	// pushEnabled controls config registration and capability advertisement.
	// Automatic delivery is a separate concern owned by pushDispatcher.
	pushEnabled bool
	// pushCtx cancels Redis config reads during shutdown.
	pushCtx    context.Context
	pushCancel context.CancelFunc
	// pushDispatcher owns the bounded, ordered automatic-delivery workers. It is
	// nil when push is disabled or the agent selected manual delivery.
	pushDispatcher *pushdispatch.Dispatcher

	// tailerWg counts cross-node resubscribe tailer goroutines so Close joins
	// them before closing the Redis client. baseCtx is canceled by Close to
	// unpark a tailer parked in a blocking XREAD.
	tailerWg   sync.WaitGroup
	baseCtx    context.Context
	baseCancel context.CancelFunc

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
	if options.Push.MaxConcurrentDeliveries < 0 || options.Push.DeliveryQueueSize < 0 {
		return nil, errors.New("push delivery concurrency and queue size cannot be negative")
	}

	manager := &TaskManager{
		processor:   processor,
		client:      client,
		expiration:  options.ExpireTime,
		subscribers: make(map[string][]*taskSubscriber),
		executions:  make(map[string]*liveExecution),
		pushEnabled: options.Push.Sender != nil || options.Push.ManualDelivery,
		options:     options,
	}
	manager.pushCtx, manager.pushCancel = context.WithCancel(context.Background())
	if options.Push.Sender != nil && !options.Push.ManualDelivery {
		manager.pushDispatcher = pushdispatch.New(
			manager.pushCtx, options.Push.Sender,
			options.Push.MaxConcurrentDeliveries, options.Push.DeliveryQueueSize,
			manager.isCurrentPushRegistration,
		)
	}
	manager.baseCtx, manager.baseCancel = context.WithCancel(context.Background())

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
		live, sentinel, yieldDone := m.claimCancelSlot(params.ID)
		if yieldDone != nil {
			select {
			case <-yieldDone:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
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
		// Linearize cancellation against a concurrent suspend handoff. If the
		// handoff won, wait for it and retry as a no-live cancel; otherwise the
		// close rule is guaranteed to observe cancelRequested.
		yieldDone, accepted := m.requestExecutionCancel(params.ID, live)
		if yieldDone != nil {
			select {
			case <-yieldDone:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if !accepted {
			continue
		}

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
	if !m.pushEnabled {
		return nil, taskmanager.ErrPushNotificationNotSupported()
	}
	if err := push.ValidateConfig(params); err != nil {
		return nil, jsonrpc.ErrInvalidParams(err.Error())
	}
	if _, err := m.getTaskInternal(ctx, params.TaskID); err != nil {
		return nil, err
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
	if !m.pushEnabled {
		return nil, taskmanager.ErrPushNotificationNotSupported()
	}
	if params.ID == "" {
		return nil, jsonrpc.ErrInvalidParams("push notification config ID is required")
	}
	if _, err := m.getTaskInternal(ctx, params.TaskID); err != nil {
		return nil, err
	}
	configBytes, err := m.client.HGet(ctx, pushNotificationPrefix+params.TaskID, params.ID).Bytes()
	if err == nil {
		registration, err := decodePushRegistration(configBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to deserialize push notification config: %w", err)
		}
		return &registration.Config, nil
	} else if !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("failed to read push notification config: %w", err)
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
	if !m.pushEnabled {
		return nil, taskmanager.ErrPushNotificationNotSupported()
	}
	if _, err := m.getTaskInternal(ctx, params.TaskID); err != nil {
		return nil, err
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
	if !m.pushEnabled {
		return taskmanager.ErrPushNotificationNotSupported()
	}
	if params.ID == "" {
		return jsonrpc.ErrInvalidParams("push notification config ID is required")
	}
	if _, err := m.getTaskInternal(ctx, params.TaskID); err != nil {
		return err
	}
	pushKey := pushNotificationPrefix + params.TaskID
	if err := m.client.HDel(ctx, pushKey, params.ID).Err(); err != nil {
		return fmt.Errorf("failed to delete push notification config: %w", err)
	}
	log.Debugf("RedisTaskManager: Push notification config %s deleted for task %s", params.ID, params.TaskID)
	return nil
}

// SupportsPushNotifications reports whether push registration and delivery are enabled.
func (m *TaskManager) SupportsPushNotifications() bool {
	return m.pushEnabled
}

// storePushConfig persists cfg as one field in the task's push-config hash.
func (m *TaskManager) storePushConfig(
	ctx context.Context, cfg protocol.TaskPushNotificationConfig,
) (protocol.TaskPushNotificationConfig, error) {
	if cfg.TaskID == "" {
		return protocol.TaskPushNotificationConfig{}, errors.New("push config store: taskId is required")
	}
	if cfg.ID == "" {
		cfg.ID = "push-" + uuid.New().String()
	}
	pushKey := pushNotificationPrefix + cfg.TaskID
	registration := pushdispatch.Registration{
		Config:     cfg,
		Generation: uuid.New().String(),
	}
	configBytes, err := json.Marshal(registration)
	if err != nil {
		return protocol.TaskPushNotificationConfig{}, fmt.Errorf("failed to serialize push notification config: %w", err)
	}
	if _, err := m.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.HSet(ctx, pushKey, cfg.ID, configBytes)
		pipe.Expire(ctx, pushKey, m.expiration)
		return nil
	}); err != nil {
		return protocol.TaskPushNotificationConfig{}, fmt.Errorf("failed to store push notification config: %w", err)
	}
	return cfg, nil
}

// decodePushRegistration decodes the current registration envelope while
// retaining compatibility with push configs written before generations were
// introduced.
func decodePushRegistration(data []byte) (pushdispatch.Registration, error) {
	var envelope struct {
		Config     json.RawMessage `json:"config"`
		Generation string          `json:"generation"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return pushdispatch.Registration{}, err
	}
	if envelope.Config != nil {
		if envelope.Generation == "" {
			return pushdispatch.Registration{}, errors.New("push registration generation is required")
		}
		var config protocol.TaskPushNotificationConfig
		if err := json.Unmarshal(envelope.Config, &config); err != nil {
			return pushdispatch.Registration{}, err
		}
		return pushdispatch.Registration{
			Config:     config,
			Generation: envelope.Generation,
		}, nil
	}

	var config protocol.TaskPushNotificationConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return pushdispatch.Registration{}, err
	}
	return pushdispatch.Registration{Config: config}, nil
}

// readPushConfigs returns all push configs registered for taskID, ordered by ID.
func (m *TaskManager) readPushConfigs(
	ctx context.Context, taskID string,
) ([]protocol.TaskPushNotificationConfig, error) {
	registrations, err := m.readPushRegistrations(ctx, taskID)
	if err != nil {
		return nil, err
	}
	configs := make([]protocol.TaskPushNotificationConfig, 0, len(registrations))
	for _, registration := range registrations {
		configs = append(configs, registration.Config)
	}
	return configs, nil
}

// readPushRegistrations returns all persisted push registration snapshots for
// taskID, ordered by config ID.
func (m *TaskManager) readPushRegistrations(
	ctx context.Context, taskID string,
) ([]pushdispatch.Registration, error) {
	entries, err := m.client.HGetAll(ctx, pushNotificationPrefix+taskID).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to read push notification configs: %w", err)
	}
	registrations := make([]pushdispatch.Registration, 0, len(entries))
	for _, configJSON := range entries {
		registration, err := decodePushRegistration([]byte(configJSON))
		if err != nil {
			return nil, fmt.Errorf("failed to deserialize push notification config: %w", err)
		}
		registrations = append(registrations, registration)
	}
	sort.Slice(registrations, func(i, j int) bool {
		return registrations[i].Config.ID < registrations[j].Config.ID
	})
	return registrations, nil
}

// isCurrentPushRegistration claims a queued registration for delivery only if
// Redis still contains the same generation. Missing or replaced registrations
// are skipped; read and decode failures fail closed.
func (m *TaskManager) isCurrentPushRegistration(
	ctx context.Context, queued pushdispatch.Registration,
) (bool, error) {
	cfg := queued.Config
	data, err := m.client.HGet(ctx, pushNotificationPrefix+cfg.TaskID, cfg.ID).Bytes()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to read current push notification config: %w", err)
	}
	current, err := decodePushRegistration(data)
	if err != nil {
		return false, fmt.Errorf("failed to deserialize current push notification config: %w", err)
	}
	return current.Generation == queued.Generation, nil
}

// dispatchPush delivers event to the webhooks registered for taskID. Configs
// are read at event time before the event enters the bounded queue, so a later
// registration change cannot retroactively change recipients. A full queue
// applies backpressure rather than silently dropping an event.
func (m *TaskManager) dispatchPush(taskID string, event protocol.StreamResponse) {
	if m.pushDispatcher == nil {
		return
	}
	m.cancelMu.RLock()
	closed := m.closed
	m.cancelMu.RUnlock()
	if closed {
		return
	}
	registrations, err := m.readPushRegistrations(m.pushCtx, taskID)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			log.Warnf("RedisTaskManager: push dispatch: load config for task %s: %v", taskID, err)
		}
		return
	}
	if err := m.pushDispatcher.Enqueue(registrations, event); err != nil && !errors.Is(err, pushdispatch.ErrClosed) {
		log.Warnf("RedisTaskManager: push dispatch: enqueue for task %s: %v", taskID, err)
	}
}

// OnResubscribe handles tasks/resubscribe requests.
func (m *TaskManager) OnResubscribe(
	ctx context.Context,
	params protocol.TaskIDParams,
) (<-chan protocol.StreamResponse, error) {
	if m.options.ResubscribeStreaming {
		return m.onResubscribeStreaming(ctx, params)
	}
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

// onResubscribeStreaming is the cross-node resubscribe path (opt-in via
// WithResubscribeStreaming). The first frame is the current snapshot; a tailer
// then replays the task's Redis event stream from the pre-snapshot cursor, so it
// works even when the task's live execution runs on another instance.
func (m *TaskManager) onResubscribeStreaming(
	ctx context.Context,
	params protocol.TaskIDParams,
) (<-chan protocol.StreamResponse, error) {
	// Capture the stream cursor BEFORE the snapshot so their union is gap-free:
	// persist-before-publish means any event at or before this cursor is already
	// in the snapshot, and any later one is replayed by the tailer.
	startID := m.streamStartID(ctx, params.ID)

	task, err := m.getTaskInternal(ctx, params.ID)
	if err != nil {
		return nil, err
	}
	// v1.0: subscribing to an already-terminal task is an error.
	if isFinalState(task.Status.State) {
		return nil, taskmanager.ErrUnsupportedOperation(
			fmt.Sprintf("subscribe to task %s in terminal state %s", params.ID, task.Status.State))
	}

	subscriber := newTaskSubscriber(
		params.ID,
		m.options.TaskSubscriberBufSize,
		m.options.TaskSubscriberBlockingSend,
	)
	// v1.0: the first stream event must be the current Task snapshot.
	if err := subscriber.Send(protocol.NewStreamResponseTask(task)); err != nil {
		subscriber.Close()
		return nil, err
	}

	// The subscriber is NOT registered in the local map: the Redis stream is its
	// only source, whose producer may be another instance. If the task went
	// terminal between the cursor and the snapshot, its terminal frame is after
	// the cursor, so the tailer delivers it and then closes.
	m.tailerWg.Add(1)
	go m.tailStream(ctx, params.ID, startID, subscriber)

	return subscriber.Channel(), nil
}

// streamStartID captures the current tail of a task's event stream. Returns "0"
// (from the beginning) for an empty/absent stream.
func (m *TaskManager) streamStartID(ctx context.Context, taskID string) string {
	msgs, err := m.client.XRevRangeN(ctx, streamPrefix+taskID, "+", "-", 1).Result()
	if err != nil || len(msgs) == 0 {
		return "0"
	}
	return msgs[0].ID
}

// tailStream feeds a resubscriber from the task's Redis event stream, reading
// strictly after startID. It closes the subscriber and returns on the terminal
// status frame, when the request ends, or when the manager closes.
func (m *TaskManager) tailStream(ctx context.Context, taskID, startID string, sub *taskSubscriber) {
	defer m.tailerWg.Done()
	defer sub.Close()

	// Cancel the blocking XREAD, and unblock a parked blocking-send via Close,
	// when the request ends or the manager closes.
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	tailerDone := make(chan struct{})
	defer close(tailerDone)
	go func() {
		select {
		case <-ctx.Done():
		case <-m.baseCtx.Done():
		case <-tailerDone:
			return
		}
		cancel()
		sub.Close()
	}()

	key := streamPrefix + taskID
	for {
		// A blocking XREAD is not interrupted mid-flight by context cancellation
		// (only Close, via the client, unblocks it), so re-check between slices:
		// on client disconnect this bounds teardown to one streamBlockTimeout.
		select {
		case <-readCtx.Done():
			return
		default:
		}
		res, err := m.client.XRead(readCtx, &redis.XReadArgs{
			Streams: []string{key, startID},
			Block:   streamBlockTimeout,
			Count:   streamReadCount,
		}).Result()
		if errors.Is(err, redis.Nil) {
			continue // BLOCK slice timed out with nothing new.
		}
		if err != nil {
			return // readCtx canceled, client closed, or a Redis error.
		}
		for _, stream := range res {
			for _, entry := range stream.Messages {
				startID = entry.ID
				raw, ok := entry.Values[streamField].(string)
				if !ok {
					continue
				}
				var event protocol.StreamResponse
				if err := json.Unmarshal([]byte(raw), &event); err != nil {
					log.Warnf("RedisTaskManager: discarding malformed stream entry for task %s: %v", taskID, err)
					continue
				}
				if err := sub.Send(event); err != nil {
					return // consumer gone.
				}
				// Terminate on a terminal STATUS frame only — an artifact's
				// lastChunk (also IsFinal on the artifact) ends an artifact, not
				// the task.
				if su := event.GetStatusUpdate(); su != nil && su.IsFinal() {
					return
				}
			}
		}
	}
}

// publishStream mirrors an already-persisted, already-locally-broadcast event
// onto the task's Redis stream for cross-instance resubscribers. No-op unless
// ResubscribeStreaming is enabled. It uses a background context (like storeTask)
// so a request-scoped cancellation cannot drop the mirror after the event is
// durable, and bounds the stream with MAXLEN + the same TTL as the task key.
func (m *TaskManager) publishStream(taskID string, event protocol.StreamResponse) {
	if !m.options.ResubscribeStreaming {
		return
	}
	payload, err := json.Marshal(event)
	if err != nil {
		log.Errorf("RedisTaskManager: failed to marshal stream event for task %s: %v", taskID, err)
		return
	}
	key := streamPrefix + taskID
	ctx := context.Background()
	pipe := m.client.Pipeline()
	pipe.XAdd(ctx, &redis.XAddArgs{
		Stream: key,
		MaxLen: streamMaxLen,
		Approx: true,
		Values: map[string]interface{}{streamField: payload},
	})
	pipe.Expire(ctx, key, m.expiration)
	if _, err := pipe.Exec(ctx); err != nil {
		log.Errorf("RedisTaskManager: failed to publish stream event for task %s: %v", taskID, err)
	}
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

		// The same MessageID may reach this path concurrently from a reply and a
		// superseded status. Keep the membership check, append, trim, and TTL
		// refresh atomic so the conversation index stays idempotent on every
		// supported Redis version.
		if _, err := appendConversationMessageScript.Run(
			ctx,
			m.client,
			[]string{convKey},
			message.MessageID,
			m.expiration.Milliseconds(),
			m.options.MaxHistoryLength,
		).Result(); err != nil {
			log.Errorf("Failed to index message %s in conversation %s: %v", message.MessageID, contextID, err)
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

	_, err = m.client.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Set(ctx, taskKey, taskBytes, m.expiration)
		// A task continuation refreshes its registered push configs as well.
		// Expire on a missing hash is intentionally a no-op.
		pipe.Expire(ctx, pushNotificationPrefix+task.ID, m.expiration)
		return nil
	})
	if err != nil {
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

// registerExecution publishes the cancel handle of a starting run. A task
// admits at most one live run; a continuation waits through the previous
// round's short suspend handoff, while any other concurrent round is rejected.
func (m *TaskManager) registerExecution(
	ctx context.Context,
	taskID string,
	live *liveExecution,
) error {
	for {
		m.cancelMu.Lock()
		if m.closed {
			m.cancelMu.Unlock()
			return jsonrpc.ErrInternalError("task manager is closed")
		}
		current, exists := m.executions[taskID]
		if !exists {
			m.executions[taskID] = live
			// Counted under the registry lock so Close (which flips m.closed first)
			// can never begin waiting before a just-admitted run is counted.
			m.engineWg.Add(1)
			m.cancelMu.Unlock()
			return nil
		}
		yieldDone := current.yieldDone
		m.cancelMu.Unlock()
		if yieldDone == nil {
			return jsonrpc.ErrInvalidParams(
				fmt.Sprintf("task %s already has an active execution", taskID))
		}
		select {
		case <-yieldDone:
			// Retry after the previous round completes its suspend handoff.
		case <-ctx.Done():
			return ctx.Err()
		}
	}
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
func (m *TaskManager) claimCancelSlot(
	taskID string,
) (live *liveExecution, sentinel *liveExecution, yieldDone <-chan struct{}) {
	m.cancelMu.Lock()
	defer m.cancelMu.Unlock()
	if exec, ok := m.executions[taskID]; ok {
		if exec.yieldDone != nil {
			return nil, nil, exec.yieldDone
		}
		return exec, nil, nil
	}
	sentinel = &liveExecution{cancel: func() {}}
	m.executions[taskID] = sentinel
	return nil, sentinel, nil
}

// requestExecutionCancel linearizes an accepted cancellation against a
// suspend handoff. It returns the handoff channel when yield won, or accepted
// after publishing cancelRequested under the registry lock.
func (m *TaskManager) requestExecutionCancel(
	taskID string,
	live *liveExecution,
) (yieldDone <-chan struct{}, accepted bool) {
	m.cancelMu.Lock()
	if m.executions[taskID] != live {
		m.cancelMu.Unlock()
		return nil, false
	}
	if live.yieldDone != nil {
		yieldDone = live.yieldDone
		m.cancelMu.Unlock()
		return yieldDone, false
	}
	live.cancelRequested.Store(true)
	m.cancelMu.Unlock()
	live.cancel()
	return nil, true
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
		if live.yieldDone != nil {
			close(live.yieldDone)
			live.yieldDone = nil
		}
	}
}

// beginExecutionYield turns an active slot into a short handoff barrier. The
// slot remains owned by live until its suspend frame has reached every local
// observer, but continuations wait for the handoff instead of failing.
func (m *TaskManager) beginExecutionYield(taskID string, live *liveExecution) bool {
	m.cancelMu.Lock()
	defer m.cancelMu.Unlock()
	if m.executions[taskID] == live && live.yieldDone == nil && !live.cancelRequested.Load() {
		live.yieldDone = make(chan struct{})
		return true
	}
	return false
}

// abortExecutionYield restores an active slot when committing the suspended
// state fails. Waiters wake and re-evaluate it as an ordinary active run.
func (m *TaskManager) abortExecutionYield(taskID string, live *liveExecution) {
	m.cancelMu.Lock()
	defer m.cancelMu.Unlock()
	if m.executions[taskID] == live && live.yieldDone != nil {
		close(live.yieldDone)
		live.yieldDone = nil
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
	// Mirror onto the task's Redis stream first so a resubscriber on another
	// instance sees the same events. This is the sole fan-out convergence point
	// (both broadcast and cancelWithoutLiveRun reach here), so no producer path
	// is missed. No-op unless ResubscribeStreaming is enabled.
	m.publishStream(taskID, event)

	// Deliver push notifications independently of live SSE subscribers: reaching
	// clients that are not currently streaming is the whole point of push.
	m.dispatchPush(taskID, event)
	m.notifyLiveSubscribers(taskID, event)
}

// notifyLiveSubscribers fans out to process-local task subscribers without
// dispatching push a second time.
func (m *TaskManager) notifyLiveSubscribers(taskID string, event protocol.StreamResponse) {
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
		// Cancel config reads, blocked enqueues, and webhook calls before waiting
		// for engines that may currently be dispatching an event.
		m.pushCancel()
		m.pushDispatcher.Close()
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

		// Stop cross-node resubscribe tailers. A blocking XREAD is not aborted by
		// context alone, so closing the client is what unparks it; baseCancel
		// stops any tailer between slices from re-reading. Join after, so no
		// tailer goroutine outlives Close.
		m.baseCancel()
		m.closeErr = m.client.Close()
		m.tailerWg.Wait()
	})
	return m.closeErr
}
