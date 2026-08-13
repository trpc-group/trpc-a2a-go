// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package redis provides a Redis-based implementation of the A2A TaskManager interface.
package redis

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

const (
	// Key prefixes for Redis storage.
	messagePrefix           = "msg:"
	conversationPrefix      = "conv:"
	taskPrefix              = "task:"
	taskIndexPrefix         = "taskidx:"
	pushNotificationPrefix  = "push:"
	tenantNamespacePrefix   = "tenant:"
	defaultTenantKeySegment = "~default"
	ownerNamespacePrefix    = "owner:"

	// Default expiration time for Redis keys (1 hour).
	defaultExpiration = 1 * time.Hour

	// Default configuration values.
	defaultMaxHistoryLength         = 100
	defaultTaskSubscriberBufferSize = 1024
	taskEventReadIdleInitialDelay   = 100 * time.Millisecond
	taskEventReadIdleMaxDelay       = time.Second
)

// scopedID is the process-local identity of tenant-and-owner-scoped runtime state.
type scopedID struct {
	tenant string
	owner  string
	id     string
}

func newScopedID(tenant, owner, id string) scopedID {
	return scopedID{tenant: tenant, owner: owner, id: id}
}

// tenantKeyPrefix places every Redis key in a tenant namespace. An empty
// protocol tenant selects an internal sentinel outside the URL-safe base64
// alphabet; explicit tenant values remain encoded as unambiguous key segments.
func tenantKeyPrefix(tenant string) string {
	if tenant == "" {
		return tenantNamespacePrefix + defaultTenantKeySegment + ":"
	}
	return tenantNamespacePrefix + base64.RawURLEncoding.EncodeToString([]byte(tenant)) + ":"
}

// ownerKeyPrefix extends the tenant namespace with an authorization owner.
// Empty owner omits the optional owner segment.
func ownerKeyPrefix(tenant, owner string) string {
	prefix := tenantKeyPrefix(tenant)
	if owner == "" {
		return prefix
	}
	return prefix + ownerNamespacePrefix + base64.RawURLEncoding.EncodeToString([]byte(owner)) + ":"
}

func messageKey(tenant, owner, messageID string) string {
	return ownerKeyPrefix(tenant, owner) + messagePrefix + messageID
}

func conversationKey(tenant, owner, contextID string) string {
	return ownerKeyPrefix(tenant, owner) + conversationPrefix + contextID
}

func taskKey(tenant, owner, taskID string) string {
	return ownerKeyPrefix(tenant, owner) + taskPrefix + taskID
}

func taskIndexKey(tenant, owner string) string {
	return ownerKeyPrefix(tenant, owner) + taskIndexPrefix + "all"
}

func pushNotificationKey(tenant, owner, taskID string) string {
	return ownerKeyPrefix(tenant, owner) + pushNotificationPrefix + taskID
}

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
// lifecycle, persistence, and resumable Redis event journal.
// It is safe for concurrent use.
type TaskManager struct {
	// processor is the user-provided agent logic.
	processor taskmanager.MessageProcessor
	// client is the Redis client.
	client redis.UniversalClient
	// expiration is the time after which Redis keys expire.
	expiration time.Duration
	// eventTransport persists the per-task event journal used by SubscribeToTask.
	eventTransport taskEventTransport
	// cancelMu is a mutex for the executions map and the closed flag.
	cancelMu sync.RWMutex
	// executions maps tenant-and-owner-scoped task IDs to live execution handles so OnCancelTask can
	// cancel the MessageProcessor's context.
	executions map[scopedID]*liveExecution
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
	pushDispatcher *push.Dispatcher

	// tailerWg counts Stream subscription tailer goroutines so Close joins
	// them before closing the Redis client. baseCtx is canceled by Close to
	// unpark a tailer parked in a blocking transport read.
	tailerWg   sync.WaitGroup
	baseCtx    context.Context
	baseCancel context.CancelFunc

	// options
	options *TaskManagerOptions
}

