// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package redis

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	redisclient "github.com/redis/go-redis/v9"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager"
	"trpc.group/trpc-go/trpc-a2a-go/v2/taskmanager/retaining"
)

const (
	storeOperationResultsPrefix = "stream-op-results:"
	storePushPrefix             = "stream-push:"
	storeVersionField           = "__task_version"
	storeOperationFieldPrefix   = "op:"
	storeMaxExactInteger        = uint64(1<<53 - 1)
)

// retainingStore implements retaining.Store with Redis Strings, Streams, Lists, and
// Hashes. It owns client and closes it from Close, matching TaskManager's
// existing Redis-client ownership behavior.
type retainingStore struct {
	client           redisclient.UniversalClient
	expiration       time.Duration
	maxHistoryLength int
	closed           atomic.Bool
	closeOnce        sync.Once
	closeErr         error
}

var _ retaining.Store = (*retainingStore)(nil)

// newRetainingStore creates a retaining Store using the existing Redis TaskManager
// options for TTL and conversation-history limits.
func newRetainingStore(client redisclient.UniversalClient, opts ...TaskManagerOption) (*retainingStore, error) {
	if client == nil {
		return nil, errors.New("redis store: client cannot be nil")
	}
	if err := client.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("redis store: connect: %w", err)
	}
	options := DefaultRedisTaskManagerOptions()
	for _, opt := range opts {
		opt(options)
	}
	expiration := options.ExpireTime
	if expiration < time.Millisecond {
		expiration = time.Millisecond
	}
	maxHistoryLength := options.MaxHistoryLength
	if maxHistoryLength <= 0 {
		maxHistoryLength = defaultMaxHistoryLength
	}
	return &retainingStore{
		client:           client,
		expiration:       expiration,
		maxHistoryLength: maxHistoryLength,
	}, nil
}

func storeOperationResultsKey(tenant, owner, taskID string) string {
	return storeOperationResultsPrefix + "{" + taskKey(tenant, owner, taskID) + "}"
}

// storePushKey intentionally differs from the legacy pushNotificationKey. The
// hash tag puts Task and Push in one Redis Cluster slot, which is required for
// SavePushConfig to reject an expired Task and write the config atomically.
func storePushKey(tenant, owner, taskID string) string {
	return storePushPrefix + "{" + taskKey(tenant, owner, taskID) + "}"
}

var storeLoadTaskScript = redisclient.NewScript(`
local task = redis.call('GET', KEYS[1])
if not task then
    return {0, '', '0-0', '0'}
end
local tail = redis.call('XREVRANGE', KEYS[2], '+', '-', 'COUNT', 1)
local cursor = '0-0'
if #tail > 0 then
    cursor = tail[1][1]
end
local version = redis.call('HGET', KEYS[3], ARGV[1]) or '0'
return {1, task, cursor, version}
`)

