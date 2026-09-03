// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	redisclient "github.com/redis/go-redis/v9"

	"trpc.group/trpc-go/trpc-a2a-go/taskmanager/redis/v2/internal/executionlease"
	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

const executionPrefix = "execution:"

const executionRenewalConcurrency = 32

func executionKey(tenant, owner, taskID string) string {
	return taskCompanionKey(executionPrefix, taskKey(tenant, owner, taskID))
}

// acquireExecutionScript claims a task's execution hash.
//
// KEYS[1] task snapshot (string)
// KEYS[2] execution record (hash)
// ARGV[1] run_id
// ARGV[2] lease duration (ms)
// ARGV[3] execution key TTL (ms)
//
// Returns {code, task}: 1 acquired, 2 lease held, 3 cancel pending, 4 terminal,
// 5 suspend handoff in progress.
var acquireExecutionScript = redisclient.NewScript(`
local task_type = redis.call('TYPE', KEYS[1]).ok
if task_type ~= 'none' and task_type ~= 'string' then
    return redis.error_reply('EXECUTION_WRONG_TASK_TYPE')
end
local execution_type = redis.call('TYPE', KEYS[2]).ok
if execution_type ~= 'none' and execution_type ~= 'hash' then
    return redis.error_reply('EXECUTION_WRONG_RECORD_TYPE')
end

local task = redis.call('GET', KEYS[1])
if task then
    local decoded = cjson.decode(task)
    local state = decoded.status and decoded.status.state or ''
    if state == 'TASK_STATE_COMPLETED' or state == 'TASK_STATE_FAILED' or
       state == 'TASK_STATE_CANCELED' or state == 'TASK_STATE_REJECTED' then
        return {4, task}
    end
end

local current_run = redis.call('HGET', KEYS[2], 'run_id')
if current_run then
    local lease_until = tonumber(redis.call('HGET', KEYS[2], 'lease_until_ms') or '0')
    local cancel_requested = redis.call('HGET', KEYS[2], 'cancel_requested') or '0'
    if cancel_requested == '1' then
        return {3, task or ''}
    end
    local redis_time = redis.call('TIME')
    local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
    if lease_until > now_ms then
        if redis.call('HGET', KEYS[2], 'yielding') == '1' then
            return {5, task or ''}
        end
        return {2, task or ''}
    end
end

local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
redis.call('HSET', KEYS[2],
    'run_id', ARGV[1],
    'lease_until_ms', now_ms + tonumber(ARGV[2]),
    'cancel_requested', '0',
    'yielding', '0')
redis.call('PEXPIRE', KEYS[2], ARGV[3])
return {1, task or ''}
`)

// checkAndRenewExecutionScript extends a still-owned lease.
//
// KEYS[1] execution record (hash)
// ARGV[1] run_id
// ARGV[2] lease duration (ms)
// ARGV[3] execution key TTL (ms)
//
// Returns {owned, canceled} as 1/0. A cancel flag is reported while renewal
// continues so the owner can persist its close-rule result.
var checkAndRenewExecutionScript = redisclient.NewScript(`
local execution_type = redis.call('TYPE', KEYS[1]).ok
if execution_type ~= 'none' and execution_type ~= 'hash' then
    return redis.error_reply('EXECUTION_WRONG_RECORD_TYPE')
end
if redis.call('HGET', KEYS[1], 'run_id') ~= ARGV[1] then
    return {0, 0}
end
local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local lease_until = tonumber(redis.call('HGET', KEYS[1], 'lease_until_ms') or '0')
if lease_until <= now_ms then
    return {0, 0}
end
local next_lease_until = now_ms + tonumber(ARGV[2])
redis.call('HSET', KEYS[1], 'lease_until_ms', next_lease_until)
redis.call('PEXPIRE', KEYS[1], ARGV[3])
if redis.call('HGET', KEYS[1], 'cancel_requested') == '1' then
    return {1, 1}
end
return {1, 0}
`)

// releaseExecutionScript deletes the hash only when ARGV[1] still owns it.
//
// KEYS[1] execution record (hash)
// ARGV[1] run_id
//
// Returns 1 if deleted, 0 if the caller is not the current owner.
var releaseExecutionScript = redisclient.NewScript(`
if redis.call('HGET', KEYS[1], 'run_id') ~= ARGV[1] then
    return 0
end
redis.call('DEL', KEYS[1])
return 1
`)

