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
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"trpc.group/trpc-go/trpc-a2a-go/v2/internal/jsonrpc"
	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
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

	// cancelMu is a mutex for the executions map.
	cancelMu sync.RWMutex
	// executions maps task IDs to live execution handles so OnCancelTask can
	// cancel the MessageProcessor's context.
	executions map[string]*liveExecution

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

	manager := &TaskManager{
		processor:   processor,
		client:      client,
		expiration:  options.ExpireTime,
		subscribers: make(map[string][]*taskSubscriber),
		executions:  make(map[string]*liveExecution),
		options:     options,
	}

	return manager, nil
}

// OnSendMessage handles the message/send request. It invokes the MessageProcessor
// and derives the result from the emitted events: the final task snapshot
// when task events were emitted, otherwise the last Message. The default is
// blocking (returnImmediately=false); with returnImmediately=true it returns
// on the first decisive event while the execution continues in background.
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
		// returnImmediately=true: answer with the first decisive event (first
		// persisted task snapshot or first Message); execution continues in
		// background and results stay retrievable via GetTask/subscriptions.
		select {
		case out := <-ex.decisive:
			return m.buildSendResponse(out.task, out.message, historyLength)
		case <-ex.done:
			// The stream closed before any decisive event: same derivation as
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
// execution a non-terminal task is marked CANCELED directly.
func (m *TaskManager) OnCancelTask(
	ctx context.Context,
	params protocol.TaskIDParams,
) (*protocol.Task, error) {
	m.cancelMu.RLock()
	live, exists := m.executions[params.ID]
	m.cancelMu.RUnlock()

	if exists {
		// A stored terminal state is immutable even while the round is still
		// draining: canceling it must fail like the no-live path does.
		task, err := m.getTaskInternal(ctx, params.ID)
		if err == nil && isFinalState(task.Status.State) {
			return nil, taskmanager.ErrTaskNotCancelable(params.ID, task.Status.State)
		}
		// Flag before canceling so the engine's close rule sees the request even
		// when the MessageProcessor reacts by closing the channel immediately.
		live.requestCancel()
		if err != nil {
			// Execution registered but no task materialized yet (lazy creation).
			return nil, err
		}
		return task, nil
	}

	task, err := m.getTaskInternal(ctx, params.ID)
	if err != nil {
		return nil, err
	}

	// A task already in a terminal state cannot be canceled.
	if isFinalState(task.Status.State) {
		return nil, taskmanager.ErrTaskNotCancelable(params.ID, task.Status.State)
	}

	// No live execution: persist CANCELED, then broadcast it (persist before
	// broadcast) and close the task's subscribers.
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
	// Check if task exists.
	_, err := m.getTaskInternal(ctx, params.TaskID)
	if err != nil {
		return nil, err
	}

	// Store the push notification configuration.
	pushKey := pushNotificationPrefix + params.TaskID
	configBytes, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize push notification config: %w", err)
	}

	if err := m.client.Set(ctx, pushKey, configBytes, m.expiration).Err(); err != nil {
		return nil, fmt.Errorf("failed to store push notification config: %w", err)
	}

	log.Debugf("RedisTaskManager: Push notification config set for task %s", params.TaskID)
	return &params, nil
}

