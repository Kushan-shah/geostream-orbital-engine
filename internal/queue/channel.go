// Copyright 2026 Kushan Shah
// SPDX-License-Identifier: Apache-2.0

package queue

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// ChannelQueue wraps Go's native buffered channel as a JobQueue implementation.
// This is the default (zero-dependency) queue used when RABBITMQ_URL is not set.
// Behavior is identical to the original Pipeline's jobChan.
type ChannelQueue struct {
	ch chan uuid.UUID
}

// NewChannelQueue creates a bounded in-memory queue backed by a Go buffered channel.
// queueSize controls the maximum number of pending jobs before backpressure kicks in.
func NewChannelQueue(queueSize int) *ChannelQueue {
	return &ChannelQueue{
		ch: make(chan uuid.UUID, queueSize),
	}
}

// Publish enqueues a job ID. Non-blocking: returns error immediately if channel is full.
func (q *ChannelQueue) Publish(_ context.Context, jobID uuid.UUID) error {
	select {
	case q.ch <- jobID:
		return nil
	default:
		return fmt.Errorf("queue is full (backpressure applied)")
	}
}

// Consume returns the underlying channel for worker goroutines to range over.
func (q *ChannelQueue) Consume(_ context.Context) (<-chan uuid.UUID, error) {
	return q.ch, nil
}

// Depth returns the current number of jobs waiting in the channel.
func (q *ChannelQueue) Depth() int {
	return len(q.ch)
}

// Close closes the underlying channel, signaling workers to drain and exit.
func (q *ChannelQueue) Close() error {
	close(q.ch)
	return nil
}
