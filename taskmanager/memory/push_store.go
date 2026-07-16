// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/google/uuid"
	"trpc.group/trpc-go/trpc-a2a-go/v2/internal/pushdispatch"
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
	// closed prevents configs from being re-created after manager shutdown.
	closed bool
	// configs maps taskID -> configID -> internal registration. Generation is
	// intentionally kept out of the public protocol type.
	configs map[string]map[string]pushdispatch.Registration
}

func newPushConfigStore() *pushConfigStore {
	return &pushConfigStore{
		configs: make(map[string]map[string]pushdispatch.Registration),
	}
}

// save stores cfg, generating a resource ID when absent.
func (s *pushConfigStore) save(
	cfg protocol.TaskPushNotificationConfig,
) (protocol.TaskPushNotificationConfig, error) {
	if cfg.TaskID == "" {
		return protocol.TaskPushNotificationConfig{}, fmt.Errorf("push config store: taskId is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return protocol.TaskPushNotificationConfig{}, fmt.Errorf("push config store is closed")
	}
	if cfg.ID == "" {
		cfg.ID = "push-" + uuid.New().String()
	}
	cfg = clonePushConfig(cfg)
	if s.configs[cfg.TaskID] == nil {
		s.configs[cfg.TaskID] = make(map[string]pushdispatch.Registration)
	}
	s.configs[cfg.TaskID][cfg.ID] = pushdispatch.Registration{
		Config:     cfg,
		Generation: uuid.New().String(),
	}
	return clonePushConfig(cfg), nil
}

// list returns all configs for taskID, ordered by ID for stable pagination.
func (s *pushConfigStore) list(taskID string) []protocol.TaskPushNotificationConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byID := s.configs[taskID]
	out := make([]protocol.TaskPushNotificationConfig, 0, len(byID))
	for _, registration := range byID {
		out = append(out, clonePushConfig(registration.Config))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

// get returns the config identified by taskID and configID.
func (s *pushConfigStore) get(taskID, configID string) (protocol.TaskPushNotificationConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byID := s.configs[taskID]
	registration, ok := byID[configID]
	return clonePushConfig(registration.Config), ok
}

// registrations returns internal delivery snapshots, ordered by config ID.
// Generation remains internal and lets the dispatcher discard queued work for
// a config that was deleted or replaced after enqueue.
func (s *pushConfigStore) registrations(taskID string) []pushdispatch.Registration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byID := s.configs[taskID]
	out := make([]pushdispatch.Registration, 0, len(byID))
	for _, registration := range byID {
		registration.Config = clonePushConfig(registration.Config)
		out = append(out, registration)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Config.ID < out[j].Config.ID
	})
	return out
}

// isCurrent reports whether registration still names the exact stored
// generation. Updating, deleting, or deleting and re-creating the same config
// ID invalidates already queued deliveries.
func (s *pushConfigStore) isCurrent(
	_ context.Context,
	registration pushdispatch.Registration,
) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byID := s.configs[registration.Config.TaskID]
	current, ok := byID[registration.Config.ID]
	return ok && current.Generation == registration.Generation, nil
}

// clonePushConfig prevents callers from mutating stored credentials after
// Set/Get/List returns.
func clonePushConfig(cfg protocol.TaskPushNotificationConfig) protocol.TaskPushNotificationConfig {
	if cfg.Authentication != nil {
		auth := *cfg.Authentication
		cfg.Authentication = &auth
	}
	return cfg
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

func (s *pushConfigStore) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.configs = make(map[string]map[string]pushdispatch.Registration)
}
