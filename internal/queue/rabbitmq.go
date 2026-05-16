// Copyright 2026 Kushan Shah
// SPDX-License-Identifier: Apache-2.0

package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/rs/zerolog/log"
)

const queueName = "geostream.jobs"

// RabbitMQQueue implements JobQueue using AMQP (RabbitMQ).
// Provides durable, persistent message delivery with manual acknowledgment.
// If RabbitMQ is unavailable at startup, the system falls back to ChannelQueue.
type RabbitMQQueue struct {
	conn    *amqp.Connection
	pubCh   *amqp.Channel
	consCh  *amqp.Channel
	jobChan chan uuid.UUID
	depth   int64
}

// JobMessage is the AMQP message payload.
type JobMessage struct {
	JobID string `json:"job_id"`
}

// NewRabbitMQQueue connects to RabbitMQ and declares the job queue.
// Returns an error if the connection fails (caller should fall back to ChannelQueue).
func NewRabbitMQQueue(amqpURL string, bufferSize int) (*RabbitMQQueue, error) {
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		return nil, fmt.Errorf("RabbitMQ connection failed: %w", err)
	}

	// Separate channels for publishing and consuming (AMQP best practice)
	pubCh, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("RabbitMQ publish channel failed: %w", err)
	}

	consCh, err := conn.Channel()
	if err != nil {
		pubCh.Close()
		conn.Close()
		return nil, fmt.Errorf("RabbitMQ consume channel failed: %w", err)
	}

	// Declare a durable queue (survives RabbitMQ restarts)
	_, err = pubCh.QueueDeclare(
		queueName,
		true,  // durable
		false, // auto-delete
		false, // exclusive
		false, // no-wait
		amqp.Table{
			"x-max-length": int32(bufferSize), // bounded queue (backpressure)
		},
	)
	if err != nil {
		consCh.Close()
		pubCh.Close()
		conn.Close()
		return nil, fmt.Errorf("RabbitMQ queue declare failed: %w", err)
	}

	// Prefetch = 1: each worker gets one job at a time (fair dispatch)
	if err := consCh.Qos(1, 0, false); err != nil {
		consCh.Close()
		pubCh.Close()
		conn.Close()
		return nil, fmt.Errorf("RabbitMQ QoS failed: %w", err)
	}

	log.Info().Str("queue", queueName).Msg("RabbitMQ queue connected and declared")

	return &RabbitMQQueue{
		conn:    conn,
		pubCh:   pubCh,
		consCh:  consCh,
		jobChan: make(chan uuid.UUID, bufferSize),
	}, nil
}

// Publish sends a job ID to the RabbitMQ queue with persistent delivery.
func (q *RabbitMQQueue) Publish(ctx context.Context, jobID uuid.UUID) error {
	msg := JobMessage{JobID: jobID.String()}
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal job message: %w", err)
	}

	err = q.pubCh.PublishWithContext(ctx,
		"",        // default exchange
		queueName, // routing key = queue name
		false,     // mandatory
		false,     // immediate
		amqp.Publishing{
			DeliveryMode: amqp.Persistent, // survives broker restart
			ContentType:  "application/json",
			Body:         body,
		},
	)
	if err != nil {
		return fmt.Errorf("RabbitMQ publish failed: %w", err)
	}

	atomic.AddInt64(&q.depth, 1)
	log.Debug().Str("job_id", jobID.String()).Msg("Job published to RabbitMQ")
	return nil
}

// Consume starts consuming messages from RabbitMQ and forwards them to a Go channel.
// Workers read from the returned channel identically to how they read from ChannelQueue.
func (q *RabbitMQQueue) Consume(ctx context.Context) (<-chan uuid.UUID, error) {
	deliveries, err := q.consCh.Consume(
		queueName,
		"",    // consumer tag (auto-generated)
		false, // auto-ack = false (manual acknowledgment after processing)
		false, // exclusive
		false, // no-local
		false, // no-wait
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("RabbitMQ consume failed: %w", err)
	}

	// Bridge: AMQP deliveries → Go channel (so pipeline workers see no difference)
	go func() {
		for {
			select {
			case <-ctx.Done():
				close(q.jobChan)
				return
			case d, ok := <-deliveries:
				if !ok {
					close(q.jobChan)
					return
				}

				var msg JobMessage
				if err := json.Unmarshal(d.Body, &msg); err != nil {
					log.Error().Err(err).Msg("Failed to unmarshal RabbitMQ message")
					d.Nack(false, false) // dead-letter
					continue
				}

				jobID, err := uuid.Parse(msg.JobID)
				if err != nil {
					log.Error().Err(err).Str("raw", msg.JobID).Msg("Invalid job ID in message")
					d.Nack(false, false)
					continue
				}

				q.jobChan <- jobID
				d.Ack(false) // acknowledge after forwarding
				atomic.AddInt64(&q.depth, -1)
			}
		}
	}()

	log.Info().Str("queue", queueName).Msg("RabbitMQ consumer started")
	return q.jobChan, nil
}

// Depth returns an approximate count of pending messages.
func (q *RabbitMQQueue) Depth() int {
	d := atomic.LoadInt64(&q.depth)
	if d < 0 {
		return 0
	}
	return int(d)
}

// Close gracefully shuts down the RabbitMQ connection.
func (q *RabbitMQQueue) Close() error {
	if q.consCh != nil {
		q.consCh.Close()
	}
	if q.pubCh != nil {
		q.pubCh.Close()
	}
	if q.conn != nil {
		return q.conn.Close()
	}
	return nil
}
