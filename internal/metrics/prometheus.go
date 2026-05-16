// Copyright 2026 Kushan Shah
// SPDX-License-Identifier: Apache-2.0

// Package metrics provides Prometheus-compatible telemetry for the GeoStream pipeline.
// Exposes counters, gauges, and histograms at /metrics for Prometheus scraping.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// JobsTotal counts the total number of jobs processed, partitioned by outcome.
	JobsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "geostream_jobs_total",
		Help: "Total number of jobs processed, partitioned by status (completed, failed, cancelled)",
	}, []string{"status"})

	// JobDurationSeconds tracks the wall-clock time of job processing.
	JobDurationSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "geostream_job_duration_seconds",
		Help:    "Histogram of job processing duration in seconds",
		Buckets: []float64{5, 10, 30, 60, 120, 300, 600},
	})

	// QueueDepth tracks the current number of jobs waiting in the queue.
	QueueDepth = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "geostream_queue_depth",
		Help: "Current number of jobs waiting in the queue",
	})

	// ActiveWorkers tracks the number of workers currently processing a job.
	ActiveWorkers = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "geostream_active_workers",
		Help: "Number of workers currently processing a job",
	})

	// RendererCircuitState exposes the circuit breaker state (0=closed, 1=open, 2=half-open).
	RendererCircuitState = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "geostream_renderer_circuit_state",
		Help: "Circuit breaker state: 0=closed, 1=open, 2=half-open",
	})

	// CacheHits counts cache hits partitioned by tier (redis, disk).
	CacheHits = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "geostream_cache_hits_total",
		Help: "Total cache hits partitioned by tier (redis, disk)",
	}, []string{"tier"})

	// CacheMisses counts cache misses (network fetches required).
	CacheMisses = promauto.NewCounter(prometheus.CounterOpts{
		Name: "geostream_cache_misses_total",
		Help: "Total cache misses requiring network fetch",
	})

	// HTTPRequestDuration tracks HTTP request latency for the Go API.
	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "geostream_http_request_duration_seconds",
		Help:    "HTTP request duration in seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path", "status"})
)
