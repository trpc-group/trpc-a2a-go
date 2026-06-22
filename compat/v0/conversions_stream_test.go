// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package v0

import (
	"testing"

	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

func csTextMsg() Message { return NewMessage(MessageRoleAgent, []Part{NewTextPart("hi")}) }

func csArtifact() Artifact {
	return *NewArtifactWithID(nil, nil, []Part{NewTextPart("payload")})
}

func TestArtifactUpdateConversionRoundTrip(t *testing.T) {
	if got := ToV1ArtifactUpdate(nil); got != nil {
		t.Errorf("ToV1ArtifactUpdate(nil) = %v, want nil", got)
	}
	if got := FromV1ArtifactUpdate(nil); got != nil {
		t.Errorf("FromV1ArtifactUpdate(nil) = %v, want nil", got)
	}

	last := true
	legacy := &TaskArtifactUpdateEvent{
		TaskID:    "task-1",
		ContextID: "ctx-1",
		Kind:      KindTaskArtifactUpdate,
		Artifact:  csArtifact(),
		LastChunk: &last,
	}
	v1 := ToV1ArtifactUpdate(legacy)
	if v1.TaskID != "task-1" || v1.ContextID != "ctx-1" || v1.LastChunk == nil || !*v1.LastChunk {
		t.Fatalf("ToV1ArtifactUpdate wrong: %+v", v1)
	}
	if v1.Artifact.ArtifactID != legacy.Artifact.ArtifactID || len(v1.Artifact.Parts) != 1 {
		t.Errorf("artifact not converted: %+v", v1.Artifact)
	}

	back := FromV1ArtifactUpdate(v1)
	if back.TaskID != "task-1" || back.Kind != KindTaskArtifactUpdate ||
		back.Artifact.ArtifactID != legacy.Artifact.ArtifactID || back.LastChunk == nil || !*back.LastChunk {
		t.Errorf("round-trip lost data: %+v", back)
	}
}

func TestToV1StreamResponse_AllBranches(t *testing.T) {
	msg := csTextMsg()
	task := NewTask("t", "c")
	statusEvt := NewTaskStatusUpdateEvent("t", "c", TaskStatus{State: TaskStateWorking}, false)
	artEvt := &TaskArtifactUpdateEvent{TaskID: "t", Artifact: csArtifact()}

	if r, err := ToV1StreamResponse(StreamingMessageEvent{Result: &msg}); err != nil || r.Message == nil {
		t.Errorf("message branch: r=%+v err=%v", r, err)
	}
	if r, err := ToV1StreamResponse(StreamingMessageEvent{Result: task}); err != nil || r.Task == nil {
		t.Errorf("task branch: r=%+v err=%v", r, err)
	}
	if r, err := ToV1StreamResponse(StreamingMessageEvent{Result: &statusEvt}); err != nil || r.StatusUpdate == nil {
		t.Errorf("status-update branch: r=%+v err=%v", r, err)
	}
	if r, err := ToV1StreamResponse(StreamingMessageEvent{Result: artEvt}); err != nil || r.ArtifactUpdate == nil {
		t.Errorf("artifact-update branch: r=%+v err=%v", r, err)
	}
	// nil/unknown Result -> error.
	if _, err := ToV1StreamResponse(StreamingMessageEvent{}); err == nil {
		t.Error("empty StreamingMessageEvent should error")
	}
}

func TestFromV1StreamResponse_AllBranches(t *testing.T) {
	msg := csTextMsg()
	task := NewTask("t", "c")
	statusEvt := NewTaskStatusUpdateEvent("t", "c", TaskStatus{State: TaskStateCompleted}, true)
	artEvt := &TaskArtifactUpdateEvent{TaskID: "t", Artifact: csArtifact()}

	cases := []struct {
		name string
		in   protocol.StreamResponse
		want interface{} // expected concrete legacy type
	}{
		{"message", protocol.StreamResponse{Message: ToV1Message(&msg)}, &Message{}},
		{"task", protocol.StreamResponse{Task: ToV1Task(task)}, &Task{}},
		{"status", protocol.StreamResponse{StatusUpdate: ToV1StatusUpdate(&statusEvt)}, &TaskStatusUpdateEvent{}},
		{"artifact", protocol.StreamResponse{ArtifactUpdate: ToV1ArtifactUpdate(artEvt)}, &TaskArtifactUpdateEvent{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := FromV1StreamResponse(tc.in)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			switch tc.want.(type) {
			case *Message:
				if _, ok := ev.Result.(*Message); !ok {
					t.Errorf("got %T, want *Message", ev.Result)
				}
			case *Task:
				if _, ok := ev.Result.(*Task); !ok {
					t.Errorf("got %T, want *Task", ev.Result)
				}
			case *TaskStatusUpdateEvent:
				if _, ok := ev.Result.(*TaskStatusUpdateEvent); !ok {
					t.Errorf("got %T, want *TaskStatusUpdateEvent", ev.Result)
				}
			case *TaskArtifactUpdateEvent:
				if _, ok := ev.Result.(*TaskArtifactUpdateEvent); !ok {
					t.Errorf("got %T, want *TaskArtifactUpdateEvent", ev.Result)
				}
			}
		})
	}
	if _, err := FromV1StreamResponse(protocol.StreamResponse{}); err == nil {
		t.Error("empty v1 StreamResponse should error")
	}
}

func TestSendMessageResponseConversion(t *testing.T) {
	msg := csTextMsg()
	task := NewTask("t", "c")

	if r, err := ToV1SendMessageResponse(nil); err != nil || r != nil {
		t.Errorf("ToV1SendMessageResponse(nil) = (%v,%v)", r, err)
	}
	if r, err := ToV1SendMessageResponse(&MessageResult{Result: &msg}); err != nil || r.Message == nil {
		t.Errorf("message branch: r=%+v err=%v", r, err)
	}
	if r, err := ToV1SendMessageResponse(&MessageResult{Result: task}); err != nil || r.Task == nil {
		t.Errorf("task branch: r=%+v err=%v", r, err)
	}
	if _, err := ToV1SendMessageResponse(&MessageResult{}); err == nil {
		t.Error("empty MessageResult should error")
	}

	if r, err := FromV1SendMessageResponse(nil); err != nil || r != nil {
		t.Errorf("FromV1SendMessageResponse(nil) = (%v,%v)", r, err)
	}
	if r, err := FromV1SendMessageResponse(&protocol.SendMessageResponse{Message: ToV1Message(&msg)}); err != nil {
		t.Errorf("message branch err: %v", err)
	} else if _, ok := r.Result.(*Message); !ok {
		t.Errorf("got %T, want *Message", r.Result)
	}
	if r, err := FromV1SendMessageResponse(&protocol.SendMessageResponse{Task: ToV1Task(task)}); err != nil {
		t.Errorf("task branch err: %v", err)
	} else if _, ok := r.Result.(*Task); !ok {
		t.Errorf("got %T, want *Task", r.Result)
	}
	if _, err := FromV1SendMessageResponse(&protocol.SendMessageResponse{}); err == nil {
		t.Error("empty v1 SendMessageResponse should error")
	}
}