// storeCommitEventScript is shared by Task-changing and Message events. Every
// Task-associated event advances one revision, while only kind=task replaces
// the Task JSON. Operation lookup precedes CAS and terminal checks.
var storeCommitEventScript = redisclient.NewScript(`
local task_type = redis.call('TYPE', KEYS[1]).ok
if task_type ~= 'none' and task_type ~= 'string' then
    return redis.error_reply('STORE_WRONG_TASK_TYPE')
end
local stream_type = redis.call('TYPE', KEYS[2]).ok
if stream_type ~= 'none' and stream_type ~= 'stream' then
    return redis.error_reply('STORE_WRONG_STREAM_TYPE')
end
local dedupe_type = redis.call('TYPE', KEYS[3]).ok
if dedupe_type ~= 'none' and dedupe_type ~= 'zset' then
    return redis.error_reply('STORE_WRONG_DEDUPE_TYPE')
end
local result_type = redis.call('TYPE', KEYS[4]).ok
if result_type ~= 'none' and result_type ~= 'hash' then
    return redis.error_reply('STORE_WRONG_RESULT_TYPE')
end

local existing = redis.call('HGET', KEYS[4], ARGV[10])
if existing then
    local decoded = cjson.decode(existing)
    if decoded.kind ~= ARGV[1] or decoded.digest ~= ARGV[2] then
        return redis.error_reply('STORE_OPERATION_CONFLICT')
    end
    return {tostring(decoded.version), decoded.cursor, '1'}
end
if redis.call('ZSCORE', KEYS[3], ARGV[10]) or redis.call('ZSCORE', KEYS[3], ARGV[11]) then
    return redis.error_reply('STORE_OPERATION_EXPIRED')
end

local task = redis.call('GET', KEYS[1])
if not task then
    if ARGV[1] ~= 'task' or ARGV[4] ~= '1' then
        return redis.error_reply('STORE_TASK_NOT_FOUND')
    end
end

local version = tonumber(redis.call('HGET', KEYS[4], ARGV[12]) or '0')
local expected = tonumber(ARGV[3])
if not version or not expected or version ~= expected then
    return redis.error_reply('STORE_VERSION_CONFLICT')
end
if version >= tonumber(ARGV[13]) then
    return redis.error_reply('STORE_VERSION_OVERFLOW')
end
if task then
    local current_task = cjson.decode(task)
    local state = current_task.status and current_task.status.state or ''
    if state == 'TASK_STATE_COMPLETED' or state == 'TASK_STATE_FAILED' or
       state == 'TASK_STATE_CANCELED' or state == 'TASK_STATE_REJECTED' then
        return redis.error_reply('STORE_TASK_TERMINAL')
    end
end
local sequence = tonumber(redis.call('ZSCORE', KEYS[3], '__seq') or '0')
if not sequence or sequence >= tonumber(ARGV[13]) then
    return redis.error_reply('STORE_OPERATION_SEQUENCE_OVERFLOW')
end
local next_version = version + 1
local next_sequence = sequence + 1
local event_id = redis.call(
    'XADD', KEYS[2], 'MAXLEN', '~', ARGV[7], '*', ARGV[8], ARGV[6]
)
if ARGV[1] == 'task' then
    redis.call('SET', KEYS[1], ARGV[5], 'PX', ARGV[9])
end
redis.call('HSET', KEYS[4], ARGV[12], tostring(next_version))
local result = cjson.encode({
    kind=ARGV[1], digest=ARGV[2], version=next_version, cursor=event_id
})
redis.call('HSET', KEYS[4], ARGV[10], result)

redis.call('ZADD', KEYS[3], next_sequence, '__seq')
redis.call('ZADD', KEYS[3], next_sequence, ARGV[10])
local excess = redis.call('ZCARD', KEYS[3]) - (tonumber(ARGV[7]) + 1)
if excess > 0 then
    local victims = redis.call('ZRANGE', KEYS[3], 0, excess - 1)
    for _, victim in ipairs(victims) do
        if victim ~= '__seq' then
            redis.call('ZREM', KEYS[3], victim)
            redis.call('HDEL', KEYS[4], victim)
        end
    end
end

local ttl = tonumber(ARGV[9])
if ARGV[1] == 'message' then
    ttl = redis.call('PTTL', KEYS[1])
end
if ttl == -1 then
    redis.call('PERSIST', KEYS[2])
    redis.call('PERSIST', KEYS[3])
    redis.call('PERSIST', KEYS[4])
else
    ttl = math.max(1, ttl)
    redis.call('PEXPIRE', KEYS[2], ttl)
    redis.call('PEXPIRE', KEYS[3], ttl)
    redis.call('PEXPIRE', KEYS[4], ttl)
end
if ARGV[1] == 'task' then
    redis.call('PEXPIRE', KEYS[5], tonumber(ARGV[9]))
end
return {tostring(next_version), event_id, '0'}
`)

var storeReadEventsScript = redisclient.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then
    return redis.error_reply('STORE_TASK_NOT_FOUND')
end
local first = redis.call('XRANGE', KEYS[2], '-', '+', 'COUNT', 1)
if ARGV[1] ~= '0-0' then
    if #first == 0 then
        return redis.error_reply('STORE_CURSOR_EXPIRED')
    end
    local exact = redis.call('XRANGE', KEYS[2], ARGV[1], ARGV[1], 'COUNT', 1)
    if #exact == 0 then
        return redis.error_reply('STORE_CURSOR_EXPIRED')
    end
end
local entries = redis.call('XRANGE', KEYS[2], ARGV[1], '+', 'COUNT', tonumber(ARGV[2]) + 1)
local result = {}
local count = 0
for _, entry in ipairs(entries) do
    if entry[1] ~= ARGV[1] and count < tonumber(ARGV[2]) then
        local payload = ''
        for field_index = 1, #entry[2], 2 do
            if entry[2][field_index] == ARGV[3] then
                payload = entry[2][field_index + 1]
                break
            end
        end
        table.insert(result, entry[1])
        table.insert(result, payload)
        count = count + 1
    end
end
return result
`)

var storeRefreshLeaseScript = redisclient.NewScript(`
if redis.call('PEXPIRE', KEYS[1], ARGV[1]) == 0 then
    return 0
