// Copyright 2026 Kushan Shah
// SPDX-License-Identifier: Apache-2.0

package handler

import (
	"encoding/json"
	"net/http"
	"time"
	"sync/atomic"

	"github.com/kushanj/geo-backend/internal/worker"
)

type HealthHandler struct {
	pipeline  *worker.Pipeline
	startTime time.Time
}

func NewHealthHandler(pipeline *worker.Pipeline) *HealthHandler {
	return &HealthHandler{
		pipeline:  pipeline,
		startTime: time.Now(),
	}
}

// Live godoc
// @Summary Liveness probe
// @Description Basic health check endpoint
// @Tags Telemetry
// @Produce plain
// @Success 200 {string} string "OK"
// @Router /health/live [get]
func (h *HealthHandler) Live(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// Metrics godoc
// @Summary System metrics
// @Description Exposes worker queue depth and total processed jobs for Prometheus/Grafana monitoring
// @Tags Telemetry
// @Produce json
// @Success 200 {object} map[string]interface{} "Metrics JSON"
// @Router /health/metrics [get]
func (h *HealthHandler) Metrics(w http.ResponseWriter, r *http.Request) {
	metrics := map[string]interface{}{
		"queue_depth":     h.pipeline.QueueDepth(),
		"uptime_seconds":  int(time.Since(h.startTime).Seconds()),
		"total_processed": atomic.LoadUint64(&h.pipeline.TotalProcessed),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}
