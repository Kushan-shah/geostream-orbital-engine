// Copyright 2026 Kushan Shah
// SPDX-License-Identifier: Apache-2.0

package worker

import (
	"errors"
	"sync"
	"time"
)

// CircuitState represents the three states of a circuit breaker.
type CircuitState int

const (
	StateClosed   CircuitState = iota // Normal operation — requests flow through
	StateOpen                         // Failure threshold reached — requests blocked
	StateHalfOpen                     // Cooldown expired — testing with single request
)

var ErrCircuitOpen = errors.New("circuit breaker is open: service unavailable")

// CircuitBreaker implements the Circuit Breaker pattern to prevent
// cascading failures when the FastAPI renderer service is down.
//
// State Machine:
//   CLOSED → (N consecutive failures) → OPEN
//   OPEN   → (cooldown expires)       → HALF_OPEN
//   HALF_OPEN → (1 success)           → CLOSED
//   HALF_OPEN → (1 failure)           → OPEN
type CircuitBreaker struct {
	mu               sync.RWMutex
	state            CircuitState
	failureCount     int
	failureThreshold int           // failures before tripping to OPEN
	cooldownPeriod   time.Duration // how long to stay in OPEN before trying HALF_OPEN
	lastFailureTime  time.Time
}

// NewCircuitBreaker creates a circuit breaker with configurable thresholds.
// Recommended: threshold=3, cooldown=30s for microservice communication.
func NewCircuitBreaker(failureThreshold int, cooldownPeriod time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		state:            StateClosed,
		failureThreshold: failureThreshold,
		cooldownPeriod:   cooldownPeriod,
	}
}

// Allow checks if a request should be permitted through the circuit breaker.
// Returns nil if allowed, ErrCircuitOpen if blocked.
func (cb *CircuitBreaker) Allow() error {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	switch cb.state {
	case StateClosed:
		return nil
	case StateOpen:
		// Check if cooldown period has elapsed → transition to HALF_OPEN
		if time.Since(cb.lastFailureTime) > cb.cooldownPeriod {
			// Upgrade to write lock for state transition
			cb.mu.RUnlock()
			cb.mu.Lock()
			cb.state = StateHalfOpen
			cb.mu.Unlock()
			cb.mu.RLock()
			return nil
		}
		return ErrCircuitOpen
	case StateHalfOpen:
		return nil // Allow single test request
	}

	return nil
}

// RecordSuccess records a successful request. Resets the circuit to CLOSED.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount = 0
	cb.state = StateClosed
}

// RecordFailure records a failed request. May trip the circuit to OPEN.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount++
	cb.lastFailureTime = time.Now()

	if cb.state == StateHalfOpen || cb.failureCount >= cb.failureThreshold {
		cb.state = StateOpen
	}
}

// State returns the current circuit state (for telemetry/logging).
func (cb *CircuitBreaker) State() string {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	switch cb.state {
	case StateClosed:
		return "CLOSED"
	case StateOpen:
		return "OPEN"
	case StateHalfOpen:
		return "HALF_OPEN"
	}
	return "UNKNOWN"
}
