// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package pushdispatch

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
	"trpc.group/trpc-go/trpc-a2a-go/v2/push"
)

func statusResponse(state protocol.TaskState) protocol.StreamResponse {
	return protocol.NewStreamResponseStatusUpdate(&protocol.TaskStatusUpdateEvent{
		TaskID: "task-1",
		Status: protocol.TaskStatus{State: state},
	})
}

func TestDispatcherPreservesOrderPerConfig(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	delivered := make(chan protocol.TaskState, 2)
	var once sync.Once
	sender := push.SenderFunc(func(
		ctx context.Context, _ protocol.TaskPushNotificationConfig, event protocol.StreamResponse,
	) error {
		state := event.GetStatusUpdate().Status.State
		if state == protocol.TaskStateWorking {
			once.Do(func() { close(firstStarted) })
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		delivered <- state
		return nil
	})
	d := New(context.Background(), sender, 4, 8)
	defer d.Close()
	cfg := protocol.TaskPushNotificationConfig{TaskID: "task-1", ID: "config-1", URL: "https://example.com"}

	if err := d.Enqueue([]protocol.TaskPushNotificationConfig{cfg}, statusResponse(protocol.TaskStateWorking)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first delivery did not start")
	}
	if err := d.Enqueue([]protocol.TaskPushNotificationConfig{cfg}, statusResponse(protocol.TaskStateCompleted)); err != nil {
		t.Fatal(err)
	}
	close(releaseFirst)

	for _, want := range []protocol.TaskState{protocol.TaskStateWorking, protocol.TaskStateCompleted} {
		select {
		case got := <-delivered:
			if got != want {
				t.Fatalf("delivery order = %s, want %s", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", want)
		}
	}
}

func TestDispatcherSnapshotsInputs(t *testing.T) {
	release := make(chan struct{})
	received := make(chan struct {
		cfg   protocol.TaskPushNotificationConfig
		event protocol.StreamResponse
	}, 1)
	sender := push.SenderFunc(func(
		ctx context.Context, cfg protocol.TaskPushNotificationConfig, event protocol.StreamResponse,
	) error {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		received <- struct {
			cfg   protocol.TaskPushNotificationConfig
			event protocol.StreamResponse
		}{cfg: cfg, event: event}
		return nil
	})
	d := New(context.Background(), sender, 1, 1)
	defer d.Close()
	auth := &protocol.AuthenticationInfo{Scheme: "Bearer", Credentials: "original"}
	cfg := protocol.TaskPushNotificationConfig{
		TaskID: "task-1", ID: "config-1", URL: "https://original.example", Authentication: auth,
	}
	event := statusResponse(protocol.TaskStateWorking)
	if err := d.Enqueue([]protocol.TaskPushNotificationConfig{cfg}, event); err != nil {
		t.Fatal(err)
	}
	auth.Credentials = "mutated"
	cfg.URL = "https://mutated.example"
	event.GetStatusUpdate().Status.State = protocol.TaskStateFailed
	close(release)

	select {
	case got := <-received:
		if got.cfg.URL != "https://original.example" || got.cfg.Authentication.Credentials != "original" {
			t.Fatalf("config was not snapshotted: %+v", got.cfg)
		}
		if state := got.event.GetStatusUpdate().Status.State; state != protocol.TaskStateWorking {
			t.Fatalf("event state = %s, want working", state)
		}
	case <-time.After(time.Second):
		t.Fatal("delivery did not complete")
	}
}

func TestDispatcherBackpressureUnblocksOnClose(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	sender := push.SenderFunc(func(
		ctx context.Context, _ protocol.TaskPushNotificationConfig, _ protocol.StreamResponse,
	) error {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return ctx.Err()
	})
	d := New(context.Background(), sender, 1, 1)
	cfg := protocol.TaskPushNotificationConfig{TaskID: "task-1", ID: "config-1", URL: "https://example.com"}
	if err := d.Enqueue([]protocol.TaskPushNotificationConfig{cfg}, statusResponse(protocol.TaskStateWorking)); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := d.Enqueue([]protocol.TaskPushNotificationConfig{cfg}, statusResponse(protocol.TaskStateWorking)); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan error, 1)
	go func() {
		blocked <- d.Enqueue([]protocol.TaskPushNotificationConfig{cfg}, statusResponse(protocol.TaskStateWorking))
	}()
	select {
	case err := <-blocked:
		t.Fatalf("enqueue returned before queue capacity was available: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	d.Close()
	select {
	case err := <-blocked:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("blocked enqueue error = %v, want ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock enqueue")
	}
}
