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
	"time"

	redisclient "github.com/redis/go-redis/v9"

	"trpc.group/trpc-go/trpc-a2a-go/v2/internal/taskevent"
	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
)

const (
	// streamPrefix keys the per-task event stream used for cross-node resubscribe.
	// streamKey adds the task key as a Redis Cluster hash tag so the task and
	// stream can be read or written atomically by one script.
	streamPrefix = "stream:"
	// streamField is the single XADD field carrying the JSON-encoded StreamResponse.
	streamField = "e"
	// streamMaxLen caps each task's event stream (XADD MAXLEN ~).
	streamMaxLen = 10000
	// streamReadCount caps entries returned per XREAD.
	streamReadCount = 64
)

// commitTaskEventScript atomically persists a Task snapshot and its event. The
// stream type is checked before either write because Redis scripts are atomic
// with respect to other clients but do not roll back commands after a runtime
// error. The task and stream keys share one Redis Cluster slot (see streamKey).
var commitTaskEventScript = redisclient.NewScript(`
local stream_type = redis.call('TYPE', KEYS[2]).ok
if stream_type ~= 'none' and stream_type ~= 'stream' then
    return redis.error_reply('task event stream has wrong type')
end
local event_id = redis.call(
    'XADD', KEYS[2], 'MAXLEN', '~', ARGV[3], '*', ARGV[4], ARGV[2]
)
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[5])
redis.call('PEXPIRE', KEYS[2], ARGV[5])
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
local event_id = redis.call(
    'XADD', KEYS[2], 'MAXLEN', '~', ARGV[2], '*', ARGV[3], ARGV[1]
)
if task_ttl == -1 then
    redis.call('PERSIST', KEYS[2])
else
    redis.call('PEXPIRE', KEYS[2], math.max(1, task_ttl))
end
return event_id
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

var _ taskevent.Transport = (*redisTaskEventTransport)(nil)

func newRedisTaskEventTransport(
	client redisclient.UniversalClient,
	expiration time.Duration,
) *redisTaskEventTransport {
	return &redisTaskEventTransport{client: client, expiration: expiration}
}

// streamKey shares the task key's Redis Cluster slot without changing the
// existing task key. Redis hashes the full task key and the {...} portion of
// the stream key, which are the same bytes for manager-generated task IDs.
func streamKey(taskID string) string {
	return streamPrefix + "{" + taskPrefix + taskID + "}"
}

func (t *redisTaskEventTransport) CommitTaskEvent(
	ctx context.Context,
	task *protocol.Task,
	event protocol.StreamResponse,
) error {
	taskBytes, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("failed to serialize task: %w", err)
	}
	eventBytes, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to serialize task event: %w", err)
	}
	if _, err := commitTaskEventScript.Run(
		ctx,
		t.client,
		[]string{taskPrefix + task.ID, streamKey(task.ID)},
		taskBytes,
		eventBytes,
		streamMaxLen,
		streamField,
		t.expiration.Milliseconds(),
	).Result(); err != nil {
		return fmt.Errorf("failed to store task and event: %w", err)
	}
	return nil
}

func (t *redisTaskEventTransport) AppendEvent(
	ctx context.Context,
	taskID string,
	event protocol.StreamResponse,
) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal stream event for task %s: %w", taskID, err)
	}
	if _, err := appendEventScript.Run(
		ctx,
		t.client,
		[]string{taskPrefix + taskID, streamKey(taskID)},
		payload,
		streamMaxLen,
		streamField,
	).Result(); err != nil {
		return fmt.Errorf("failed to append stream event for task %s: %w", taskID, err)
	}
	return nil
}

func (t *redisTaskEventTransport) LoadTaskAndCursor(
	ctx context.Context,
	taskID string,
) (*protocol.Task, string, error) {
	values, err := loadTaskAndCursorScript.Run(
		ctx,
		t.client,
		[]string{taskPrefix + taskID, streamKey(taskID)},
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
	taskID string,
	cursor string,
) ([]protocol.StreamResponse, string, error) {
	res, err := t.client.XRead(ctx, &redisclient.XReadArgs{
		Streams: []string{streamKey(taskID), cursor},
		// Do not pin the TaskManager's shared Redis connection pool while a
		// subscription is idle. The manager applies a context-aware backoff when
		// this non-blocking read has no event or cursor progress.
		Block: -1,
		Count: streamReadCount,
	}).Result()
	if errors.Is(err, redisclient.Nil) {
		return nil, cursor, nil
	}
	if err != nil {
		return nil, cursor, err
	}

	nextCursor := cursor
	events := make([]protocol.StreamResponse, 0)
	for _, stream := range res {
		for _, entry := range stream.Messages {
			nextCursor = entry.ID
			var payload []byte
			switch raw := entry.Values[streamField].(type) {
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
	}
	return events, nextCursor, nil
}
