// Tencent is pleased to support the open source community by making trpc-a2a-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-a2a-go is licensed under the Apache License Version 2.0.

package retaining

import "context"

// Notifier provides best-effort, low-latency wakeups for Task event readers.
// Durability and ordering come from Store, not from Notifier.
//
// Notifications may be lost, duplicated, or delivered spuriously. Wait must
// honor ctx; the retaining Manager always supplies a bounded context and reads
// Store again after Wait returns. Close must be safe to call more than once.
type Notifier interface {
	// Notify announces that key may have events through cursor.
	Notify(ctx context.Context, key TaskKey, cursor Cursor) error

	// Wait blocks until key may have events after cursor or ctx ends.
	Wait(ctx context.Context, key TaskKey, cursor Cursor) error

	// Close releases resources owned by this Notifier wrapper.
	Close() error
}
