// Copyright 2026 Kushan Shah
// SPDX-License-Identifier: Apache-2.0

// Package queue provides a pluggable job dispatch abstraction.
// Implementations: ChannelQueue (in-memory Go channel) and RabbitMQQueue (AMQP).
// The active implementation is selected at startup via the RABBITMQ_URL env var.
package queue

import (
	"context"

	"github.com/google/uuid"
)

// JobQueue defines the contract for job dispatch mechanisms.
// This interface allows swapping between in-memory channels and
// persistent message brokers without changing the pipeline logic.
type JobQueue interface {
	// Publish sends a job ID to the queue. Returns error if queue is full (backpressure).
	Publish(ctx context.Context, jobID uuid.UUID) error

	// Consume returns a channel that emits job IDs for worker consumption.
	Consume(ctx context.Context) (<-chan uuid.UUID, error)

	// Depth returns the current number of pending jobs in the queue.
	Depth() int

	// Close gracefully shuts down the queue connection.
	Close() error
}