// requestExecutionCancelScript commits CANCELED or records cancel intent.
//
// KEYS[1] task snapshot (string)
// KEYS[2] event stream
// KEYS[3] event dedupe journal (zset)
// KEYS[4] execution record (hash)
// ARGV[1] observed task bytes (CAS)
// ARGV[2] canceled task JSON
// ARGV[3] canceled event JSON
// ARGV[4] stream MAXLEN
// ARGV[5] task/stream TTL (ms)
// ARGV[6] operation id
// ARGV[7] stream field name
// ARGV[8] execution key TTL (ms)
// ARGV[9] lease duration (ms)
//
// Returns {code, task[, event_id]}: 1 intent only, 2 committed, 3 not
// cancelable, 4 not found, 5 CAS mismatch, 6 suspend handoff in progress.
var requestExecutionCancelScript = redisclient.NewScript(`
local task_type = redis.call('TYPE', KEYS[1]).ok
if task_type ~= 'none' and task_type ~= 'string' then
    return redis.error_reply('EXECUTION_WRONG_TASK_TYPE')
end
local stream_type = redis.call('TYPE', KEYS[2]).ok
if stream_type ~= 'none' and stream_type ~= 'stream' then
    return redis.error_reply('EXECUTION_WRONG_STREAM_TYPE')
end
local dedupe_type = redis.call('TYPE', KEYS[3]).ok
if dedupe_type ~= 'none' and dedupe_type ~= 'zset' then
    return redis.error_reply('EXECUTION_WRONG_DEDUPE_TYPE')
end
local execution_type = redis.call('TYPE', KEYS[4]).ok
if execution_type ~= 'none' and execution_type ~= 'hash' then
    return redis.error_reply('EXECUTION_WRONG_RECORD_TYPE')
end

local task = redis.call('GET', KEYS[1])
if redis.call('ZSCORE', KEYS[3], ARGV[6]) then
    return {2, task or ARGV[2]}
end

local lease_until = tonumber(redis.call('HGET', KEYS[4], 'lease_until_ms') or '0')
local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local active = redis.call('HGET', KEYS[4], 'run_id') and lease_until > now_ms
if active and redis.call('HGET', KEYS[4], 'yielding') == '1' then
    return {6, task or ''}
end
if not task then
    if active then
        redis.call('HSET', KEYS[4],
            'cancel_requested', '1',
            'lease_until_ms', now_ms + tonumber(ARGV[9]))
        redis.call('PEXPIRE', KEYS[4], ARGV[8])
        return {1, ''}
    end
    return {4, ''}
end
local decoded = cjson.decode(task)
local state = decoded.status and decoded.status.state or ''
if state == 'TASK_STATE_COMPLETED' or state == 'TASK_STATE_FAILED' or
   state == 'TASK_STATE_CANCELED' or state == 'TASK_STATE_REJECTED' then
    return {3, task}
end
if active then
    redis.call('HSET', KEYS[4],
        'cancel_requested', '1',
        'lease_until_ms', now_ms + tonumber(ARGV[9]))
    redis.call('PEXPIRE', KEYS[4], ARGV[8])
    return {1, task}
end
if task ~= ARGV[1] then
    return {5, task}
end

local event_id = redis.call(
    'XADD', KEYS[2], 'MAXLEN', '~', ARGV[4], '*', ARGV[7], ARGV[3]
)
redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[5])
redis.call('PEXPIRE', KEYS[2], ARGV[5])
local sequence = redis.call('ZINCRBY', KEYS[3], 1, '__seq')
redis.call('ZADD', KEYS[3], sequence, ARGV[6])
local excess = redis.call('ZCARD', KEYS[3]) - (tonumber(ARGV[4]) + 1)
if excess > 0 then
    redis.call('ZREMRANGEBYRANK', KEYS[3], 0, excess - 1)
end
redis.call('PEXPIRE', KEYS[3], ARGV[5])
redis.call('DEL', KEYS[4])
return {2, ARGV[2], event_id}
`)

