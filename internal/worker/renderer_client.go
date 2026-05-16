// Copyright 2026 Kushan Shah
// SPDX-License-Identifier: Apache-2.0

package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
)

// RendererClient handles HTTP communication with the FastAPI renderer microservice.
// Features:
//   - Connection pooling with keep-alive for TCP reuse
//   - Circuit breaker to prevent cascading failures
//   - Configurable timeouts per request
type RendererClient struct {
	baseURL    string
	httpClient *http.Client
	cb         *CircuitBreaker
}

// RendererRequest is the JSON payload sent to POST /render.
type RendererRequest struct {
	JobID          string         `json:"job_id"`
	Bbox           []float64      `json:"bbox"`
	StartDate      string         `json:"start_date"`
	EndDate        string         `json:"end_date"`
	FPS            int            `json:"fps"`
	FrameCount     int            `json:"frame_count"`
	Layers         []RendererLayer `json:"layers"`
	TimeStep       string         `json:"time_step"`
	TrackSatellite string         `json:"track_satellite"`
	OutputPath     string         `json:"output_path"`
}

// RendererLayer represents a WMS layer in the renderer request.
type RendererLayer struct {
	URL     string  `json:"url"`
	Name    string  `json:"name"`
	Opacity float64 `json:"opacity"`
}

// RendererResponse is the JSON payload returned by the FastAPI /render endpoint.
type RendererResponse struct {
	Status          string    `json:"status"`
	VideoPath       string    `json:"video_path"`
	Metrics         []float64 `json:"metrics"`
	DurationSeconds float64   `json:"duration_seconds"`
}

// NewRendererClient creates an HTTP client optimized for inter-service communication.
// Connection pooling reduces TCP handshake overhead for repeated calls.
func NewRendererClient(baseURL string) *RendererClient {
	transport := &http.Transport{
		// Connection pooling: reuse TCP connections to the renderer service
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		// TCP keep-alive for long-lived connections
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}

	return &RendererClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   10 * time.Minute, // rendering can take several minutes
		},
		cb: NewCircuitBreaker(3, 30*time.Second),
	}
}

// Render sends a render request to the FastAPI service.
// Returns the response or an error. The circuit breaker wraps the call.
func (c *RendererClient) Render(ctx context.Context, req RendererRequest) (*RendererResponse, error) {
	// Circuit breaker check
	if err := c.cb.Allow(); err != nil {
		return nil, fmt.Errorf("renderer circuit breaker open: %w", err)
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal render request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/render", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	log.Info().
		Str("url", c.baseURL+"/render").
		Str("job_id", req.JobID).
		Str("circuit_state", c.cb.State()).
		Msg("Dispatching render request to FastAPI service")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		c.cb.RecordFailure()
		return nil, fmt.Errorf("renderer HTTP call failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.cb.RecordFailure()
		return nil, fmt.Errorf("failed to read renderer response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		c.cb.RecordFailure()
		return nil, fmt.Errorf("renderer returned HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var renderResp RendererResponse
	if err := json.Unmarshal(respBody, &renderResp); err != nil {
		c.cb.RecordFailure()
		return nil, fmt.Errorf("failed to unmarshal renderer response: %w", err)
	}

	c.cb.RecordSuccess()
	log.Info().
		Str("job_id", req.JobID).
		Float64("duration_s", renderResp.DurationSeconds).
		Str("video_path", renderResp.VideoPath).
		Msg("Render request completed via FastAPI service")

	return &renderResp, nil
}

// HealthCheck pings the renderer service's /health endpoint.
func (c *RendererClient) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("renderer health check failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("renderer unhealthy: HTTP %d", resp.StatusCode)
	}
	return nil
}
