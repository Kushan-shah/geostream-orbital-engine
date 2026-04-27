// Copyright 2026 Kushan J
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"

	"github.com/google/uuid"
)

type JobStatus string

const (
	StatusPending    JobStatus = "PENDING"
	StatusProcessing JobStatus = "PROCESSING"
	StatusCompleted  JobStatus = "COMPLETED"
	StatusFailed     JobStatus = "FAILED"
	StatusCancelled  JobStatus = "CANCELLED"
)

type WMSLayer struct {
	URL     string  `json:"url"`
	Name    string  `json:"name"`
	Opacity float64 `json:"opacity"` // 0.0 to 1.0 (defaults to 1.0)
}

type Job struct {
	ID                  uuid.UUID  `json:"id"`
	UserID              uuid.UUID  `json:"user_id"`
	Status              JobStatus  `json:"status"`
	FrameCount          int        `json:"frame_count"`
	ProcessedFrames     int        `json:"processed_frames"`
	VideoURL            *string    `json:"video_url,omitempty"`
	ActivityMetrics     []float64  `json:"activity_metrics,omitempty"`
	RequestHash         string     `json:"request_hash"`
	RetryCount          int        `json:"retry_count"`
	WorkerID            *string    `json:"-"`
	LeaseExpiry         *time.Time `json:"-"`
	ProcessingStartedAt *time.Time `json:"processing_started_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
	CompletedAt         *time.Time `json:"completed_at,omitempty"`
	CancelledAt         *time.Time `json:"cancelled_at,omitempty"`
	ExpiresAt           time.Time  `json:"expires_at"`
	CancelRequested     bool       `json:"-"`
	ErrorMessage        *string    `json:"error_message,omitempty"`
	Version             int        `json:"-"`

	// Geospatial parameters
	Bbox      []float64  `json:"bbox,omitempty"`       // [minLng, minLat, maxLng, maxLat]
	StartDate *time.Time `json:"start_date,omitempty"`
	EndDate   *time.Time `json:"end_date,omitempty"`
	TimeStep       string     `json:"time_step,omitempty"`  // e.g., "1d", "1h", "10m"
	TrackSatellite string     `json:"track_satellite,omitempty"` // e.g., "TERRA", "AQUA", "ISS"
	FPS            int        `json:"fps"`
	WMSLayers      []WMSLayer `json:"wms_layers,omitempty"`
}

type CreateJobRequest struct {
	Bbox       []float64  `json:"bbox"` // [minLng, minLat, maxLng, maxLat]
	StartDate  string     `json:"start_date"`
	EndDate    string     `json:"end_date"`
	TimeStep       string     `json:"time_step"` // e.g., "1d", "1h", "10m"
	TrackSatellite string     `json:"track_satellite"` // e.g., "TERRA", "AQUA", "ISS"
	FPS            int        `json:"fps"`
	FrameCount     int        `json:"frame_count"`
	WMSLayers      []WMSLayer `json:"wms_layers"`
}