func (t *redisTaskEventTransport) AcquireExecution(
	ctx context.Context,
	tenant, owner, taskID, runID string,
) (*protocol.Task, bool, bool, error) {
	for {
		values, err := acquireExecutionScript.Run(
			ctx,
			t.client,
			[]string{taskKey(tenant, owner, taskID), executionKey(tenant, owner, taskID)},
			runID,
			t.executionLeaseDuration.Milliseconds(),
			t.executionRetention.Milliseconds(),
		).Slice()
		if err != nil {
			return nil, false, false, fmt.Errorf("acquire execution for task %s: %w", taskID, err)
		}
		if len(values) < 2 {
			return nil, false, false, fmt.Errorf("acquire execution for task %s: invalid result", taskID)
		}
		code, ok := values[0].(int64)
		if !ok || code < 1 || code > 5 {
			return nil, false, false, fmt.Errorf("acquire execution for task %s: invalid result code", taskID)
		}
		if code == 5 {
			select {
			case <-ctx.Done():
				return nil, false, false, fmt.Errorf("acquire execution for task %s: %w", taskID, ctx.Err())
			case <-time.After(10 * time.Millisecond):
			}
			continue
		}
		task, err := decodeOptionalExecutionTask(values[1])
		if err != nil {
			return nil, false, false, fmt.Errorf("acquire execution for task %s: %w", taskID, err)
		}
		return task, code == 1, code == 3, nil
	}
}

func (t *redisTaskEventTransport) RequestExecutionCancel(
	ctx context.Context,
	tenant, owner, taskID string,
) (*protocol.Task, bool, error) {
	operationID := "cancel-" + protocol.GenerateMessageID()
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		currentBytes, err := t.client.Get(ctx, taskKey(tenant, owner, taskID)).Bytes()
		if errors.Is(err, redisclient.Nil) {
			// Lazy-created runs may not have a Task yet. Lua still records
			// cancel intent against a live lease.
			currentBytes = nil
			err = nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("load task %s for cancellation: %w", taskID, err)
		}

		var canceledBytes, eventBytes []byte
		if len(currentBytes) > 0 {
			var task protocol.Task
			if err := json.Unmarshal(currentBytes, &task); err != nil {
				return nil, false, fmt.Errorf("decode task %s for cancellation: %w", taskID, err)
			}
			if !isFinalState(task.Status.State) {
				canceled := copyTask(&task)
				status := protocol.TaskStatus{
					State:     protocol.TaskStateCanceled,
					Timestamp: time.Now().UTC().Format(time.RFC3339),
				}
				canceled.Status = status
				event := &protocol.TaskStatusUpdateEvent{
					TaskID: canceled.ID, ContextID: canceled.ContextID, Status: status, Final: true,
				}
				canceledBytes, err = json.Marshal(canceled)
				if err != nil {
					return nil, false, fmt.Errorf("encode canceled task %s: %w", taskID, err)
				}
				eventBytes, err = json.Marshal(protocol.NewStreamResponseStatusUpdate(event))
				if err != nil {
					return nil, false, fmt.Errorf("encode canceled event for task %s: %w", taskID, err)
				}
			}
		}

		values, err := requestExecutionCancelScript.Run(
			ctx,
			t.client,
			[]string{
				taskKey(tenant, owner, taskID),
				streamKey(tenant, owner, taskID),
				streamDedupeKey(tenant, owner, taskID),
				executionKey(tenant, owner, taskID),
			},
			currentBytes,
			canceledBytes,
			eventBytes,
			streamMaxLen,
			t.expiration.Milliseconds(),
			operationID,
			streamField,
			t.executionRetention.Milliseconds(),
			t.executionLeaseDuration.Milliseconds(),
		).Slice()
		if err != nil {
			return nil, false, fmt.Errorf("request cancellation for task %s: %w", taskID, err)
		}
		if len(values) < 2 {
			return nil, false, fmt.Errorf("request cancellation for task %s: invalid result", taskID)
		}
		code, ok := values[0].(int64)
		if !ok {
			return nil, false, fmt.Errorf("request cancellation for task %s: invalid result code", taskID)
		}
		if code == 5 {
			continue
		}
		if code == 4 {
			return nil, false, taskmanager.ErrTaskNotFound(taskID)
		}
		resultTask, err := decodeOptionalExecutionTask(values[1])
		if err != nil {
			return nil, false, fmt.Errorf("request cancellation for task %s: %w", taskID, err)
		}
		switch code {
		case 1:
			return resultTask, false, nil
		case 2:
			return resultTask, true, nil
		case 3:
			return resultTask, false, nil
		case 6:
			select {
			case <-ctx.Done():
				return nil, false, fmt.Errorf("request cancellation for task %s: %w", taskID, ctx.Err())
			case <-time.After(10 * time.Millisecond):
			}
			continue
		default:
			return nil, false, fmt.Errorf(
				"request cancellation for task %s: unknown result code %d", taskID, code,
			)
		}
	}
}

