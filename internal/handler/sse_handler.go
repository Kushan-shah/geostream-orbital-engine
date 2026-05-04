// Copyright 2026 Kushan Shah
// SPDX-License-Identifier: Apache-2.0

package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

type SSEHandler struct {
	db *pgxpool.Pool
}

func NewSSEHandler(db *pgxpool.Pool) *SSEHandler {
	return &SSEHandler{db: db}
}

// SSEProgressPayload is the structured SSE event payload.
type SSEProgressPayload struct {
	Status    string  `json:"status"`
	Processed int     `json:"processed"`
	Total     int     `json:"total"`
	VideoURL  *string `json:"video_url,omitempty"`
}

// StreamProgress godoc
// @Summary Stream job progress
// @Description Server-Sent Events (SSE) endpoint providing real-time progress updates for a specific job
// @Tags Jobs
// @Security ApiKeyAuth
// @Produce text/event-stream
// @Param job_id query string true "Job UUID"
// @Success 200 {string} string "SSE Event Stream"
// @Failure 400 {string} string "Invalid job_id"
// @Failure 500 {string} string "Streaming unsupported"
// @Router /jobs/progress/stream [get]
func (h *SSEHandler) StreamProgress(w http.ResponseWriter, r *http.Request) {
	jobIDStr := r.URL.Query().Get("job_id")
	jobID, err := uuid.Parse(jobIDStr)
	if err != nil {
		http.Error(w, "Invalid job_id", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Nginx reverse proxy compat
	// Flush headers to establish SSE stream
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}
	flusher.Flush()

	ctx := r.Context()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info().Str("job_id", jobID.String()).Msg("Client disconnected from SSE")
			return
		case <-ticker.C:
			var status string
			var processed, total int
			var videoURL *string

			query := `SELECT status, processed_frames, frame_count, video_url FROM jobs WHERE id = $1`
			err := h.db.QueryRow(ctx, query, jobID).Scan(&status, &processed, &total, &videoURL)
			
			if err != nil {
				log.Error().Err(err).Msg("Failed to fetch job progress")
				continue
			}

			// Use encoding/json for proper escaping (prevents JSON injection via video_url)
			payload := SSEProgressPayload{
				Status:    status,
				Processed: processed,
				Total:     total,
				VideoURL:  videoURL,
			}
			jsonBytes, _ := json.Marshal(payload)

			fmt.Fprintf(w, "data: %s\n\n", jsonBytes)
			flusher.Flush()

			if status == "COMPLETED" || status == "FAILED" || status == "CANCELLED" {
				return // End stream gracefully
			}
		}
	}
}
