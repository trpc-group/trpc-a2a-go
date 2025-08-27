package server

import (
	"context"
	"net/http"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/internal/sse"
	"trpc.group/trpc-go/trpc-a2a-go/log"
	"trpc.group/trpc-go/trpc-a2a-go/protocol"
)

const (
	defaultSSEBatchSize     = 20
	defaultSSEFlushInterval = 1 * time.Second
)

// sseTunnel optimizes SSE streaming by batching events before sending
type sseTunnel struct {
	w             http.ResponseWriter
	flusher       http.Flusher
	rpcID         string
	batchSize     int
	flushInterval time.Duration
	batch         []protocol.StreamingMessageEvent
}

// newSSETunnel creates a new SSE tunnel with default settings
func newSSETunnel(w http.ResponseWriter, flusher http.Flusher, rpcID string) *sseTunnel {
	return &sseTunnel{
		w:             w,
		flusher:       flusher,
		rpcID:         rpcID,
		batchSize:     defaultSSEBatchSize,
		flushInterval: defaultSSEFlushInterval,
		batch:         make([]protocol.StreamingMessageEvent, 0, defaultSSEBatchSize),
	}
}

// start runs the SSE tunnel with event batching optimization
func (t *sseTunnel) start(ctx context.Context, eventsChan <-chan protocol.StreamingMessageEvent, clientClosed <-chan struct{}) {
	ticker := time.NewTicker(t.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case event, ok := <-eventsChan:
			if !ok {
				// Channel closed, flush any remaining events and exit
				if len(t.batch) > 0 {
					t.flushBatch()
				}
				return
			}

			// Add event to batch
			t.batch = append(t.batch, event)

			// Flush if batch is full
			if len(t.batch) >= t.batchSize {
				ticker.Reset(t.flushInterval)
				if !t.flushBatch() {
					return // Error occurred, exit
				}
			}

		case <-ticker.C:
			// Periodic flush for any accumulated events
			if len(t.batch) > 0 {
				if !t.flushBatch() {
					return // Error occurred, exit
				}
			}

		case <-clientClosed:
			// Client disconnected
			log.Infof("SSE client disconnected for request ID: %s. Closing stream.", t.rpcID)
			return
		}
	}
}

// flushBatch sends all events in the current batch as a single write operation
func (t *sseTunnel) flushBatch() bool {
	if len(t.batch) == 0 {
		return true
	}

	// Convert to batch format for efficient processing
	events := make([]sse.EventBatch, 0, len(t.batch))

	// Process all events in the batch
	for _, event := range t.batch {
		eventType, err := t.getEventType(&event)
		if err != nil {
			if err == errUnknownEvent {
				log.Warnf("Unknown event type received for request ID: %s: %T. Skipping.", t.rpcID, event)
				continue
			}
			log.Errorf("Error determining event type for request ID: %s: %v", t.rpcID, err)
			return false
		}

		// Add to batch events
		events = append(events, sse.EventBatch{
			EventType: eventType,
			ID:        t.rpcID,
			Data:      &event,
		})
	}

	// Write the entire batch using optimized batch function
	if err := sse.FormatJSONRPCEventBatch(t.w, events); err != nil {
		log.Errorf("Error writing SSE batch for request ID: %s (client likely disconnected): %v", t.rpcID, err)
		return false
	}

	// Clear the batch and flush to client
	t.batch = t.batch[:0]
	t.flusher.Flush()

	return true
}

// getEventType determines the SSE event type from a StreamingMessageEvent
func (t *sseTunnel) getEventType(event *protocol.StreamingMessageEvent) (string, error) {
	actualEvent := event.Result

	switch actualEvent.(type) {
	case *protocol.TaskStatusUpdateEvent:
		return protocol.EventStatusUpdate, nil
	case *protocol.TaskArtifactUpdateEvent:
		return protocol.EventArtifactUpdate, nil
	case *protocol.Message:
		return protocol.EventMessage, nil
	case *protocol.Task:
		return protocol.EventTask, nil
	default:
		return "", errUnknownEvent
	}
}