end
redis.call('PEXPIRE', KEYS[2], ARGV[1])
redis.call('PEXPIRE', KEYS[3], ARGV[1])
redis.call('PEXPIRE', KEYS[4], ARGV[1])
redis.call('PEXPIRE', KEYS[5], ARGV[1])
return 1
`)

var storeSavePushScript = redisclient.NewScript(`
local task_ttl = redis.call('PTTL', KEYS[1])
if task_ttl == -2 then
    return redis.error_reply('STORE_TASK_NOT_FOUND')
end
local push_type = redis.call('TYPE', KEYS[2]).ok
if push_type ~= 'none' and push_type ~= 'hash' then
    return redis.error_reply('STORE_WRONG_PUSH_TYPE')
end
redis.call('HSET', KEYS[2], ARGV[1], ARGV[2])
if task_ttl == -1 then
    redis.call('PERSIST', KEYS[2])
else
    redis.call('PEXPIRE', KEYS[2], math.max(1, task_ttl))
end
return 1
`)

var storeGetPushScript = redisclient.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then
    return redis.error_reply('STORE_TASK_NOT_FOUND')
end
return redis.call('HGET', KEYS[2], ARGV[1])
`)

var storeListPushScript = redisclient.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then
    return redis.error_reply('STORE_TASK_NOT_FOUND')
end
return redis.call('HGETALL', KEYS[2])
`)

var storeDeletePushScript = redisclient.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then
    return redis.error_reply('STORE_TASK_NOT_FOUND')
end
return redis.call('HDEL', KEYS[2], ARGV[1])
`)

func (s *retainingStore) checkOpen() error {
	if s == nil || s.closed.Load() {
		return retaining.ErrStoreClosed
	}
	return nil
}

func validateTaskKey(key retaining.TaskKey) error {
	if key.ID == "" {
		return errors.New("redis store: task ID is required")
	}
	return nil
}

// LoadTask returns an atomic Task/version observation.
func (s *retainingStore) LoadTask(ctx context.Context, key retaining.TaskKey) (*retaining.TaskRecord, error) {
	record, err := s.loadTask(ctx, key)
	if err != nil {
		return nil, err
	}
	record.Cursor = ""
	return record, nil
}

// LoadTaskAndCursor returns an atomic Task/version/stream-tail observation.
func (s *retainingStore) LoadTaskAndCursor(
	ctx context.Context, key retaining.TaskKey,
) (*retaining.TaskRecord, error) {
	return s.loadTask(ctx, key)
}

func (s *retainingStore) loadTask(ctx context.Context, key retaining.TaskKey) (*retaining.TaskRecord, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if err := validateTaskKey(key); err != nil {
		return nil, err
	}
	values, err := storeLoadTaskScript.Run(
		ctx,
		s.client,
		[]string{
			taskKey(key.Tenant, key.Owner, key.ID),
			streamKey(key.Tenant, key.Owner, key.ID),
			storeOperationResultsKey(key.Tenant, key.Owner, key.ID),
		},
		storeVersionField,
	).Slice()
	if err != nil {
		return nil, fmt.Errorf("redis store: load task %s: %w", key.ID, err)
	}
	if len(values) != 4 {
		return nil, fmt.Errorf("redis store: load task %s: unexpected result", key.ID)
	}
	exists, ok := values[0].(int64)
	if !ok || exists == 0 {
		return nil, retaining.ErrTaskNotFound
	}
	taskJSON, ok := redisString(values[1])
	if !ok {
		return nil, fmt.Errorf("redis store: load task %s: invalid payload", key.ID)
	}
	cursor, ok := redisString(values[2])
	if !ok {
		return nil, fmt.Errorf("redis store: load task %s: invalid cursor", key.ID)
	}
	versionText, ok := redisString(values[3])
	if !ok {
		return nil, fmt.Errorf("redis store: load task %s: invalid version", key.ID)
	}
	version, err := strconv.ParseUint(versionText, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("redis store: load task %s: parse version: %w", key.ID, err)
	}
	var task protocol.Task
	if err := json.Unmarshal([]byte(taskJSON), &task); err != nil {
		return nil, fmt.Errorf("redis store: load task %s: decode: %w", key.ID, err)
	}
	return &retaining.TaskRecord{Task: &task, Version: version, Cursor: retaining.Cursor(cursor)}, nil
}