func (m *TaskManager) resolveOwner(ctx context.Context) (string, error) {
	if m.options.OwnerResolver == nil {
		return "", nil
	}
	owner, err := m.options.OwnerResolver(ctx)
	if err != nil {
		return "", taskmanager.ErrInternalError(fmt.Sprintf("resolve task owner: %v", err))
	}
	if owner == "" {
		return "", taskmanager.ErrInternalError("task owner resolver returned an empty owner")
	}
	return owner, nil
}

// NewTaskManager creates a new Redis-based TaskManager with the provided options.
// Redis 5.0 or newer is required for the per-task event Streams.
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
	expiration := options.ExpireTime
	if expiration < time.Millisecond {
		// go-redis applies the same lower bound for SET expiration. Keep Lua PX
		// arguments valid and preserve the pre-streaming expiration semantics.
		expiration = time.Millisecond
	}

	manager := &TaskManager{
		processor:      processor,
		client:         client,
		expiration:     expiration,
		eventTransport: newRedisTaskEventTransport(client, expiration),
		executions:     make(map[scopedID]*liveExecution),
		pushEnabled:    options.Push.Sender != nil || options.Push.ManualDelivery,
		options:        options,
	}
	manager.pushCtx, manager.pushCancel = context.WithCancel(context.Background())
	if options.Push.Sender != nil && !options.Push.ManualDelivery {
		manager.pushDispatcher = push.NewDispatcher(
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
			return m.buildSendResponse(request.Tenant, ex.owner, out.task, out.message, historyLength)
		case err := <-ex.runFailed:
			return nil, err
		case <-ex.done:
			// The stream closed before any immediate result: same derivation as
			// blocking.
			if ex.runErr != nil {
				return nil, ex.runErr
			}
			return m.buildSendResponse(request.Tenant, ex.owner, ex.finalTask, ex.lastMessage, historyLength)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	select {
	case err := <-ex.runFailed:
		return nil, err
	case <-ex.done:
		if ex.runErr != nil {
			return nil, ex.runErr
		}
		return m.buildSendResponse(request.Tenant, ex.owner, ex.finalTask, ex.lastMessage, historyLength)
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
	owner, err := m.resolveOwner(ctx)
	if err != nil {
		return nil, err
	}
	task, err := m.getTaskInternal(ctx, params.Tenant, owner, params.ID)
	if err != nil {
		return nil, err
	}

	// Fill message history per the v1.0 GetTaskRequest semantics:
	//   - historyLength unset -> no limit (full history)
	//   - historyLength == 0  -> no messages
	//   - historyLength > 0   -> the most recent N messages
	m.fillTaskHistory(ctx, params.Tenant, owner, task, params.HistoryLength)

	return task, nil
}

// fillTaskHistory shapes a response task's History per the v1.0 historyLength
// semantics: unset means the full conversation history, 0 (or negative) means
// none, N means the most recent N messages.
func (m *TaskManager) fillTaskHistory(
	ctx context.Context,
	tenant string,
	owner string,
	task *protocol.Task,
	historyLength *int,
) {
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
	history, err := m.getConversationHistory(ctx, tenant, owner, task.ContextID, length)
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
	tenant string,
	owner string,
	task *protocol.Task,
	message *protocol.Message,
	historyLength *int,
) (*protocol.SendMessageResponse, error) {
	if task != nil {
		m.fillTaskHistory(context.Background(), tenant, owner, task, historyLength)
		return protocol.NewSendMessageResponseTask(task), nil
	}
	if message != nil {
		return protocol.NewSendMessageResponseMessage(message), nil
	}
	return nil, taskmanager.ErrInternalError("processor produced no result")
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
	owner, err := m.resolveOwner(ctx)
	if err != nil {
		return nil, err
	}
	for {
		live, sentinel, yieldDone := m.claimCancelSlot(params.Tenant, owner, params.ID)
		if yieldDone != nil {
			select {
			case <-yieldDone:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if live == nil {
			defer m.deregisterExecution(params.Tenant, owner, params.ID, sentinel)
			return m.cancelWithoutLiveRun(ctx, owner, params)
		}

		// A stored terminal state is immutable even while the round is still
		// draining: canceling it must fail like the no-live path does.
		task, err := m.getTaskInternal(ctx, params.Tenant, owner, params.ID)
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
		yieldDone, accepted := m.requestExecutionCancel(params.Tenant, owner, params.ID, live)
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

		if m.liveRun(params.Tenant, owner, params.ID) != live {
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

// cancelWithoutLiveRun persists CANCELED for a task with no live execution and
// dispatches push after the event journal commit. The caller holds the task's
// execution slot (sentinel), making this the task's single writer.
func (m *TaskManager) cancelWithoutLiveRun(
	ctx context.Context,
	owner string,
	params protocol.TaskIDParams,
) (*protocol.Task, error) {
	task, err := m.getTaskInternal(ctx, params.Tenant, owner, params.ID)
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
	response := protocol.NewStreamResponseStatusUpdate(event)
	// Persist with a background context (like every engine write): the CANCELED
	// state must land even if the cancel request's own context is already done.
	if err := m.commitTaskEvent(context.Background(), params.Tenant, owner, task, response, false); err != nil {
		log.Errorf("Error storing cancelled task %s: %v", params.ID, err)
		return nil, err
	}
	m.dispatchPush(params.Tenant, owner, params.ID, response)

	return task, nil
}

// OnPushNotificationSet handles tasks/pushNotificationConfig/set requests.
func (m *TaskManager) OnPushNotificationSet(
	ctx context.Context,
	params protocol.TaskPushNotificationConfig,
) (*protocol.TaskPushNotificationConfig, error) {
	owner, err := m.resolveOwner(ctx)
	if err != nil {
		return nil, err
	}
	if !m.pushEnabled {
		return nil, taskmanager.ErrPushNotificationNotSupported()
	}
	if err := push.ValidateConfig(params); err != nil {
		return nil, taskmanager.ErrInvalidParams(err.Error())
	}
	if _, err := m.getTaskInternal(ctx, params.Tenant, owner, params.TaskID); err != nil {
		return nil, err
	}
	stored, err := m.storePushConfig(ctx, owner, params)
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
	owner, err := m.resolveOwner(ctx)
	if err != nil {
		return nil, err
	}
	if !m.pushEnabled {
		return nil, taskmanager.ErrPushNotificationNotSupported()
	}
	if params.ID == "" {
		return nil, taskmanager.ErrInvalidParams("push notification config ID is required")
	}
	if _, err := m.getTaskInternal(ctx, params.Tenant, owner, params.TaskID); err != nil {
		return nil, err
	}
	configBytes, err := m.client.HGet(ctx, pushNotificationKey(params.Tenant, owner, params.TaskID), params.ID).Bytes()
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

// OnListTasks handles the v1.0 ListTasks request from the tenant-and-owner-local task
// index, filtering and applying offset-based pagination without a keyspace SCAN.
func (m *TaskManager) OnListTasks(
	ctx context.Context,
	params protocol.ListTasksParams,
) (*protocol.ListTasksResult, error) {
	owner, err := m.resolveOwner(ctx)
	if err != nil {
		return nil, err
	}
	afterTime, err := taskmanager.ParseListTasksStatusTimestampAfter(params.StatusTimestampAfter)
	if err != nil {
		return nil, err
	}

	indexKey := taskIndexKey(params.Tenant, owner)
	nowMillis := time.Now().UnixMilli()
	if err := m.client.ZRemRangeByScore(ctx, indexKey, "-inf", strconv.FormatInt(nowMillis, 10)).Err(); err != nil {
		return nil, fmt.Errorf("failed to prune expired task index entries: %w", err)
	}
	taskIDs, err := m.client.ZRangeByScore(ctx, indexKey, &redis.ZRangeBy{
		Min: "(" + strconv.FormatInt(nowMillis, 10),
		Max: "+inf",
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to load task index: %w", err)
	}

	// Pipeline the task loads. ClusterClient splits the pipeline by node, so
	// task records may stay distributed even though the scoped index is one key.
	loads := make([]*redis.StringCmd, len(taskIDs))
	_, pipelineErr := m.client.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for i, taskID := range taskIDs {
			loads[i] = pipe.Get(ctx, taskKey(params.Tenant, owner, taskID))
		}
		return nil
	})
	if pipelineErr != nil && !errors.Is(pipelineErr, redis.Nil) {
		return nil, fmt.Errorf("failed to load indexed tasks: %w", pipelineErr)
	}

	var filtered []*protocol.Task
	for i, load := range loads {
		taskBytes, err := load.Bytes()
		if errors.Is(err, redis.Nil) {
			// The index is written before the task. A failed or still-in-flight
			// task write can therefore leave a temporary member; its score expires
			// automatically. Do not ZREM here: the same task ID may have been
			// concurrently recreated after this GET observed a miss.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("failed to load task %s: %w", taskIDs[i], err)
		}
		var task protocol.Task
		if err := json.Unmarshal(taskBytes, &task); err != nil {
			log.Errorf("RedisTaskManager: skip malformed indexed task %s: %v", taskIDs[i], err)
			continue
		}
		if taskmanager.TaskMatchesListFilter(&task, params, afterTime) {
			filtered = append(filtered, &task)
		}
	}
	return taskmanager.PaginateTasks(filtered, params)
}

// OnPushNotificationList handles the v1.0 ListTaskPushNotificationConfigs request.
// It returns every push-notification config registered for the task.
func (m *TaskManager) OnPushNotificationList(
	ctx context.Context,
	params protocol.ListTaskPushNotificationConfigsParams,
) (*protocol.ListTaskPushNotificationConfigsResult, error) {
	owner, err := m.resolveOwner(ctx)
	if err != nil {
		return nil, err
	}
	if !m.pushEnabled {
		return nil, taskmanager.ErrPushNotificationNotSupported()
	}
	if _, err := m.getTaskInternal(ctx, params.Tenant, owner, params.TaskID); err != nil {
		return nil, err
	}

	configs, err := m.readPushConfigs(ctx, params.Tenant, owner, params.TaskID)
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
	owner, err := m.resolveOwner(ctx)
	if err != nil {
		return err
	}
	if !m.pushEnabled {
		return taskmanager.ErrPushNotificationNotSupported()
	}
	if params.ID == "" {
		return taskmanager.ErrInvalidParams("push notification config ID is required")
	}
	if _, err := m.getTaskInternal(ctx, params.Tenant, owner, params.TaskID); err != nil {
		return err
	}
	pushKey := pushNotificationKey(params.Tenant, owner, params.TaskID)
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
	ctx context.Context, owner string, cfg protocol.TaskPushNotificationConfig,
) (protocol.TaskPushNotificationConfig, error) {
	if cfg.TaskID == "" {
		return protocol.TaskPushNotificationConfig{}, errors.New("push config store: taskId is required")
	}
	if cfg.ID == "" {
		cfg.ID = "push-" + uuid.New().String()
	}
	pushKey := pushNotificationKey(cfg.Tenant, owner, cfg.TaskID)
	registration := push.Registration{
		Config:     cfg,
		Generation: uuid.New().String(),
		Owner:      owner,
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
func decodePushRegistration(data []byte) (push.Registration, error) {
	var envelope struct {
		Config     json.RawMessage `json:"config"`
		Generation string          `json:"generation"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return push.Registration{}, err
	}
	if envelope.Config != nil {
		if envelope.Generation == "" {
			return push.Registration{}, errors.New("push registration generation is required")
		}
		var config protocol.TaskPushNotificationConfig
		if err := json.Unmarshal(envelope.Config, &config); err != nil {
			return push.Registration{}, err
		}
		return push.Registration{
			Config:     config,
			Generation: envelope.Generation,
		}, nil
	}

	var config protocol.TaskPushNotificationConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return push.Registration{}, err
	}
	return push.Registration{Config: config}, nil
}

// readPushConfigs returns all push configs registered for taskID, ordered by ID.
func (m *TaskManager) readPushConfigs(
	ctx context.Context, tenant, owner, taskID string,
) ([]protocol.TaskPushNotificationConfig, error) {
	registrations, err := m.readPushRegistrations(ctx, tenant, owner, taskID)
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
	ctx context.Context, tenant, owner, taskID string,
) ([]push.Registration, error) {
	entries, err := m.client.HGetAll(ctx, pushNotificationKey(tenant, owner, taskID)).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to read push notification configs: %w", err)
	}
	registrations := make([]push.Registration, 0, len(entries))
	for _, configJSON := range entries {
		registration, err := decodePushRegistration([]byte(configJSON))
		if err != nil {
			return nil, fmt.Errorf("failed to deserialize push notification config: %w", err)
		}
		registration.Owner = owner
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
	ctx context.Context, queued push.Registration,
) (bool, error) {
	cfg := queued.Config
	data, err := m.client.HGet(ctx, pushNotificationKey(cfg.Tenant, queued.Owner, cfg.TaskID), cfg.ID).Bytes()
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
func (m *TaskManager) dispatchPush(tenant, owner, taskID string, event protocol.StreamResponse) {
	if m.pushDispatcher == nil {
		return
	}
	m.cancelMu.RLock()
	closed := m.closed
	m.cancelMu.RUnlock()
	if closed {
		return
	}
	registrations, err := m.readPushRegistrations(m.pushCtx, tenant, owner, taskID)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			log.Warnf("RedisTaskManager: push dispatch: load config for task %s: %v", taskID, err)
		}
		return
	}
	if err := m.pushDispatcher.Enqueue(registrations, event); err != nil && !errors.Is(err, push.ErrDispatcherClosed) {
		log.Warnf("RedisTaskManager: push dispatch: enqueue for task %s: %v", taskID, err)
	}
}

// OnResubscribe handles tasks/resubscribe requests.
func (m *TaskManager) OnResubscribe(
	ctx context.Context,
	params protocol.TaskIDParams,
) (<-chan protocol.StreamResponse, error) {
	owner, err := m.resolveOwner(ctx)
	if err != nil {
		return nil, err
	}
	// The transport loads the Task and event cursor at one atomic boundary. A
	// concurrent task-event commit is therefore either reflected by both values
	// or by neither: the snapshot and subsequent read contain no gap or overlap.
	task, startID, err := m.eventTransport.LoadTaskAndCursor(ctx, params.Tenant, owner, params.ID)
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
		1,
		true,
	)
	// v1.0: the first stream event must be the current Task snapshot.
	if err := subscriber.Send(protocol.NewStreamResponseTask(task)); err != nil {
		subscriber.Close()
		return nil, err
	}

	// The event journal is the subscription's only source, whose producer may be
	// another instance. Admit the tailer under cancelMu so Close cannot wait
	// before this goroutine is counted.
	m.cancelMu.Lock()
	if m.closed {
		m.cancelMu.Unlock()
		subscriber.Close()
		return nil, taskmanager.ErrInternalError("task manager is closed")
	}
	m.tailerWg.Add(1)
	m.cancelMu.Unlock()
	go m.tailTaskEvents(ctx, params.Tenant, owner, params.ID, startID, subscriber)

	return subscriber.Channel(), nil
}

// tailTaskEvents feeds a resubscriber from the configured event transport,
// reading strictly after startID. It closes the subscriber and returns on the
// terminal status frame, when the request ends, or when the manager closes.
//
//nolint:gocyclo // Retry, backpressure, cancellation, and terminal handling stay in one ordered tailer loop.
func (m *TaskManager) tailTaskEvents(
	ctx context.Context,
	tenant, owner, taskID, startID string,
	sub *taskSubscriber,
) {
	defer m.tailerWg.Done()
	defer sub.Close()

	// Cancel a blocking transport read, and unblock a parked blocking-send via
	// Close, when the request ends or the manager closes.
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

	idleDelay := taskEventReadIdleInitialDelay
	jitterUnit := time.Now().UnixNano() % 1000
	for i := 0; i < len(taskID); i++ {
		jitterUnit = (jitterUnit*33 + int64(taskID[i])) % 1000
	}
	waitForRetry := func() bool {
		jitterRange := idleDelay / 4
		waitDelay := idleDelay
		if jitterRange > 0 {
			waitDelay += jitterRange * time.Duration(jitterUnit) / 1000
		}
		timer := time.NewTimer(waitDelay)
		select {
		case <-readCtx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
		if idleDelay < taskEventReadIdleMaxDelay {
			idleDelay *= 2
			if idleDelay > taskEventReadIdleMaxDelay {
				idleDelay = taskEventReadIdleMaxDelay
			}
		}
		return true
	}
	for {
		// ReadAfter implementations may use bounded blocking reads, so re-check
		// cancellation between batches.
		select {
		case <-readCtx.Done():
			return
		default:
		}
		previousID := startID
		events, nextID, err := m.eventTransport.ReadAfter(readCtx, tenant, owner, taskID, startID)
		if err != nil {
			if readCtx.Err() != nil || errors.Is(err, taskmanager.ErrTaskNotFoundSentinel) {
				return
			}
			log.Warnf("RedisTaskManager: failed to read event stream for task %s: %v", taskID, err)
			if !waitForRetry() {
				return
			}
			continue
		}
		startID = nextID
		// A transport may implement ReadAfter as a non-blocking poll. Exponential
		// backoff keeps long-idle subscriptions from producing fixed-rate Redis
		// traffic; per-tailer jitter avoids synchronized polling across replicas.
		if len(events) == 0 && nextID == previousID {
			if !waitForRetry() {
				return
			}
			continue
		}
		idleDelay = taskEventReadIdleInitialDelay
		for _, event := range events {
			if err := sub.Send(event); err != nil {
				return // consumer gone.
			}
			// Terminate on a terminal STATUS frame only — an artifact's
			// lastChunk (also IsFinal on the artifact) ends an artifact, not
			// the task.
			if su := event.GetStatusUpdate(); su != nil && isFinalState(su.Status.State) {
				return
			}
		}
	}
}

// appendTaskEvent distributes an event that does not modify the Task snapshot.
// Task-changing events use commitTaskEvent so their snapshot and event share
// one atomic commit.
func (m *TaskManager) appendTaskEvent(
	ctx context.Context,
	tenant string,
	owner string,
	taskID string,
	event protocol.StreamResponse,
) error {
	return m.eventTransport.AppendEvent(ctx, tenant, owner, taskID, event)
}

// =============================================================================
// Internal helper methods
// =============================================================================

// stampReplyMessage fills the framework-owned reply fields before the event is
// persisted or published.
func (m *TaskManager) stampReplyMessage(ctxID *string, message *protocol.Message) {
	message.ContextID = ctxID
	message.Role = protocol.MessageRoleAgent
	if message.MessageID == "" {
		message.MessageID = protocol.GenerateMessageID()
	}
	if message.ContextID == nil || *message.ContextID == "" {
		contextID := protocol.GenerateContextID()
		message.ContextID = &contextID
	}
}

// storeMessage stores a message in Redis and updates conversation history.
func (m *TaskManager) storeMessage(ctx context.Context, tenant, owner string, message protocol.Message) {
	// Store the message.
	msgKey := messageKey(tenant, owner, message.MessageID)
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
		convKey := conversationKey(tenant, owner, contextID)

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
	tenant string,
	owner string,
	contextID string,
	length int,
) ([]protocol.Message, error) {
	if contextID == "" {
		return nil, nil
	}

	convKey := conversationKey(tenant, owner, contextID)

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
		msgKey := messageKey(tenant, owner, msgID)
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
func (m *TaskManager) getTaskInternal(ctx context.Context, tenant, owner, taskID string) (*protocol.Task, error) {
	taskBytes, err := m.client.Get(ctx, taskKey(tenant, owner, taskID)).Bytes()
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

// ensureTaskIndexed records taskID in the tenant-and-owner-local listing index before
// the Task write. A later Task-write failure may leave a stale member, which
// ListTasks removes lazily; the reverse ordering could make a successfully
// persisted task permanently invisible.
func (m *TaskManager) ensureTaskIndexed(ctx context.Context, tenant, owner, taskID string) error {
	indexKey := taskIndexKey(tenant, owner)
	indexExpiration := m.expiration
	if indexExpiration <= time.Duration(1<<63-1)/2 {
		indexExpiration *= 2
	}
	expiresAt := time.Now().Add(indexExpiration).UnixMilli()
	if _, err := m.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.ZAdd(ctx, indexKey, redis.Z{Score: float64(expiresAt), Member: taskID})
		pipe.Expire(ctx, indexKey, indexExpiration)
		return nil
	}); err != nil {
		return fmt.Errorf("failed to index task %s: %w", taskID, err)
	}
	return nil
}

// storeTask stores a task in Redis.
func (m *TaskManager) storeTask(ctx context.Context, tenant, owner string, task *protocol.Task) error {
	if err := m.ensureTaskIndexed(ctx, tenant, owner, task.ID); err != nil {
		return err
	}
	return m.storeTaskWithoutIndex(ctx, tenant, owner, task)
}

// commitTaskEvent atomically persists a Task snapshot and the event that
// produced it in the per-task journal.
func (m *TaskManager) commitTaskEvent(
	ctx context.Context,
	tenant string,
	owner string,
	task *protocol.Task,
	event protocol.StreamResponse,
	allowCreate bool,
) error {
	if err := m.ensureTaskIndexed(ctx, tenant, owner, task.ID); err != nil {
		return err
	}
	if err := m.eventTransport.CommitTaskEvent(ctx, tenant, owner, task, event, allowCreate); err != nil {
		return err
	}
	// Keep the latest v2 storeTask behavior: every Task write refreshes its push
	// registrations. This key cannot join the atomic script because the existing
	// push key is in a different Redis Cluster slot; failure here must not turn an
	// already-committed Task/event pair into a false negative result.
	if err := m.client.Expire(ctx, pushNotificationKey(tenant, owner, task.ID), m.expiration).Err(); err != nil {
		log.Warnf("RedisTaskManager: failed to refresh push config expiration for task %s: %v", task.ID, err)
	}
	return nil
}

// refreshTaskLease extends the storage owned by a live execution without
// recreating an expired Task. The Task/Stream/dedupe keys renew atomically;
// the scoped index and push registrations live in other cluster slots and are
// refreshed afterwards.
func (m *TaskManager) refreshTaskLease(ctx context.Context, tenant, owner, taskID string) error {
	if err := m.eventTransport.RefreshTaskLease(ctx, tenant, owner, taskID); err != nil {
		return err
	}
	if err := m.ensureTaskIndexed(ctx, tenant, owner, taskID); err != nil {
		return err
	}
	if err := m.client.Expire(ctx, pushNotificationKey(tenant, owner, taskID), m.expiration).Err(); err != nil {
		log.Warnf("RedisTaskManager: failed to refresh push config expiration for task %s: %v", taskID, err)
	}
	return nil
}

// storeTaskWithoutIndex persists the Task after ensureTaskIndexed succeeded.
func (m *TaskManager) storeTaskWithoutIndex(ctx context.Context, tenant, owner string, task *protocol.Task) error {
	key := taskKey(tenant, owner, task.ID)
	taskBytes, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("failed to serialize task: %w", err)
	}
	_, err = m.client.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Set(ctx, key, taskBytes, m.expiration)
		pipe.Expire(ctx, pushNotificationKey(tenant, owner, task.ID), m.expiration)
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
	tenant string,
	owner string,
	taskID string,
	live *liveExecution,
) error {
	key := newScopedID(tenant, owner, taskID)
	for {
		m.cancelMu.Lock()
		if m.closed {
			m.cancelMu.Unlock()
			return taskmanager.ErrInternalError("task manager is closed")
		}
		current, exists := m.executions[key]
		if !exists {
			m.executions[key] = live
			// Counted under the registry lock so Close (which flips m.closed first)
			// can never begin waiting before a just-admitted run is counted.
			m.engineWg.Add(1)
			m.cancelMu.Unlock()
			return nil
		}
		yieldDone := current.yieldDone
		m.cancelMu.Unlock()
		if yieldDone == nil {
			return taskmanager.ErrInvalidParams(
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
func (m *TaskManager) releaseExecution(tenant, owner, taskID string, live *liveExecution) {
	m.deregisterExecution(tenant, owner, taskID, live)
	m.engineWg.Done()
}

// claimCancelSlot atomically returns the task's live run or — when there is
// none — claims the execution slot with a sentinel, so a no-live cancel's
// CANCELED write gets the same single-writer guarantee as a run: no
// continuation can register (and then write) concurrently with it.
func (m *TaskManager) claimCancelSlot(
	tenant string,
	owner string,
	taskID string,
) (live *liveExecution, sentinel *liveExecution, yieldDone <-chan struct{}) {
	key := newScopedID(tenant, owner, taskID)
	m.cancelMu.Lock()
	defer m.cancelMu.Unlock()
	if exec, ok := m.executions[key]; ok {
		if exec.yieldDone != nil {
			return nil, nil, exec.yieldDone
		}
		return exec, nil, nil
	}
	sentinel = &liveExecution{cancel: func() {}}
	m.executions[key] = sentinel
	return nil, sentinel, nil
}

// requestExecutionCancel linearizes an accepted cancellation against a
// suspend handoff. It returns the handoff channel when yield won, or accepted
// after publishing cancelRequested under the registry lock.
func (m *TaskManager) requestExecutionCancel(
	tenant string,
	owner string,
	taskID string,
	live *liveExecution,
) (yieldDone <-chan struct{}, accepted bool) {
	key := newScopedID(tenant, owner, taskID)
	m.cancelMu.Lock()
	if m.executions[key] != live {
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
func (m *TaskManager) liveRun(tenant, owner, taskID string) *liveExecution {
	m.cancelMu.RLock()
	defer m.cancelMu.RUnlock()
	return m.executions[newScopedID(tenant, owner, taskID)]
}

// deregisterExecution removes the handle at engine end. It only removes its
// own entry so a follow-up round on the same task is never evicted.
func (m *TaskManager) deregisterExecution(tenant, owner, taskID string, live *liveExecution) {
	key := newScopedID(tenant, owner, taskID)
	m.cancelMu.Lock()
	defer m.cancelMu.Unlock()
	if current, ok := m.executions[key]; ok && current == live {
		delete(m.executions, key)
		if live.yieldDone != nil {
			close(live.yieldDone)
			live.yieldDone = nil
		}
	}
}

// beginExecutionYield turns an active slot into a short handoff barrier. The
// slot remains owned by live until its suspend frame is committed and current
// request publication completes, but continuations wait instead of failing.
func (m *TaskManager) beginExecutionYield(tenant, owner, taskID string, live *liveExecution) bool {
	key := newScopedID(tenant, owner, taskID)
	m.cancelMu.Lock()
	defer m.cancelMu.Unlock()
	if m.executions[key] == live && live.yieldDone == nil && !live.cancelRequested.Load() {
		live.yieldDone = make(chan struct{})
		return true
	}
	return false
}

// abortExecutionYield restores an active slot when committing the suspended
// state fails. Waiters wake and re-evaluate it as an ordinary active run.
func (m *TaskManager) abortExecutionYield(tenant, owner, taskID string, live *liveExecution) {
	key := newScopedID(tenant, owner, taskID)
	m.cancelMu.Lock()
	defer m.cancelMu.Unlock()
	if m.executions[key] == live && live.yieldDone != nil {
		close(live.yieldDone)
		live.yieldDone = nil
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
		// Tailers do not participate in engine persistence. Cancel them before
		// waiting for processors so subscriptions close promptly even when a
		// processor takes time to honor cancellation. Redis remains open until
		// both groups have joined.
		m.baseCancel()
		m.tailerWg.Wait()
		// Wait for the detached engines: their final persists (close-rule
		// CANCELED) must land while the Redis client is still usable.
		m.engineWg.Wait()
		m.closeErr = m.client.Close()
	})
	return m.closeErr
}
