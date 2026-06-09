// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

// Package telemetry defines telemetry customization contracts shared by A2A components.
package telemetry

import "trpc.group/trpc-go/trpc-a2a-go/protocol"

// FirstTokenPolicy defines TTFT first-token detection for both request modes.
type FirstTokenPolicy interface {
	// IsStreamingFirstToken reports whether a streaming event should be treated
	// as the first token.
	IsStreamingFirstToken(event *protocol.StreamResponse) bool
	// IsNonStreamingFirstToken reports whether a non-streaming response should be
	// treated as the first token.
	IsNonStreamingFirstToken(result *protocol.SendMessageResponse) bool
}

type defaultFirstTokenPolicy struct{}

// IsStreamingFirstToken implements the default streaming first-token logic.
func (defaultFirstTokenPolicy) IsStreamingFirstToken(event *protocol.StreamResponse) bool {
	if event == nil {
		return false
	}
	switch {
	case event.StatusUpdate != nil:
		return event.StatusUpdate.Status.State == protocol.TaskStateWorking && event.StatusUpdate.Status.Message != nil
	case event.ArtifactUpdate != nil:
		return true
	default:
		return false
	}
}

// IsNonStreamingFirstToken implements the default non-streaming first-token logic.
func (defaultFirstTokenPolicy) IsNonStreamingFirstToken(_ *protocol.SendMessageResponse) bool {
	return true
}

// DefaultFirstTokenPolicy provides the default TTFT first-token policy.
var DefaultFirstTokenPolicy FirstTokenPolicy = defaultFirstTokenPolicy{}

// FirstTokenMatcher determines whether an event should be considered as the
// first token. This legacy function style remains for backward compatibility.
//
// Deprecated: use FirstTokenPolicy instead.
type FirstTokenMatcher func(event *protocol.StreamResponse, isStreaming bool) bool

type firstTokenMatcherPolicy struct {
	matcher FirstTokenMatcher
}

func (p firstTokenMatcherPolicy) IsStreamingFirstToken(event *protocol.StreamResponse) bool {
	return p.matcher(event, true)
}

func (p firstTokenMatcherPolicy) IsNonStreamingFirstToken(_ *protocol.SendMessageResponse) bool {
	// Keep backward compatibility: legacy matchers were only used for streaming.
	return true
}

// NewFirstTokenPolicyFromMatcher adapts a legacy matcher into a policy.
// It returns nil when matcher is nil.
func NewFirstTokenPolicyFromMatcher(matcher FirstTokenMatcher) FirstTokenPolicy {
	if matcher == nil {
		return nil
	}
	return firstTokenMatcherPolicy{matcher: matcher}
}

// DefaultFirstTokenMatcher provides the default first-token detection logic:
//   - Streaming: the first TaskStatusUpdateEvent with state=Working that carries a message,
//     or the first TaskArtifactUpdateEvent.
//   - Non-streaming: always returns true (the response itself is the first token).
func DefaultFirstTokenMatcher(event *protocol.StreamResponse, isStreaming bool) bool {
	if isStreaming {
		return DefaultFirstTokenPolicy.IsStreamingFirstToken(event)
	}
	return DefaultFirstTokenPolicy.IsNonStreamingFirstToken(nil)
}