// CommitTaskEvent atomically applies CAS, replaces the snapshot, and appends
// the corresponding event.
func (s *retainingStore) CommitTaskEvent(
	ctx context.Context, req retaining.CommitTaskEventRequest,
) (uint64, retaining.Cursor, error) {
	if err := s.checkOpen(); err != nil {
		return 0, "", err
	}
	if req.Task == nil {
		return 0, "", errors.New("redis store: task is required")
	}
	if err := validateTaskKey(req.Key); err != nil {
		return 0, "", err
	}
	if req.Task.ID != req.Key.ID {
		return 0, "", errors.New("redis store: TaskKey ID and Task ID differ")
	}
	if err := validateTaskEvent(req.Task, req.Event); err != nil {
		return 0, "", err
	}
	if req.OperationID == "" {
		return 0, "", errors.New("redis store: OperationID is required")
	}
	taskJSON, err := json.Marshal(req.Task)
	if err != nil {
		return 0, "", fmt.Errorf("redis store: encode task: %w", err)
	}
	eventJSON, err := json.Marshal(req.Event)
	if err != nil {
		return 0, "", fmt.Errorf("redis store: encode task event: %w", err)
	}
	digest, err := requestDigest(struct {
		Kind            string
		Key             retaining.TaskKey
		ExpectedVersion uint64
		AllowCreate     bool
		Task            json.RawMessage
		Event           json.RawMessage
	}{"task", req.Key, req.ExpectedVersion, req.AllowCreate, taskJSON, eventJSON})
	if err != nil {
		return 0, "", err
	}
	if err := s.ensureTaskIndexed(ctx, req.Key); err != nil {
		return 0, "", err
	}
	return s.commitEvent(ctx, req.Key, "task", req.ExpectedVersion, req.AllowCreate,
		req.OperationID, digest, taskJSON, eventJSON)
}

// CommitMessageEvent appends the event derived from Message, advances the Task
// revision, then completes the cross-slot history projection idempotently.
func (s *retainingStore) CommitMessageEvent(
	ctx context.Context, req retaining.CommitMessageEventRequest,
) (uint64, retaining.Cursor, error) {
	if err := s.checkOpen(); err != nil {
		return 0, "", err
	}
	if err := validateTaskKey(req.Key); err != nil {
		return 0, "", err
	}
	if req.Message.MessageID == "" {
		return 0, "", errors.New("redis store: MessageID is required")
	}
	if req.Message.TaskID == nil || *req.Message.TaskID != req.Key.ID {
		return 0, "", errors.New("redis store: TaskKey ID and Message TaskID differ")
	}
	event := protocol.NewStreamResponseMessage(&req.Message)
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return 0, "", fmt.Errorf("redis store: encode message event: %w", err)
	}
	digest, err := requestDigest(struct {
		Kind            string
		Key             retaining.TaskKey
		ExpectedVersion uint64
		Message         protocol.Message
	}{"message", req.Key, req.ExpectedVersion, req.Message})
	if err != nil {
		return 0, "", err
	}
	operationExists, err := s.checkOperationConflict(ctx, req.Key, req.OperationID, "message", digest)
	if err != nil {
		return 0, "", err
	}
	if !operationExists {
		if err := s.checkMessageConflict(ctx, req.Key.Tenant, req.Key.Owner, req.Message); err != nil {
			return 0, "", err
		}
	}
	version, cursor, err := s.commitEvent(ctx, req.Key, "message", req.ExpectedVersion,
		false, req.OperationID, digest, nil, eventJSON)
	if err != nil {
		return 0, "", err
	}
	if err := s.SaveMessage(ctx, req.Key.Tenant, req.Key.Owner, req.Message); err != nil {
		return version, cursor, fmt.Errorf("%w: project message: %v", retaining.ErrCommitUncertain, err)
	}
	return version, cursor, nil
}

func (s *retainingStore) commitEvent(
	ctx context.Context,
	key retaining.TaskKey,
	kind string,
	expectedVersion uint64,
	allowCreate bool,
	operationID string,
	digest string,
	taskJSON []byte,
	eventJSON []byte,
) (uint64, retaining.Cursor, error) {
	if err := s.checkOpen(); err != nil {
		return 0, "", err
	}
	if operationID == "" {
		return 0, "", errors.New("redis store: OperationID is required")
	}
	if expectedVersion > storeMaxExactInteger {
		return 0, "", errors.New("redis store: expected version exceeds Redis Lua exact integer range")
	}
	allowCreateFlag := 0
	if allowCreate {
		allowCreateFlag = 1
	}
	opField := storeOperationFieldPrefix + operationID
	values, err := storeCommitEventScript.Run(
		ctx,
		s.client,
		[]string{
			taskKey(key.Tenant, key.Owner, key.ID),
			streamKey(key.Tenant, key.Owner, key.ID),
			streamDedupeKey(key.Tenant, key.Owner, key.ID),
			storeOperationResultsKey(key.Tenant, key.Owner, key.ID),
			storePushKey(key.Tenant, key.Owner, key.ID),
		},
		kind,
		digest,
		expectedVersion,
		allowCreateFlag,
		taskJSON,
		eventJSON,
		streamMaxLen,
		streamField,
		s.expiration.Milliseconds(),
		opField,
		operationID,
		storeVersionField,
		storeMaxExactInteger,
	).Slice()
	if err != nil {
		if mapped := mapStoreScriptError(err); mapped != nil {
			return 0, "", mapped
		}
		return 0, "", fmt.Errorf("%w: commit %s event: %v", retaining.ErrCommitUncertain, kind, err)
	}
	if len(values) != 3 {
		return 0, "", fmt.Errorf("%w: commit %s event returned unexpected result", retaining.ErrCommitUncertain, kind)
	}
	versionText, ok := redisString(values[0])
	if !ok {
		return 0, "", fmt.Errorf("%w: commit %s event returned invalid version", retaining.ErrCommitUncertain, kind)
	}
	version, err := strconv.ParseUint(versionText, 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("%w: parse committed version: %v", retaining.ErrCommitUncertain, err)
	}
	cursor, ok := redisString(values[1])
	if !ok || cursor == "" {
		return 0, "", fmt.Errorf("%w: commit %s event returned invalid cursor", retaining.ErrCommitUncertain, kind)
	}
	return version, retaining.Cursor(cursor), nil
}

