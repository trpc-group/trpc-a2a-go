// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package memory

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

// pushConfigStore is the in-memory, multi-config-per-task store backing the
// manager's push-notification config methods. It supports multiple configs per
// task, keyed by config ID.
//
// It is intentionally unexported: pluggable storage backends are not part of the
// public API yet. If distributed backends are needed later, promote this to an
// exported interface then.
type pushConfigStore struct {
	mu sync.RWMutex
	// configs maps taskID -> configID -> config.
	configs map[string]map[string]protocol.TaskPushNotificationConfig
}

func newPushConfigStore() *pushConfigStore {
	return &pushConfigStore{
		configs: make(map[string]map[string]protocol.TaskPushNotificationConfig),
	}
}

// save stores cfg, generating an ID when absent. CreatedAt is server-authoritative:
// stamped once on create, preserved across updates, and a client-supplied value
// is ignored (so it cannot spoof creation time and thus List ordering).
func (s *pushConfigStore) save(
	cfg protocol.TaskPushNotificationConfig,
) (protocol.TaskPushNotificationConfig, error) {
	if cfg.TaskID == "" {
		return protocol.TaskPushNotificationConfig{}, fmt.Errorf("push config store: taskId is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cfg.ID == "" {
		cfg.ID = protocol.GeneratePushConfigID()
	}
	if s.configs[cfg.TaskID] == nil {
		s.configs[cfg.TaskID] = make(map[string]protocol.TaskPushNotificationConfig)
	}
	if existing, ok := s.configs[cfg.TaskID][cfg.ID]; ok && existing.CreatedAt != "" {
		cfg.CreatedAt = existing.CreatedAt
	} else {
		cfg.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	s.configs[cfg.TaskID][cfg.ID] = cfg
	return cfg, nil
}

// list returns all configs for taskID, ordered by CreatedAt then ID so callers
// see a stable ordering across calls (map iteration order is random).
func (s *pushConfigStore) list(taskID string) []protocol.TaskPushNotificationConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byID := s.configs[taskID]
	out := make([]protocol.TaskPushNotificationConfig, 0, len(byID))
	for _, cfg := range byID {
		out = append(out, cfg)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// remove deletes a single config by ID; a missing config is a no-op.
func (s *pushConfigStore) remove(taskID, configID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if byID := s.configs[taskID]; byID != nil {
		delete(byID, configID)
		if len(byID) == 0 {
			delete(s.configs, taskID)
		}
	}
}

// removeAll deletes every config for taskID.
func (s *pushConfigStore) removeAll(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.configs, taskID)
}
