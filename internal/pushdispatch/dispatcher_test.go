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
	d := New(context.Background(), sender, 4, 8, nil)
	defer d.Close()
	cfg := protocol.TaskPushNotificationConfig{TaskID: "task-1", ID: "config-1", URL: "https://example.com"}
	registration := Registration{Config: cfg, Generation: "generation-1"}

	if err := d.Enqueue([]Registration{registration}, statusResponse(protocol.TaskStateWorking)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first delivery did not start")
	}
	if err := d.Enqueue([]Registration{registration}, statusResponse(protocol.TaskStateCompleted)); err != nil {
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
	d := New(context.Background(), sender, 1, 1, nil)
	defer d.Close()
	auth := &protocol.AuthenticationInfo{Scheme: "Bearer", Credentials: "original"}
	cfg := protocol.TaskPushNotificationConfig{
		TaskID: "task-1", ID: "config-1", URL: "https://original.example", Authentication: auth,
	}
	event := statusResponse(protocol.TaskStateWorking)
	registration := Registration{Config: cfg, Generation: "generation-1"}
	if err := d.Enqueue([]Registration{registration}, event); err != nil {
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
	d := New(context.Background(), sender, 1, 1, nil)
	cfg := protocol.TaskPushNotificationConfig{TaskID: "task-1", ID: "config-1", URL: "https://example.com"}
	registration := Registration{Config: cfg, Generation: "generation-1"}
	if err := d.Enqueue([]Registration{registration}, statusResponse(protocol.TaskStateWorking)); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := d.Enqueue([]Registration{registration}, statusResponse(protocol.TaskStateWorking)); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan error, 1)
	go func() {
		blocked <- d.Enqueue([]Registration{registration}, statusResponse(protocol.TaskStateWorking))
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

func TestDispatcherCloseDoesNotClaimQueuedJob(t *testing.T) {
	firstStarted := make(chan struct{})
	validatorCalls := 0
	senderCalls := 0
	sender := push.SenderFunc(func(
		ctx context.Context, _ protocol.TaskPushNotificationConfig, _ protocol.StreamResponse,
	) error {
		senderCalls++
		if senderCalls == 1 {
			close(firstStarted)
		}
		<-ctx.Done()
		return ctx.Err()
	})
	isCurrent := func(context.Context, Registration) (bool, error) {
		validatorCalls++
		return true, nil
	}
	d := New(context.Background(), sender, 1, 1, isCurrent)
	defer d.Close()
	cfg := protocol.TaskPushNotificationConfig{TaskID: "task-1", ID: "config-1", URL: "https://example.com"}
	registration := Registration{Config: cfg, Generation: "generation-1"}
	if err := d.Enqueue([]Registration{registration}, statusResponse(protocol.TaskStateWorking)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first delivery did not enter Sender")
	}
	if err := d.Enqueue([]Registration{registration}, statusResponse(protocol.TaskStateCompleted)); err != nil {
		t.Fatal(err)
	}

	closed := make(chan struct{})
	go func() {
		d.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not stop the in-flight Sender")
	}
	if validatorCalls != 1 {
		t.Fatalf("validator calls = %d, want 1; queued job was claimed after Close", validatorCalls)
	}
	if senderCalls != 1 {
		t.Fatalf("sender calls = %d, want 1; queued job reached Sender after Close", senderCalls)
	}
}

func TestDispatcherRecoversSenderPanicPerJob(t *testing.T) {
	tests := []struct {
		name       string
		panicValue any
	}{
		{name: "value", panicValue: "sender panic"},
		{name: "nil", panicValue: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			delivered := make(chan protocol.TaskState, 1)
			sender := push.SenderFunc(func(
				_ context.Context, _ protocol.TaskPushNotificationConfig, event protocol.StreamResponse,
			) error {
				calls++
				if calls == 1 {
					panic(tt.panicValue)
				}
				delivered <- event.GetStatusUpdate().Status.State
				return nil
			})
			d := New(context.Background(), sender, 1, 2, nil)
			cfg := protocol.TaskPushNotificationConfig{
				TaskID: "task-1", ID: "config-1", URL: "https://example.com",
			}
			registration := Registration{Config: cfg, Generation: "generation-1"}
			if err := d.Enqueue([]Registration{registration}, statusResponse(protocol.TaskStateWorking)); err != nil {
				t.Fatal(err)
			}
			if err := d.Enqueue([]Registration{registration}, statusResponse(protocol.TaskStateCompleted)); err != nil {
				t.Fatal(err)
			}

			select {
			case state := <-delivered:
				if state != protocol.TaskStateCompleted {
					t.Fatalf("state after sender panic = %s, want completed", state)
				}
			case <-time.After(time.Second):
				t.Fatal("worker did not continue after sender panic")
			}

			closed := make(chan struct{})
			go func() {
				d.Close()
				close(closed)
			}()
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("Close hung after recovering a sender panic")
			}
			if calls != 2 {
				t.Fatalf("sender calls = %d, want 2", calls)
			}
		})
	}
}