// OnPushNotificationGet handles tasks/pushNotificationConfig/get requests.
func (m *TaskManager) OnPushNotificationGet(
	ctx context.Context,
	params protocol.TaskIDParams,
) (*protocol.TaskPushNotificationConfig, error) {
	// Check if task exists.
	_, err := m.getTaskInternal(ctx, params.ID)
	if err != nil {
		return nil, err
	}

	// Retrieve the push notification configuration.
	pushKey := pushNotificationPrefix + params.ID
	configBytes, err := m.client.Get(ctx, pushKey).Bytes()
	if err != nil {
		return nil, fmt.Errorf("push notification config not found for task: %s", params.ID)
	}

	var config protocol.TaskPushNotificationConfig
	if err := json.Unmarshal(configBytes, &config); err != nil {
		return nil, fmt.Errorf("failed to deserialize push notification config: %w", err)
	}

	return &config, nil
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
// The Redis manager stores at most one configuration per task, so the result
// contains zero or one entries.
func (m *TaskManager) OnPushNotificationList(
	ctx context.Context,
	params protocol.ListTaskPushNotificationConfigsParams,
) (*protocol.ListTaskPushNotificationConfigsResult, error) {
	// Check if task exists.
	if _, err := m.getTaskInternal(ctx, params.TaskID); err != nil {
		return nil, err
	}

	result := &protocol.ListTaskPushNotificationConfigsResult{
		Configs: []protocol.TaskPushNotificationConfig{},
	}
	configBytes, err := m.client.Get(ctx, pushNotificationPrefix+params.TaskID).Bytes()
	if err != nil {
		return result, nil // No config registered.
	}
	var config protocol.TaskPushNotificationConfig
	if err := json.Unmarshal(configBytes, &config); err != nil {
		return nil, fmt.Errorf("failed to deserialize push notification config: %w", err)
	}
	result.Configs = append(result.Configs, config)
	return result, nil
}

// OnPushNotificationDelete handles the v1.0 DeleteTaskPushNotificationConfig request.
// Deleting a non-existent configuration is a no-op.
func (m *TaskManager) OnPushNotificationDelete(
	ctx context.Context,
	params protocol.DeleteTaskPushNotificationConfigParams,
) error {
	if err := m.client.Del(ctx, pushNotificationPrefix+params.TaskID).Err(); err != nil {
		return fmt.Errorf("failed to delete push notification config: %w", err)
	}
	log.Debugf("RedisTaskManager: Push notification config deleted for task %s", params.TaskID)
	return nil
}

// OnResubscribe handles tasks/resubscribe requests.
func (m *TaskManager) OnResubscribe(
	ctx context.Context,
	params protocol.TaskIDParams,
) (<-chan protocol.StreamResponse, error) {
	// Read the snapshot and register the subscriber under subMu: an engine
	// broadcast serializes with this section, so every update either precedes
	// the snapshot (already persisted, hence included) or is delivered to the
	// subscriber after the first frame — no gap, snapshot always first.
	m.subMu.Lock()
	defer m.subMu.Unlock()

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

	// v1.0: the first stream event must be the current Task snapshot.
	// getTaskInternal returns a fresh copy, safe to hand to the subscriber.
	if err := subscriber.Send(protocol.NewStreamResponseTask(task)); err != nil {
		subscriber.Close()
		return nil, err
	}
	m.subscribers[params.ID] = append(m.subscribers[params.ID], subscriber)

	return subscriber.Channel(), nil
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

// getTaskInternal retrieves a task from Redis.
func (m *TaskManager) getTaskInternal(ctx context.Context, taskID string) (*protocol.Task, error) {
	taskKey := taskPrefix + taskID
	taskBytes, err := m.client.Get(ctx, taskKey).Bytes()
	if err != nil {
		return nil, taskmanager.ErrTaskNotFound(taskID)
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
	if _, exists := m.executions[taskID]; exists {
		return jsonrpc.ErrInvalidParams(
			fmt.Sprintf("task %s already has an active execution", taskID))
	}
	m.executions[taskID] = live
	return nil
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

// Close closes the Redis client and cleans up resources.
func (m *TaskManager) Close() error {
	// Cancel every live MessageProcessor run; the detached engines wind down when
	// the Executors close their channels.
	m.cancelMu.Lock()
	for _, live := range m.executions {
		live.cancel()
	}
	m.executions = make(map[string]*liveExecution)
	m.cancelMu.Unlock()

	// Collect subscribers under subMu, then close them outside the lock so a
	// stuck blocking send can never wedge the manager-wide lock.
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

	// Close the Redis client.
	return m.client.Close()
}
