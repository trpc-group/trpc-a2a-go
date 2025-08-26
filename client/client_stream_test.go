package client

import (
    "bytes"
    "context"
    "io"
    "net/http"
    "testing"
    "time"

    "trpc.group/trpc-go/trpc-a2a-go/protocol"
)

type fakeSSEHandler struct{}

func (h *fakeSSEHandler) Handle(
    _ context.Context,
    _ *http.Client,
    _ *http.Request,
) (*http.Response, error) {
    resp := &http.Response{
        StatusCode: http.StatusOK,
        Header:     make(http.Header),
        Body:       io.NopCloser(bytes.NewReader(nil)),
    }
    resp.Header.Set("Content-Type", "text/event-stream")
    return resp, nil
}

func TestStreamMessage_ChannelCapacity_Default(t *testing.T) {
    const (
        baseURL         = "http://127.0.0.1:18081/"
        wantChannelSize = 1024
    )
    c, err := NewA2AClient(
        baseURL,
        WithHTTPReqHandler(&fakeSSEHandler{}),
        WithTimeout(500*time.Millisecond),
    )
    if err != nil {
        t.Fatalf("NewA2AClient error: %v", err)
    }

    ch, err := c.StreamMessage(context.Background(), protocol.SendMessageParams{})
    if err != nil {
        t.Fatalf("StreamMessage error: %v", err)
    }
    if cap(ch) != wantChannelSize {
        t.Fatalf("unexpected channel cap, got %d, want %d", cap(ch),
            wantChannelSize)
    }

    // Drain and ensure channel closes quickly due to EOF.
    select {
    case _, ok := <-ch:
        _ = ok
    case <-time.After(100 * time.Millisecond):
    }
}

func TestStreamMessage_ChannelCapacity_Custom(t *testing.T) {
    const (
        baseURL       = "http://127.0.0.1:18082/"
        customBufSize = 8
    )
    c, err := NewA2AClient(
        baseURL,
        WithChannelSize(customBufSize),
        WithHTTPReqHandler(&fakeSSEHandler{}),
    )
    if err != nil {
        t.Fatalf("NewA2AClient error: %v", err)
    }

    ch, err := c.StreamMessage(context.Background(), protocol.SendMessageParams{})
    if err != nil {
        t.Fatalf("StreamMessage error: %v", err)
    }
    if cap(ch) != customBufSize {
        t.Fatalf("unexpected channel cap, got %d, want %d", cap(ch),
            customBufSize)
    }
}

func TestResubscribeTask_ChannelCapacity_Custom(t *testing.T) {
    const (
        baseURL       = "http://127.0.0.1:18083/"
        customBufSize = 16
    )
    c, err := NewA2AClient(
        baseURL,
        WithChannelSize(customBufSize),
        WithHTTPReqHandler(&fakeSSEHandler{}),
    )
    if err != nil {
        t.Fatalf("NewA2AClient error: %v", err)
    }

    ch, err := c.ResubscribeTask(context.Background(), protocol.TaskIDParams{})
    if err != nil {
        t.Fatalf("ResubscribeTask error: %v", err)
    }
    if cap(ch) != customBufSize {
        t.Fatalf("unexpected channel cap, got %d, want %d", cap(ch),
            customBufSize)
    }
}