func (t *redisTaskEventTransport) CheckAndRenewExecution(
	ctx context.Context,
	tenant, owner, taskID, runID string,
) (bool, bool, error) {
	values, err := checkAndRenewExecutionScript.Run(
		ctx,
		t.client,
		[]string{executionKey(tenant, owner, taskID)},
		runID,
		t.executionLeaseDuration.Milliseconds(),
		t.executionRetention.Milliseconds(),
	).Slice()
	if err != nil {
		return false, false, fmt.Errorf("renew execution for task %s: %w", taskID, err)
	}
	if len(values) != 2 {
		return false, false, fmt.Errorf("renew execution for task %s: invalid result", taskID)
	}
	owned, okOwned := values[0].(int64)
	canceled, okCanceled := values[1].(int64)
	if !okOwned || !okCanceled {
		return false, false, fmt.Errorf("renew execution for task %s: invalid result values", taskID)
	}
	return owned == 1, canceled == 1, nil
}

func (t *redisTaskEventTransport) ReleaseExecution(
	ctx context.Context,
	tenant, owner, taskID, runID string,
) error {
	if _, err := releaseExecutionScript.Run(
		ctx,
		t.client,
		[]string{executionKey(tenant, owner, taskID)},
		runID,
	).Result(); err != nil {
		return fmt.Errorf("release execution for task %s: %w", taskID, err)
	}
	return nil
}

func decodeOptionalExecutionTask(value any) (*protocol.Task, error) {
	var payload []byte
	switch raw := value.(type) {
	case string:
		payload = []byte(raw)
	case []byte:
		payload = raw
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("invalid task payload type %T", value)
	}
	if len(payload) == 0 {
		return nil, nil
	}
	var task protocol.Task
	if err := json.Unmarshal(payload, &task); err != nil {
		return nil, fmt.Errorf("decode task payload: %w", err)
	}
	return &task, nil
}

func mapExecutionScriptError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case strings.Contains(err.Error(), "EXECUTION_CANCEL_REQUESTED"):
		return fmt.Errorf("%w: %v", executionlease.ErrCancelRequested, err)
	case strings.Contains(err.Error(), "EXECUTION_STALE"):
		return fmt.Errorf("%w: %v", executionlease.ErrStale, err)
	default:
		return err
	}
}

func (m *TaskManager) runExecutionControlLoop(leaseDuration time.Duration) {
	defer m.controlWg.Done()
	interval := leaseDuration / 3
	if interval > 2*time.Second {
		interval = 2 * time.Second
	}
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.controlCtx.Done():
			return
		case <-ticker.C:
			m.sweepExecutions()
		}
	}
}

type scopedLiveExecution struct {
	key  scopedID
	live *liveExecution
}

func (m *TaskManager) sweepExecutions() {
	m.cancelMu.RLock()
	liveRuns := make([]scopedLiveExecution, 0, len(m.executions))
	for key, live := range m.executions {
		if live.runID != "" && !live.released.Load() {
			liveRuns = append(liveRuns, scopedLiveExecution{key: key, live: live})
		}
	}
	m.cancelMu.RUnlock()

	if len(liveRuns) == 0 {
		return
	}
	workerCount := executionRenewalConcurrency
	if len(liveRuns) < workerCount {
		workerCount = len(liveRuns)
	}
	jobs := make(chan scopedLiveExecution)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer workers.Done()
			for run := range jobs {
				m.renewExecution(run)
			}
		}()
	}
	for _, run := range liveRuns {
		select {
		case jobs <- run:
		case <-m.controlCtx.Done():
			close(jobs)
			workers.Wait()
			return
		}
	}
	close(jobs)
	workers.Wait()
}