func validateTaskEvent(task *protocol.Task, event protocol.StreamResponse) error {
	if status := event.GetStatusUpdate(); status != nil {
		if status.TaskID != task.ID || status.ContextID != task.ContextID {
			return errors.New("redis store: status event scope disagrees with Task snapshot")
		}
		return nil
	}
	if artifact := event.GetArtifactUpdate(); artifact != nil {
		if artifact.TaskID != task.ID || artifact.ContextID != task.ContextID {
			return errors.New("redis store: artifact event scope disagrees with Task snapshot")
		}
		return nil
	}
	return errors.New("redis store: CommitTaskEvent requires a Status or Artifact event")
}

func (s *retainingStore) checkOperationConflict(
	ctx context.Context,
	key retaining.TaskKey,
	operationID string,
	kind string,
	digest string,
) (bool, error) {
	if operationID == "" {
		return false, errors.New("redis store: OperationID is required")
	}
	payload, err := s.client.HGet(
		ctx,
		storeOperationResultsKey(key.Tenant, key.Owner, key.ID),
		storeOperationFieldPrefix+operationID,
	).Bytes()
	if errors.Is(err, redisclient.Nil) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("redis store: load operation result: %w", err)
	}
	var result struct {
		Kind   string `json:"kind"`
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return false, fmt.Errorf("redis store: decode operation result: %w", err)
	}
	if result.Kind != kind || result.Digest != digest {
		return true, retaining.ErrOperationConflict
	}
	return true, nil
}

// ReadTaskEvents returns decodable events strictly after after and separately
// advances next over every physical Stream entry inspected.
func (s *retainingStore) ReadTaskEvents(
	ctx context.Context,
	key retaining.TaskKey,
	after retaining.Cursor,
	limit int,
) ([]retaining.StoredEvent, retaining.Cursor, error) {
	if err := s.checkOpen(); err != nil {
		return nil, after, err
	}
	if err := validateTaskKey(key); err != nil {
		return nil, after, err
	}
	if limit <= 0 {
		return []retaining.StoredEvent{}, after, nil
	}
	start := string(after)
	if start == "" {
		start = "0-0"
	}
	values, err := storeReadEventsScript.Run(
		ctx,
		s.client,
		[]string{taskKey(key.Tenant, key.Owner, key.ID), streamKey(key.Tenant, key.Owner, key.ID)},
		start,
		limit,
		streamField,
	).Slice()
	if err != nil {
		if mapped := mapStoreScriptError(err); mapped != nil {
			return nil, after, mapped
		}
		return nil, after, fmt.Errorf("redis store: read task %s events: %w", key.ID, err)
	}
	next := retaining.Cursor(start)
	events := make([]retaining.StoredEvent, 0, len(values)/2)
	for i := 0; i+1 < len(values); i += 2 {
		entryID, ok := redisString(values[i])
		if !ok || entryID == "" {
			continue
		}
		next = retaining.Cursor(entryID)
		payload, ok := redisBytes(values[i+1])
		if !ok {
			continue
		}
		var event protocol.StreamResponse
		if err := json.Unmarshal(payload, &event); err != nil {
			continue
		}
		events = append(events, retaining.StoredEvent{Cursor: next, Event: event})
	}
	return events, next, nil
}