func TestDispatcherSkipsQueuedOldGeneration(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	delivered := make(chan protocol.TaskState, 2)
	currentGeneration := "generation-1"
	sender := push.SenderFunc(func(
		ctx context.Context, _ protocol.TaskPushNotificationConfig, event protocol.StreamResponse,
	) error {
		state := event.GetStatusUpdate().Status.State
		if state == protocol.TaskStateWorking {
			close(firstStarted)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		delivered <- state
		return nil
	})
	isCurrent := func(_ context.Context, registration Registration) (bool, error) {
		return registration.Generation == currentGeneration, nil
	}
	d := New(context.Background(), sender, 1, 3, isCurrent)
	defer d.Close()
	cfg := protocol.TaskPushNotificationConfig{TaskID: "task-1", ID: "config-1", URL: "https://example.com"}
	oldRegistration := Registration{Config: cfg, Generation: "generation-1"}
	if err := d.Enqueue([]Registration{oldRegistration}, statusResponse(protocol.TaskStateWorking)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first delivery did not start")
	}
	if err := d.Enqueue([]Registration{oldRegistration}, statusResponse(protocol.TaskStateCompleted)); err != nil {
		t.Fatal(err)
	}
	currentGeneration = "generation-2"
	newRegistration := Registration{Config: cfg, Generation: currentGeneration}
	if err := d.Enqueue([]Registration{newRegistration}, statusResponse(protocol.TaskStateFailed)); err != nil {
		t.Fatal(err)
	}
	close(releaseFirst)

	for _, want := range []protocol.TaskState{protocol.TaskStateWorking, protocol.TaskStateFailed} {
		select {
		case got := <-delivered:
			if got != want {
				t.Fatalf("delivered state = %s, want %s", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", want)
		}
	}
}

func TestDispatcherValidationErrorFailsClosed(t *testing.T) {
	delivered := make(chan protocol.TaskState, 2)
	sender := push.SenderFunc(func(
		_ context.Context, _ protocol.TaskPushNotificationConfig, event protocol.StreamResponse,
	) error {
		delivered <- event.GetStatusUpdate().Status.State
		return nil
	})
	isCurrent := func(_ context.Context, registration Registration) (bool, error) {
		if registration.Generation == "invalid" {
			return false, errors.New("validation failed")
		}
		return true, nil
	}
	d := New(context.Background(), sender, 1, 2, isCurrent)
	defer d.Close()
	cfg := protocol.TaskPushNotificationConfig{TaskID: "task-1", ID: "config-1", URL: "https://example.com"}
	if err := d.Enqueue([]Registration{{Config: cfg, Generation: "invalid"}},
		statusResponse(protocol.TaskStateWorking)); err != nil {
		t.Fatal(err)
	}
	if err := d.Enqueue([]Registration{{Config: cfg, Generation: "valid"}},
		statusResponse(protocol.TaskStateCompleted)); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-delivered:
		if got != protocol.TaskStateCompleted {
			t.Fatalf("delivered state = %s, want completed", got)
		}
	case <-time.After(time.Second):
		t.Fatal("valid delivery did not run after validation error")
	}
	select {
	case got := <-delivered:
		t.Fatalf("validation error must fail closed; unexpected delivery %s", got)
	case <-time.After(50 * time.Millisecond):
	}
}
