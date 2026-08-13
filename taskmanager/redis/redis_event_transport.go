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
	"time"

	redisclient "github.com/redis/go-redis/v9"

	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

// taskEventTransport distributes Task events between Redis TaskManager
// instances. Cursors are opaque and implementations must preserve event order.
// CommitTaskEvent and LoadTaskAndCursor provide the atomic boundaries needed so
// a concurrent Task-changing event is either reflected by the loaded snapshot
// and cursor or returned by a subsequent ReadAfter call, never lost between the
// two.
type taskEventTransport interface {
	// CommitTaskEvent atomically stores a Task snapshot and the event that
	// produced it. allowCreate is true only for a task's first event; later
	// events must not recreate a Task that expired while execution was silent.
	CommitTaskEvent(
		ctx context.Context,
		tenant string,
		owner string,
		task *protocol.Task,
		event protocol.StreamResponse,
		allowCreate bool,
	) error
	// AppendEvent stores an event that does not modify the Task snapshot.
	AppendEvent(
		ctx context.Context,
		tenant string,
		owner string,
		taskID string,
		event protocol.StreamResponse,
	) error
	// LoadTaskAndCursor atomically loads the current Task snapshot and current
	// event cursor. A subscriber reads only events committed after that cursor.
	LoadTaskAndCursor(
		ctx context.Context,
		tenant string,
		owner string,
		taskID string,
	) (*protocol.Task, string, error)
	// ReadAfter returns an ordered batch strictly after cursor and the cursor for
	// the next call. An empty batch is not an error; callers must wait or back off
	// when the cursor also did not advance.
	ReadAfter(
		ctx context.Context,
		tenant string,
		owner string,
		taskID string,
		cursor string,
	) ([]protocol.StreamResponse, string, error)
	// RefreshTaskLease extends an existing live Task and its event journal. It
	// must not create a missing Task.
	RefreshTaskLease(ctx context.Context, tenant string, owner string, taskID string) error
}

const (
	// streamPrefix keys the per-task event stream used for cross-node resubscribe.
	// streamKey adds the task key as a Redis Cluster hash tag so the task and
	// stream can be read or written atomically by one script.
	streamPrefix = "stream:"
	// streamDedupePrefix keys the bounded operation journal that makes a Lua
	// write safe when go-redis retries after an ambiguous network failure.
	streamDedupePrefix = "stream-dedupe:"
	// streamField is the single XADD field carrying the JSON-encoded StreamResponse.
	streamField = "e"
	// streamMaxLen caps each task's event stream (XADD MAXLEN ~).
	streamMaxLen = 10000
	// streamReadCount caps entries returned per read.
	streamReadCount = 64
)

// commitTaskEventScript atomically persists a Task snapshot and its event. The
// stream type is checked before either write because Redis scripts are atomic
// with respect to other clients but do not roll back commands after a runtime
// error. The task and stream keys share one Redis Cluster slot (see streamKey).
var commitTaskEventScript = redisclient.NewScript(`
if ARGV[6] ~= '1' and redis.call('EXISTS', KEYS[1]) == 0 then
    return redis.error_reply('task does not exist')
end
local stream_type = redis.call('TYPE', KEYS[2]).ok
if stream_type ~= 'none' and stream_type ~= 'stream' then
    return redis.error_reply('task event stream has wrong type')
end
local dedupe_type = redis.call('TYPE', KEYS[3]).ok
if dedupe_type ~= 'none' and dedupe_type ~= 'zset' then
    return redis.error_reply('task event dedupe journal has wrong type')
end
local existing = redis.call('ZSCORE', KEYS[3], ARGV[7])
if existing then
    return existing
end
local event_id = redis.call(
    'XADD', KEYS[2], 'MAXLEN', '~', ARGV[3], '*', ARGV[4], ARGV[2]
)
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[5])
redis.call('PEXPIRE', KEYS[2], ARGV[5])
local sequence = redis.call('ZINCRBY', KEYS[3], 1, '__seq')
redis.call('ZADD', KEYS[3], sequence, ARGV[7])
local excess = redis.call('ZCARD', KEYS[3]) - (tonumber(ARGV[3]) + 1)
if excess > 0 then
    redis.call('ZREMRANGEBYRANK', KEYS[3], 0, excess - 1)
end
redis.call('PEXPIRE', KEYS[3], ARGV[5])
return event_id
`)

