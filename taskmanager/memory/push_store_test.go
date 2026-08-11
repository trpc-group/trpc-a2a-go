// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package memory

import (
	"testing"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

func TestPushConfigStore_SaveGeneratesID(t *testing.T) {
	s := newPushConfigStore()
	got, err := s.save(protocol.TaskPushNotificationConfig{TaskID: "t1", URL: "https://x"})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if got.ID == "" {
		t.Error("expected a generated config ID")
	}
}

func TestPushConfigStore_SaveRequiresTaskID(t *testing.T) {
	s := newPushConfigStore()
	if _, err := s.save(protocol.TaskPushNotificationConfig{URL: "https://x"}); err == nil {
		t.Error("expected an error for empty taskId")
	}
}

func TestPushConfigStore_SavePreservesExplicitID(t *testing.T) {
	s := newPushConfigStore()
	got, err := s.save(protocol.TaskPushNotificationConfig{TaskID: "t1", ID: "my-id", URL: "https://a"})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if got.ID != "my-id" {
		t.Errorf("explicit ID not preserved: got %q", got.ID)
	}
}

func TestPushConfigStore_ClonesAuthentication(t *testing.T) {
	s := newPushConfigStore()
	auth := &protocol.AuthenticationInfo{Scheme: "Bearer", Credentials: "secret"}
	_, _ = s.save(protocol.TaskPushNotificationConfig{
		TaskID: "t1", ID: "c1", URL: "https://a", Authentication: auth,
	})
	auth.Credentials = "mutated"

	first, ok := s.get("", "t1", "c1")
	if !ok || first.Authentication.Credentials != "secret" {
		t.Fatalf("caller mutation changed stored config: %+v", first)
	}
	first.Authentication.Credentials = "returned-mutation"
	second, _ := s.get("", "t1", "c1")
	if second.Authentication.Credentials != "secret" {
		t.Fatalf("returned value mutation changed stored config: %+v", second)
	}
}

func TestPushConfigStore_MultipleConfigsPerTask(t *testing.T) {
	s := newPushConfigStore()
	c1, _ := s.save(protocol.TaskPushNotificationConfig{TaskID: "t1", URL: "https://a"})
	c2, _ := s.save(protocol.TaskPushNotificationConfig{TaskID: "t1", URL: "https://b"})
	if c1.ID == c2.ID {
		t.Fatal("expected distinct config IDs")
	}

	if list := s.list("", "t1"); len(list) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(list))
	}

	s.remove("", "t1", c1.ID)
	list := s.list("", "t1")
	if len(list) != 1 || list[0].ID != c2.ID {
		t.Fatalf("expected only c2 to remain, got %+v", list)
	}
}

func TestPushConfigStore_ListEmptyIsNotNil(t *testing.T) {
	s := newPushConfigStore()
	list := s.list("", "nope")
	if list == nil {
		t.Error("expected a non-nil empty slice")
	}
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d", len(list))
	}
}

func TestPushConfigStore_RemoveAll(t *testing.T) {
	s := newPushConfigStore()
	_, _ = s.save(protocol.TaskPushNotificationConfig{TaskID: "t1", URL: "https://a"})
	_, _ = s.save(protocol.TaskPushNotificationConfig{TaskID: "t1", URL: "https://b"})
	s.removeAll("", "t1")
	if list := s.list("", "t1"); len(list) != 0 {
		t.Errorf("expected no configs after removeAll, got %d", len(list))
	}
}

func TestPushConfigStore_CloseClearsAndRejectsWrites(t *testing.T) {
	store := newPushConfigStore()
	if _, err := store.save(protocol.TaskPushNotificationConfig{
		TaskID: "task-1", URL: "https://example.com",
	}); err != nil {
		t.Fatal(err)
	}
	store.close()
	if got := store.list("", "task-1"); len(got) != 0 {
		t.Fatalf("closed store retained configs: %+v", got)
	}
	if _, err := store.save(protocol.TaskPushNotificationConfig{
		TaskID: "task-1", URL: "https://example.com",
	}); err == nil {
		t.Fatal("closed store accepted a write")
	}
}