func (m *TaskManager) renewExecution(run scopedLiveExecution) {
	leaseCheckStarted := time.Now()
	owned, canceled, err := m.executionLease.CheckAndRenewExecution(
		m.controlCtx, run.key.tenant, run.key.owner, run.key.id, run.live.runID,
	)
	if err != nil {
		if time.Now().UnixMilli() >= run.live.leaseUntilMillis.Load() {
			m.loseExecution(run.live)
		} else {
			log.Warnf("RedisTaskManager: failed to check execution lease for task %s: %v", run.key.id, err)
		}
		return
	}
	if !owned {
		m.loseExecution(run.live)
		return
	}
	run.live.leaseUntilMillis.Store(
		leaseCheckStarted.Add(m.executionLeaseDuration).UnixMilli(),
	)
	if err := m.refreshTaskLease(m.controlCtx, run.key.tenant, run.key.owner, run.key.id); err != nil &&
		!errors.Is(err, taskmanager.ErrTaskNotFoundSentinel) && !errors.Is(err, context.Canceled) {
		log.Warnf("RedisTaskManager: failed to refresh live task %s: %v", run.key.id, err)
	}
	if canceled {
		_, _ = m.cancelLocalExecution(run.key.tenant, run.key.owner, run.key.id, run.live)
	}
}

func (m *TaskManager) loseExecution(live *liveExecution) {
	if live.released.Load() {
		// A terminal commit or explicit release may have deleted the Redis record
		// before its caller receives the result. That is intentional, not an
		// ownership loss.
		return
	}
	if live.cancelRequested.Load() {
		// The owner is already winding down after observing cancellation. Do
		// not turn that accepted intent into a stale lease error while its
		// close rule or a racing terminal event is still being persisted.
		live.cancel()
		return
	}
	if live.leaseLost.CompareAndSwap(false, true) {
		live.cancel()
		if live.pipe != nil {
			live.pipe.Close()
		}
	}
}

func (m *TaskManager) dispatchCanceledTask(key scopedID, task *protocol.Task) {
	if task == nil || task.Status.State != protocol.TaskStateCanceled {
		return
	}
	event := &protocol.TaskStatusUpdateEvent{
		TaskID: task.ID, ContextID: task.ContextID, Status: task.Status, Final: true,
	}
	m.dispatchPush(key.tenant, key.owner, key.id, protocol.NewStreamResponseStatusUpdate(event))
}

func (m *TaskManager) finishCanceledAdmission(
	tenant string,
	owner string,
	taskID string,
	live *liveExecution,
) error {
	stored, err := m.getTaskInternal(context.Background(), tenant, owner, taskID)
	if err != nil {
		m.releaseExecution(tenant, owner, taskID, live)
		return err
	}
	if isFinalState(stored.Status.State) {
		m.releaseExecution(tenant, owner, taskID, live)
		return taskmanager.ErrInvalidParams(
			fmt.Sprintf("task %s is in terminal state %s", taskID, stored.Status.State))
	}
	canceled := copyTask(stored)
	canceled.Status = protocol.TaskStatus{
		State:     protocol.TaskStateCanceled,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	event := &protocol.TaskStatusUpdateEvent{
		TaskID: canceled.ID, ContextID: canceled.ContextID, Status: canceled.Status, Final: true,
	}
	live.requestCancel()
	live.released.Store(true)
	if err := m.commitExecutionTaskEvent(
		context.Background(), tenant, owner, live.runID, canceled,
		protocol.NewStreamResponseStatusUpdate(event), false, true,
	); err != nil {
		live.released.Store(false)
		m.releaseExecution(tenant, owner, taskID, live)
		return err
	}
	m.dispatchCanceledTask(newScopedID(tenant, owner, taskID), canceled)
	m.releaseExecution(tenant, owner, taskID, live)
	return taskmanager.ErrInvalidParams(fmt.Sprintf("task %s was canceled before execution started", taskID))
}
