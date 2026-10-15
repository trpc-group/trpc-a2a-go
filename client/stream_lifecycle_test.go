// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2026 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package client_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "trpc.group/trpc-go/trpc-a2a-go/client"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

type blockedBody struct {
	entered, closed      chan struct{}
	enterOnce, closeOnce sync.Once
	calls                atomic.Int32
}

func newBlockedBody() *blockedBody {
	return &blockedBody{entered: make(chan struct{}), closed: make(chan struct{})}
}
func (b *blockedBody) Read(p []byte) (int, error) {
	b.enterOnce.Do(func() { close(b.entered) })
	<-b.closed
	return 0, errors.New("closed blocked body")
}
func (b *blockedBody) Close() error {
	b.calls.Add(1)
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

type syntheticHandler struct {
	body        io.ReadCloser
	status      int
	contentType string
}

func (h syntheticHandler) Handle(context.Context, *http.Client, *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: h.status, Header: http.Header{"Content-Type": {h.contentType}}, Body: h.body}, nil
}
func params() protocol.SendMessageParams {
	return protocol.SendMessageParams{Message: protocol.NewMessage(protocol.MessageRoleUser, []protocol.Part{protocol.NewTextPart("smoke")})}
}
func TestSDKBlockedBodyCancellation(t *testing.T) {
	for _, status := range []int{200, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			b := newBlockedBody()
			t.Cleanup(func() { b.Close() })
			c, e := sdk.NewA2AClient("http://unused", sdk.WithHTTPReqHandler(syntheticHandler{b, status, "text/event-stream"}))
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan struct{})
			go func() {
				events, e := c.StreamMessage(ctx, params())
				if status == 500 && !errors.Is(e, context.Canceled) {
					t.Errorf("error-body cancellation: %v", e)
				}
				if status == 200 && e != nil {
					t.Error(e)
				}
				if e == nil {
					for range events {
					}
				}
				close(finished)
			}()
			await(t, b.entered, "blocked SDK read")
			cancel()
			await(t, finished, "SDK cleanup")
			if b.calls.Load() != 1 {
				t.Errorf("underlying Close count=%d", b.calls.Load())
			}
		})
	}
}

type countedBody struct {
	io.Reader
	closes atomic.Int32
}

func (b *countedBody) Close() error { b.closes.Add(1); return nil }

const messageEvent = `data: {"kind":"message","messageId":"m1","role":"agent","parts":[{"kind":"text","text":"hello"}]}` + "\n\n"

func TestSDKNormalEOFAndBackpressure(t *testing.T) {
	for _, mode := range []string{"eof", "close", "invalid_then_valid", "backpressure", "wrong_type"} {
		t.Run(mode, func(t *testing.T) {
			body := messageEvent
			typ := "text/event-stream"
			switch mode {
			case "close":
				body += "event: close\ndata: done\n\n"
			case "invalid_then_valid":
				body = "data: {invalid}\n\n" + body
			case "backpressure":
				body = strings.Repeat(messageEvent, 1000)
			case "wrong_type":
				typ = "text/plain"
			}
			b := &countedBody{Reader: strings.NewReader(body)}
			c, e := sdk.NewA2AClient("http://unused", sdk.WithHTTPReqHandler(syntheticHandler{b, 200, typ}), sdk.WithChannelSize(1))
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			events, e := c.StreamMessage(ctx, params())
			if mode == "wrong_type" {
				if e == nil {
					t.Fatal("expected validation error")
				}
			} else {
				if e != nil {
					t.Fatal(e)
				}
				if mode == "backpressure" {
					deadline := time.Now().Add(limit)
					for len(events) < cap(events) && time.Now().Before(deadline) {
						runtime.Gosched()
					}
					if len(events) != cap(events) {
						t.Fatal("producer never filled event buffer")
					}
					cancel()
				}
				count := 0
				done := make(chan struct{})
				go func() {
					for range events {
						count++
					}
					close(done)
				}()
				await(t, done, "SSE completion")
				if mode != "backpressure" && count != 1 {
					t.Errorf("event count=%d", count)
				}
			}
			if b.closes.Load() != 1 {
				t.Errorf("body closed %d times", b.closes.Load())
			}
		})
	}
}

func TestSDKResubscribeBlockedBody(t *testing.T) {
	b := newBlockedBody()
	t.Cleanup(func() { b.Close() })
	c, e := sdk.NewA2AClient("http://unused", sdk.WithHTTPReqHandler(syntheticHandler{b, 200, "text/event-stream"}))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, e := c.ResubscribeTask(ctx, protocol.TaskIDParams{ID: "task"})
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	await(t, b.entered, "Resubscribe Read")
	cancel()
	await(t, done, "resubscribe cleanup")
}

const limit = 2 * time.Second

func await(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("timeout waiting for %s", what)
	}
}
