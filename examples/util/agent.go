// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package util provides a self-contained mock agent for the A2A examples.
//
// The Agent interface is intentionally minimal: Run executes an input and
// returns a channel of streaming events. NewMessageProcessor (adapter.go)
// bridges an Agent onto a taskmanager.MessageProcessor, so an example can drive
// the A2A server from an agent instead of a hand-written processor.
package util

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Event is one item in an agent's output stream: a piece of content, whether it
// is a partial (streaming) delta, and whether the run has finished.
type Event struct {
	// Content is the text carried by this event: a delta for a partial event,
	// or the full reply for the final (Done) event.
	Content string
	// Partial is true for intermediate streaming chunks.
	Partial bool
	// Done marks the final event of the run; its Content is the full reply.
	Done bool
	// Err carries a run failure. When set, it is the last event of the run.
	Err error
}

// Agent runs an input and reports progress as a stream of events. The returned
// channel is closed when the run ends; Run itself does not block on the work.
type Agent interface {
	Run(ctx context.Context, input string) (<-chan *Event, error)
}

// Handler produces an agent's reply for a given input as an ordered list of
// chunks. Returning several chunks lets the mock stream them as partial events
// before the final aggregated one, simulating a model emitting tokens.
type Handler func(input string) []string

// MockAgent is a canned Agent: it streams the chunks returned by its handler as
// partial events, then a final event carrying the full concatenated reply.
type MockAgent struct {
	handler Handler
	delay   time.Duration
}

// Option configures a MockAgent.
type Option func(*MockAgent)

// WithChunkDelay makes the mock pause between streamed chunks, simulating a
// real agent that produces output over time.
func WithChunkDelay(d time.Duration) Option {
	return func(a *MockAgent) { a.delay = d }
}

// NewMockAgent builds a mock agent from a Handler.
func NewMockAgent(handler Handler, opts ...Option) *MockAgent {
	a := &MockAgent{handler: handler}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Run implements Agent. It streams each handler chunk as a partial event, then
// a final event whose Content is the full concatenated reply. Sends respect
// ctx cancellation so a canceled run stops promptly.
func (a *MockAgent) Run(ctx context.Context, input string) (<-chan *Event, error) {
	if a.handler == nil {
		return nil, errors.New("mock agent: nil handler")
	}
	out := make(chan *Event)
	go func() {
		defer close(out)
		var full strings.Builder
		for _, chunk := range a.handler(input) {
			full.WriteString(chunk)
			if !a.send(ctx, out, &Event{Content: chunk, Partial: true}) {
				return
			}
			if a.delay > 0 {
				select {
				case <-ctx.Done():
					a.send(ctx, out, &Event{Err: ctx.Err()})
					return
				case <-time.After(a.delay):
				}
			}
		}
		a.send(ctx, out, &Event{Content: full.String(), Done: true})
	}()
	return out, nil
}

// send delivers ev on out unless ctx is canceled first, in which case it emits
// a terminal error event. It returns false when the caller should stop.
func (a *MockAgent) send(ctx context.Context, out chan<- *Event, ev *Event) bool {
	if ev.Err == nil {
		select {
		case <-ctx.Done():
			out <- &Event{Err: ctx.Err()}
			return false
		case out <- ev:
			return true
		}
	}
	out <- ev
	return false
}

// Chunk splits s into pieces of at most size bytes, useful for a Handler that
// wants to simulate streamed output. A size <= 0 returns s as a single chunk.
func Chunk(s string, size int) []string {
	if size <= 0 || len(s) <= size {
		return []string{s}
	}
	var chunks []string
	for len(s) > size {
		chunks = append(chunks, s[:size])
		s = s[size:]
	}
	if len(s) > 0 {
		chunks = append(chunks, s)
	}
	return chunks
}