// RefreshTaskLease refreshes only state owned by a still-live Task.
func (s *retainingStore) RefreshTaskLease(ctx context.Context, key retaining.TaskKey) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if err := validateTaskKey(key); err != nil {
		return err
	}
	refreshed, err := storeRefreshLeaseScript.Run(
		ctx,
		s.client,
		[]string{
			taskKey(key.Tenant, key.Owner, key.ID),
			streamKey(key.Tenant, key.Owner, key.ID),
			streamDedupeKey(key.Tenant, key.Owner, key.ID),
			storeOperationResultsKey(key.Tenant, key.Owner, key.ID),
			storePushKey(key.Tenant, key.Owner, key.ID),
		},
		s.expiration.Milliseconds(),
	).Int()
	if err != nil {
		return fmt.Errorf("redis store: refresh task %s lease: %w", key.ID, err)
	}
	if refreshed == 0 {
		return retaining.ErrTaskNotFound
	}
	return s.ensureTaskIndexed(ctx, key)
}

// SaveMessage writes immutable Message content and idempotently appends its ID
// to the conversation index.
func (s *retainingStore) SaveMessage(
	ctx context.Context, tenant, owner string, message protocol.Message,
) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if message.MessageID == "" {
		return errors.New("redis store: MessageID is required")
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("redis store: encode message %s: %w", message.MessageID, err)
	}
	key := messageKey(tenant, owner, message.MessageID)
	created, err := s.client.SetNX(ctx, key, payload, s.expiration).Result()
	if err != nil {
		return fmt.Errorf("redis store: save message %s: %w", message.MessageID, err)
	}
	if !created {
		if err := s.checkMessageConflict(ctx, tenant, owner, message); err != nil {
			return err
		}
		if err := s.client.Expire(ctx, key, s.expiration).Err(); err != nil {
			return fmt.Errorf("redis store: refresh message %s: %w", message.MessageID, err)
		}
	}
	if message.ContextID == nil || *message.ContextID == "" {
		return nil
	}
	if _, err := appendConversationMessageScript.Run(
		ctx,
		s.client,
		[]string{conversationKey(tenant, owner, *message.ContextID)},
		message.MessageID,
		s.expiration.Milliseconds(),
		s.maxHistoryLength,
	).Result(); err != nil {
		return fmt.Errorf("redis store: index message %s: %w", message.MessageID, err)
	}
	return nil
}

func (s *retainingStore) checkMessageConflict(
	ctx context.Context, tenant, owner string, message protocol.Message,
) error {
	stored, err := s.client.Get(ctx, messageKey(tenant, owner, message.MessageID)).Bytes()
	if errors.Is(err, redisclient.Nil) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("redis store: load message %s: %w", message.MessageID, err)
	}
	want, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("redis store: encode message %s: %w", message.MessageID, err)
	}
	if bytes.Equal(stored, want) {
		return nil
	}
	var decoded protocol.Message
	if err := json.Unmarshal(stored, &decoded); err == nil {
		canonical, marshalErr := json.Marshal(decoded)
		if marshalErr == nil && bytes.Equal(canonical, want) {
			return nil
		}
	}
	return retaining.ErrMessageConflict
}

// LoadHistory loads the newest limit Messages in protocol order.
func (s *retainingStore) LoadHistory(
	ctx context.Context, tenant, owner, contextID string, limit int,
) ([]protocol.Message, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if contextID == "" || limit == 0 {
		return []protocol.Message{}, nil
	}
	start := int64(0)
	if limit > 0 {
		start = -int64(limit)
	}
	messageIDs, err := s.client.LRange(ctx, conversationKey(tenant, owner, contextID), start, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("redis store: load conversation %s: %w", contextID, err)
	}
	messages := make([]protocol.Message, 0, len(messageIDs))
	for _, messageID := range messageIDs {
		payload, err := s.client.Get(ctx, messageKey(tenant, owner, messageID)).Bytes()
		if errors.Is(err, redisclient.Nil) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("redis store: load message %s: %w", messageID, err)
		}
		var message protocol.Message
		if err := json.Unmarshal(payload, &message); err != nil {
			return nil, fmt.Errorf("redis store: decode message %s: %w", messageID, err)
		}
		messages = append(messages, message)
	}
	return messages, nil
}

