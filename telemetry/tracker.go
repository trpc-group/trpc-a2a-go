// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 THL A29 Limited, a Tencent company.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package telemetry

import "github.com/mikeboe/trpc-a2a-go/protocol"

// FirstTokenMatcher determines whether a TaskEvent should be considered
// as the "first token" for TTFT measurement.
// isStreaming indicates whether the current request is a streaming (SSE) request.
type FirstTokenMatcher func(event protocol.TaskEvent, isStreaming bool) bool

// DefaultFirstTokenMatcher provides the default first-token detection logic:
//   - Streaming: the first TaskStatusUpdateEvent with state=Working that carries a message,
//     or the first TaskArtifactUpdateEvent.
//   - Non-streaming: always returns true (the response itself is the first token).
func DefaultFirstTokenMatcher(event protocol.TaskEvent, isStreaming bool) bool {
	if !isStreaming {
		return true
	}
	switch e := event.(type) {
	case protocol.TaskStatusUpdateEvent:
		return e.Status.State == protocol.TaskStateWorking && e.Status.Message != nil
	case protocol.TaskArtifactUpdateEvent:
		return true
	default:
		return false
	}
}
