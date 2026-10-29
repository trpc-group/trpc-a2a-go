// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2026 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package client

import (
	"context"
	"io"
	"sync"
)

// cancelStreamBody owns one cancellation listener for the entire response,
// including error-body validation. The listener ends when the body is closed.
type cancelStreamBody struct {
	io.ReadCloser
	once sync.Once
	done chan struct{}
	err  error
}

func newCancelStreamBody(ctx context.Context, body io.ReadCloser) io.ReadCloser {
	b := &cancelStreamBody{ReadCloser: body, done: make(chan struct{})}
	if ctx.Err() != nil {
		b.Close()
	} else if ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				b.Close()
			case <-b.done:
			}
		}()
	}
	return b
}

func (b *cancelStreamBody) Close() error {
	b.once.Do(func() {
		close(b.done)
		b.err = b.ReadCloser.Close()
	})
	return b.err
}
