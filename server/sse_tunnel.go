package server

import (
	"context"
	"io"
	"net/http"
	"time"

	"trpc.group/trpc-go/trpc-a2a-go/v2/internal/sse"
	"trpc.group/trpc-go/trpc-a2a-go/v2/log"
	"trpc.group/trpc-go/trpc-a2a-go/v2/protocol"
)

const (
	defaultSSEBatchSize     = 5
	defaultSSEFlushInterval = 50 * time.Millisecond
)

// sseTunnel optimizes SSE streaming by batching events before sending
type sseTunnel struct {
	w             http.ResponseWriter
	flusher       http.Flusher
	rpcID         interface{}
	batchSize     int
	flushInterval time.Duration
	batch         []protocol.StreamResponse
	formatBatch   func(io.Writer, []sse.EventBatch) error
}

// newSSETunnel creates a new SSE tunnel with default settings
func newSSETunnel(
	w http.ResponseWriter,
	flusher http.Flusher,
	rpcID interface{},
	formatBatch func(io.Writer, []sse.EventBatch) error,
) *sseTunnel {
	return &sseTunnel{
		w:             w,
		flusher:       flusher,
		rpcID:         rpcID,
		batchSize:     defaultSSEBatchSize,
		flushInterval: defaultSSEFlushInterval,
		batch:         make([]protocol.StreamResponse, 0, defaultSSEBatchSize),
		formatBatch:   formatBatch,
	}
}

// start runs the SSE tunnel with event batching optimization
func (t *sseTunnel) start(ctx context.Context, eventsChan <-chan protocol.StreamResponse, clientClosed <-chan struct{}) {
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
					drainToClose(eventsChan) // Write error: abandon delivery, keep draining.
					return
				}
			}

		case <-ticker.C:
			// Periodic flush for any accumulated events
			if len(t.batch) > 0 {
				if !t.flushBatch() {
					drainToClose(eventsChan) // Write error: abandon delivery, keep draining.
					return
				}
			}

		case <-clientClosed:
			// Client disconnected
			log.Infof("SSE client disconnected for request ID: %s. Closing stream.", t.rpcID)
			drainToClose(eventsChan)
			return
		}
	}
}

// drainToClose keeps consuming the task-manager event channel until it closes,
// discarding the events. The taskmanager pipe must always be drained to
// closure: with a blocking-send manager the drain engine blocks on a full
// pipe, so abandoning it on a client disconnect or write error would wedge the
// execution forever. The goroutine ends when the manager closes the channel —
// at end of round for the request pipe, or when the manager unsubscribes the
// disconnected client (request ctx) for resubscribe streams — so it never
// leaks.
func drainToClose(eventsChan <-chan protocol.StreamResponse) {
	go func() {
		for range eventsChan {
		}
	}()
}

// flushBatch sends all events in the current batch as a single write operation
func (t *sseTunnel) flushBatch() bool {
	if len(t.batch) == 0 {
		return true
	}

	// Convert to batch format for efficient processing
	events := make([]sse.EventBatch, 0, len(t.batch))

	// Process all events in the batch
	for i := range t.batch {
		eventType := t.batch[i].EventType()
		if eventType == "" {
			log.Warnf("Unknown event type received for request ID: %s. Skipping.", t.rpcID)
			continue
		}

		// Add to batch events
		events = append(events, sse.EventBatch{
			EventType: eventType,
			ID:        t.rpcID,
			Data:      &t.batch[i],
		})
	}

	// Write the entire batch using optimized batch function
	if err := t.formatBatch(t.w, events); err != nil {
		log.Errorf("Error writing SSE batch for request ID: %s (client likely disconnected): %v", t.rpcID, err)
		return false
	}

	// Clear the batch and flush to client
	t.batch = t.batch[:0]
	t.flusher.Flush()

	return true
}