// appendEventScript persists an event that does not change the Task snapshot.
// Its TTL is capped to the Task's remaining TTL so an event-only write cannot
// leave an orphan stream after the Task expires.
var appendEventScript = redisclient.NewScript(`
local task_ttl = redis.call('PTTL', KEYS[1])
if task_ttl == -2 then
    return redis.error_reply('task does not exist')
end
local stream_type = redis.call('TYPE', KEYS[2]).ok
if stream_type ~= 'none' and stream_type ~= 'stream' then
    return redis.error_reply('task event stream has wrong type')
end
local dedupe_type = redis.call('TYPE', KEYS[3]).ok
if dedupe_type ~= 'none' and dedupe_type ~= 'zset' then
    return redis.error_reply('task event dedupe journal has wrong type')
end
local existing = redis.call('ZSCORE', KEYS[3], ARGV[4])
if existing then
    return existing
end
local event_id = redis.call(
    'XADD', KEYS[2], 'MAXLEN', '~', ARGV[2], '*', ARGV[3], ARGV[1]
)
local sequence = redis.call('ZINCRBY', KEYS[3], 1, '__seq')
redis.call('ZADD', KEYS[3], sequence, ARGV[4])
local excess = redis.call('ZCARD', KEYS[3]) - (tonumber(ARGV[2]) + 1)
if excess > 0 then
    redis.call('ZREMRANGEBYRANK', KEYS[3], 0, excess - 1)
end
if task_ttl == -1 then
    redis.call('PERSIST', KEYS[2])
    redis.call('PERSIST', KEYS[3])
else
    redis.call('PEXPIRE', KEYS[2], math.max(1, task_ttl))
    redis.call('PEXPIRE', KEYS[3], math.max(1, task_ttl))
end
return event_id
`)

// readAfterScript reads strictly after the cursor and checks Task existence at
// the same Redis linearization point. Keeping both operations in one script
// halves idle subscriber traffic without pinning a shared pool connection.
var readAfterScript = redisclient.NewScript(`
local entries = redis.call('XRANGE', KEYS[2], ARGV[1], '+', 'COUNT', tonumber(ARGV[2]) + 1)
local result = {}
local event_count = 0
for _, entry in ipairs(entries) do
    if entry[1] ~= ARGV[1] and event_count < tonumber(ARGV[2]) then
        local payload = ''
        for field_index = 1, #entry[2], 2 do
            if entry[2][field_index] == ARGV[3] then
                payload = entry[2][field_index + 1]
                break
            end
        end
        table.insert(result, entry[1])
        table.insert(result, payload)
        event_count = event_count + 1
    end
end
if #result == 0 and redis.call('EXISTS', KEYS[1]) == 0 then
    return redis.error_reply('task does not exist')
end
return result
`)

// refreshTaskLeaseScript conditionally renews the Task, Stream, and dedupe
// journal in one cluster slot. PEXPIRE returns zero for a missing Task, so a
// late heartbeat cannot resurrect an expired execution.
var refreshTaskLeaseScript = redisclient.NewScript(`
if redis.call('PEXPIRE', KEYS[1], ARGV[1]) == 0 then
    return 0
end
redis.call('PEXPIRE', KEYS[2], ARGV[1])
redis.call('PEXPIRE', KEYS[3], ARGV[1])
return 1
`)

// loadTaskAndCursorScript returns a Task snapshot and the stream tail from one
// Redis linearization point. A concurrent commitTaskEventScript therefore lands
// wholly before the snapshot/cursor pair or wholly after it.
var loadTaskAndCursorScript = redisclient.NewScript(`
local task = redis.call('GET', KEYS[1])
if not task then
    return {0, '', '0-0'}
end
local tail = redis.call('XREVRANGE', KEYS[2], '+', '-', 'COUNT', 1)
local cursor = '0-0'
if #tail > 0 then
    cursor = tail[1][1]
end
return {1, task, cursor}
`)

type redisTaskEventTransport struct {
	client     redisclient.UniversalClient
	expiration time.Duration
}

var _ taskEventTransport = (*redisTaskEventTransport)(nil)

func newRedisTaskEventTransport(
	client redisclient.UniversalClient,
	expiration time.Duration,
) *redisTaskEventTransport {
	return &redisTaskEventTransport{client: client, expiration: expiration}
}

// streamKey shares the task key's Redis Cluster slot without changing the
// existing task key. Redis hashes the full task key and the {...} portion of
// the stream key, which are the same bytes for manager-generated task IDs.
func streamKey(tenant, owner, taskID string) string {
	return streamPrefix + "{" + taskKey(tenant, owner, taskID) + "}"
}

func streamDedupeKey(tenant, owner, taskID string) string {
	return streamDedupePrefix + "{" + taskKey(tenant, owner, taskID) + "}"
}

func (t *redisTaskEventTransport) CommitTaskEvent(
	ctx context.Context,
	tenant string,
	owner string,
	task *protocol.Task,
	event protocol.StreamResponse,
	allowCreate bool,
) error {
	return t.commitTaskEventWithOperationID(
		ctx, tenant, owner, task, event, allowCreate, "op-"+protocol.GenerateMessageID(),
	)
}

func (t *redisTaskEventTransport) commitTaskEventWithOperationID(
	ctx context.Context,
	tenant string,
	owner string,
	task *protocol.Task,
	event protocol.StreamResponse,
	allowCreate bool,
	operationID string,
) error {
	taskBytes, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("failed to serialize task: %w", err)
	}
	eventBytes, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to serialize task event: %w", err)
	}
	allowCreateFlag := 0
	if allowCreate {
		allowCreateFlag = 1
	}
	if _, err := commitTaskEventScript.Run(
		ctx,
		t.client,
		[]string{
			taskKey(tenant, owner, task.ID),
			streamKey(tenant, owner, task.ID),
			streamDedupeKey(tenant, owner, task.ID),
		},
		taskBytes,
		eventBytes,
		streamMaxLen,
		streamField,
		t.expiration.Milliseconds(),
		allowCreateFlag,
		operationID,
	).Result(); err != nil {
		return fmt.Errorf("failed to store task and event: %w", err)
	}
	return nil
}