// ListTasks uses the existing scoped expiry index and shared keyset paginator.
func (s *retainingStore) ListTasks(
	ctx context.Context, tenant, owner string, params protocol.ListTasksParams,
) (*protocol.ListTasksResult, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if params.Tenant != "" && params.Tenant != tenant {
		return nil, errors.New("redis store: ListTasks tenant disagrees with authoritative scope")
	}
	params.Tenant = tenant
	afterTime, err := taskmanager.ParseListTasksStatusTimestampAfter(params.StatusTimestampAfter)
	if err != nil {
		return nil, err
	}
	indexKey := taskIndexKey(tenant, owner)
	nowMillis := time.Now().UnixMilli()
	if err := s.client.ZRemRangeByScore(ctx, indexKey, "-inf", strconv.FormatInt(nowMillis, 10)).Err(); err != nil {
		return nil, fmt.Errorf("redis store: prune task index: %w", err)
	}
	taskIDs, err := s.client.ZRangeByScore(ctx, indexKey, &redisclient.ZRangeBy{
		Min: "(" + strconv.FormatInt(nowMillis, 10), Max: "+inf",
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("redis store: load task index: %w", err)
	}
	loads := make([]*redisclient.StringCmd, len(taskIDs))
	_, pipelineErr := s.client.Pipelined(ctx, func(pipe redisclient.Pipeliner) error {
		for i, taskID := range taskIDs {
			loads[i] = pipe.Get(ctx, taskKey(tenant, owner, taskID))
		}
		return nil
	})
	if pipelineErr != nil && !errors.Is(pipelineErr, redisclient.Nil) {
		return nil, fmt.Errorf("redis store: load indexed tasks: %w", pipelineErr)
	}
	filtered := make([]*protocol.Task, 0, len(loads))
	for i, load := range loads {
		payload, err := load.Bytes()
		if errors.Is(err, redisclient.Nil) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("redis store: load task %s: %w", taskIDs[i], err)
		}
		var task protocol.Task
		if err := json.Unmarshal(payload, &task); err != nil {
			return nil, fmt.Errorf("redis store: decode task %s: %w", taskIDs[i], err)
		}
		task.History = nil
		if taskmanager.TaskMatchesListFilter(&task, params, afterTime) {
			filtered = append(filtered, &task)
		}
	}
	return taskmanager.PaginateTasks(filtered, params)
}

func (s *retainingStore) ensureTaskIndexed(ctx context.Context, key retaining.TaskKey) error {
	indexExpiration := s.expiration
	if indexExpiration <= time.Duration(1<<63-1)/2 {
		indexExpiration *= 2
	}
	expiresAt := time.Now().Add(indexExpiration).UnixMilli()
	_, err := s.client.TxPipelined(ctx, func(pipe redisclient.Pipeliner) error {
		pipe.ZAdd(ctx, taskIndexKey(key.Tenant, key.Owner), redisclient.Z{
			Score: float64(expiresAt), Member: key.ID,
		})
		pipe.Expire(ctx, taskIndexKey(key.Tenant, key.Owner), indexExpiration)
		return nil
	})
	if err != nil {
		return fmt.Errorf("redis store: index task %s: %w", key.ID, err)
	}
	return nil
}

// SavePushConfig writes a generated registration only if the Task is visible
// at the same Redis linearization point.
func (s *retainingStore) SavePushConfig(
	ctx context.Context,
	key retaining.TaskKey,
	config protocol.TaskPushNotificationConfig,
) (push.Registration, error) {
	if err := s.checkOpen(); err != nil {
		return push.Registration{}, err
	}
	if err := validateTaskKey(key); err != nil {
		return push.Registration{}, err
	}
	if config.Tenant != "" && config.Tenant != key.Tenant {
		return push.Registration{}, errors.New("redis store: push config tenant disagrees with TaskKey")
	}
	if config.TaskID != "" && config.TaskID != key.ID {
		return push.Registration{}, errors.New("redis store: push config task ID disagrees with TaskKey")
	}
	config.Tenant = key.Tenant
	config.TaskID = key.ID
	if config.ID == "" {
		config.ID = "push-" + uuid.New().String()
	}
	registration := push.Registration{
		Config: config, Generation: uuid.New().String(), Owner: key.Owner,
	}
	payload, err := json.Marshal(registration)
	if err != nil {
		return push.Registration{}, fmt.Errorf("redis store: encode push config: %w", err)
	}
	_, err = storeSavePushScript.Run(
		ctx,
		s.client,
		[]string{taskKey(key.Tenant, key.Owner, key.ID), storePushKey(key.Tenant, key.Owner, key.ID)},
		config.ID,
		payload,
	).Result()
	if err != nil {
		if mapped := mapStoreScriptError(err); mapped != nil {
			return push.Registration{}, mapped
		}
		return push.Registration{}, fmt.Errorf("redis store: save push config: %w", err)
	}
	stored, err := decodePushRegistration(payload)
	if err != nil {
		return push.Registration{}, fmt.Errorf("redis store: copy push config: %w", err)
	}
	stored.Owner = key.Owner
	return stored, nil
}

// GetPushConfig returns one current registration for a visible Task.
func (s *retainingStore) GetPushConfig(
	ctx context.Context, key retaining.TaskKey, configID string,
) (push.Registration, error) {
	if err := s.checkOpen(); err != nil {
		return push.Registration{}, err
	}
	if err := validateTaskKey(key); err != nil {
		return push.Registration{}, err
	}
	value, err := storeGetPushScript.Run(
		ctx,
		s.client,
		[]string{taskKey(key.Tenant, key.Owner, key.ID), storePushKey(key.Tenant, key.Owner, key.ID)},
		configID,
	).Result()
	if errors.Is(err, redisclient.Nil) {
		return push.Registration{}, retaining.ErrPushConfigNotFound
	}
	if err != nil {
		if mapped := mapStoreScriptError(err); mapped != nil {
			return push.Registration{}, mapped
		}
		return push.Registration{}, fmt.Errorf("redis store: get push config: %w", err)
	}
	payload, ok := redisBytes(value)
	if !ok {
		return push.Registration{}, fmt.Errorf("redis store: get push config: invalid payload")
	}
	registration, err := decodePushRegistration(payload)
	if err != nil {
		return push.Registration{}, fmt.Errorf("redis store: decode push config: %w", err)
	}
	registration.Owner = key.Owner
	return registration, nil
}

// ListPushConfigs returns registrations ordered by config ID.
func (s *retainingStore) ListPushConfigs(
	ctx context.Context, key retaining.TaskKey,
) ([]push.Registration, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if err := validateTaskKey(key); err != nil {
		return nil, err
	}
	values, err := storeListPushScript.Run(
		ctx,
		s.client,
		[]string{taskKey(key.Tenant, key.Owner, key.ID), storePushKey(key.Tenant, key.Owner, key.ID)},
	).Slice()
	if err != nil {
		if mapped := mapStoreScriptError(err); mapped != nil {
			return nil, mapped
		}
		return nil, fmt.Errorf("redis store: list push configs: %w", err)
	}
	registrations := make([]push.Registration, 0, len(values)/2)
	for i := 0; i+1 < len(values); i += 2 {
		payload, ok := redisBytes(values[i+1])
		if !ok {
			return nil, fmt.Errorf("redis store: list push configs: invalid payload")
		}
		registration, err := decodePushRegistration(payload)
		if err != nil {
			return nil, fmt.Errorf("redis store: decode push config: %w", err)
		}
		registration.Owner = key.Owner
		registrations = append(registrations, registration)
	}
	sort.Slice(registrations, func(i, j int) bool {
		return registrations[i].Config.ID < registrations[j].Config.ID
	})
	return registrations, nil
}

// DeletePushConfig removes a config for a still-visible Task. Missing configs
// remain a no-op.
func (s *retainingStore) DeletePushConfig(
	ctx context.Context, key retaining.TaskKey, configID string,
) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if err := validateTaskKey(key); err != nil {
		return err
	}
	_, err := storeDeletePushScript.Run(
		ctx,
		s.client,
		[]string{taskKey(key.Tenant, key.Owner, key.ID), storePushKey(key.Tenant, key.Owner, key.ID)},
		configID,
	).Result()
	if err != nil {
		if mapped := mapStoreScriptError(err); mapped != nil {
			return mapped
		}
		return fmt.Errorf("redis store: delete push config: %w", err)
	}
	return nil
}

// Close closes the Redis client exactly once.
func (s *retainingStore) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		s.closeErr = s.client.Close()
	})
	return s.closeErr
}

func requestDigest(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("redis store: encode request digest: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func redisString(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case []byte:
		return string(typed), true
	case int64:
		return strconv.FormatInt(typed, 10), true
	default:
		return "", false
	}
}

func redisBytes(value any) ([]byte, bool) {
	switch typed := value.(type) {
	case string:
		return []byte(typed), true
	case []byte:
		return typed, true
	default:
		return nil, false
	}
}

func mapStoreScriptError(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "STORE_TASK_NOT_FOUND"):
		return retaining.ErrTaskNotFound
	case strings.Contains(message, "STORE_VERSION_CONFLICT"):
		return retaining.ErrVersionConflict
	case strings.Contains(message, "STORE_TASK_TERMINAL"):
		return retaining.ErrTaskTerminal
	case strings.Contains(message, "STORE_OPERATION_CONFLICT"):
		return retaining.ErrOperationConflict
	case strings.Contains(message, "STORE_OPERATION_EXPIRED"):
		return retaining.ErrOperationExpired
	case strings.Contains(message, "STORE_CURSOR_EXPIRED"):
		return retaining.ErrCursorExpired
	default:
		return nil
	}
}