func (t *redisTaskEventTransport) AppendEvent(
	ctx context.Context,
	tenant string,
	owner string,
	taskID string,
	event protocol.StreamResponse,
) error {
	return t.appendEventWithOperationID(
		ctx, tenant, owner, taskID, event, "op-"+protocol.GenerateMessageID(),
	)
}

func (t *redisTaskEventTransport) appendEventWithOperationID(
	ctx context.Context,
	tenant string,
	owner string,
	taskID string,
	event protocol.StreamResponse,
	operationID string,
) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal stream event for task %s: %w", taskID, err)
	}
	if _, err := appendEventScript.Run(
		ctx,
		t.client,
		[]string{
			taskKey(tenant, owner, taskID),
			streamKey(tenant, owner, taskID),
			streamDedupeKey(tenant, owner, taskID),
		},
		payload,
		streamMaxLen,
		streamField,
		operationID,
	).Result(); err != nil {
		return fmt.Errorf("failed to append stream event for task %s: %w", taskID, err)
	}
	return nil
}

func (t *redisTaskEventTransport) LoadTaskAndCursor(
	ctx context.Context,
	tenant string,
	owner string,
	taskID string,
) (*protocol.Task, string, error) {
	values, err := loadTaskAndCursorScript.Run(
		ctx,
		t.client,
		[]string{taskKey(tenant, owner, taskID), streamKey(tenant, owner, taskID)},
	).Slice()
	if err != nil {
		return nil, "", fmt.Errorf("failed to load task %s and stream cursor: %w", taskID, err)
	}
	if len(values) != 3 {
		return nil, "", fmt.Errorf("failed to load task %s and stream cursor: unexpected result", taskID)
	}
	exists, ok := values[0].(int64)
	if !ok {
		return nil, "", fmt.Errorf("failed to load task %s and stream cursor: invalid existence flag", taskID)
	}
	if exists == 0 {
		return nil, "", taskmanager.ErrTaskNotFound(taskID)
	}
	taskJSON, ok := values[1].(string)
	if !ok {
		return nil, "", fmt.Errorf("failed to load task %s and stream cursor: invalid task payload", taskID)
	}
	cursor, ok := values[2].(string)
	if !ok || cursor == "" {
		return nil, "", fmt.Errorf("failed to load task %s and stream cursor: invalid cursor", taskID)
	}
	var task protocol.Task
	if err := json.Unmarshal([]byte(taskJSON), &task); err != nil {
		return nil, "", fmt.Errorf("failed to deserialize task: %w", err)
	}
	return &task, cursor, nil
}

func (t *redisTaskEventTransport) ReadAfter(
	ctx context.Context,
	tenant string,
	owner string,
	taskID string,
	cursor string,
) ([]protocol.StreamResponse, string, error) {
	values, err := readAfterScript.Run(
		ctx,
		t.client,
		[]string{taskKey(tenant, owner, taskID), streamKey(tenant, owner, taskID)},
		cursor,
		streamReadCount,
		streamField,
	).Slice()
	if err != nil {
		if errors.Is(err, redisclient.Nil) || strings.Contains(err.Error(), "task does not exist") {
			return nil, cursor, taskmanager.ErrTaskNotFound(taskID)
		}
		return nil, cursor, err
	}

	nextCursor := cursor
	events := make([]protocol.StreamResponse, 0, len(values)/2)
	for i := 0; i+1 < len(values); i += 2 {
		entryID, ok := values[i].(string)
		if !ok || entryID == "" {
			continue
		}
		nextCursor = entryID
		var payload []byte
		switch raw := values[i+1].(type) {
		case string:
			payload = []byte(raw)
		case []byte:
			payload = raw
		default:
			continue
		}
		var event protocol.StreamResponse
		if err := json.Unmarshal(payload, &event); err != nil {
			log.Warnf("RedisTaskManager: discarding malformed stream entry for task %s: %v", taskID, err)
			continue
		}
		events = append(events, event)
	}
	return events, nextCursor, nil
}

func (t *redisTaskEventTransport) RefreshTaskLease(
	ctx context.Context,
	tenant string,
	owner string,
	taskID string,
) error {
	refreshed, err := refreshTaskLeaseScript.Run(
		ctx,
		t.client,
		[]string{
			taskKey(tenant, owner, taskID),
			streamKey(tenant, owner, taskID),
			streamDedupeKey(tenant, owner, taskID),
		},
		t.expiration.Milliseconds(),
	).Int()
	if err != nil {
		return fmt.Errorf("failed to refresh task %s lease: %w", taskID, err)
	}
	if refreshed == 0 {
		return taskmanager.ErrTaskNotFound(taskID)
	}
	return nil
}
