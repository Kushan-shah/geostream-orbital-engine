// Copyright 2026 Kushan J
// SPDX-License-Identifier: Apache-2.0

package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/kushanj/geo-backend/internal/middleware"
	"github.com/kushanj/geo-backend/internal/model"
	"github.com/kushanj/geo-backend/internal/worker"
)

type JobHandler struct {
	repo     *model.JobRepository
	pipeline *worker.Pipeline
}

func NewJobHandler(repo *model.JobRepository, pipeline *worker.Pipeline) *JobHandler {
	return &JobHandler{repo: repo, pipeline: pipeline}
}

// CreateJob godoc
// @Summary Submit a new video generation job
// @Description Submit a geographic bounding box and date range to generate a timelapse video
// @Tags Jobs
// @Security ApiKeyAuth
// @Accept json
// @Produce json
// @Param request body model.CreateJobRequest true "Job Configuration"
// @Success 201 {object} map[string]string "Job ID"
// @Failure 400 {string} string "Invalid input or boundaries"
// @Failure 401 {string} string "Unauthorized"
// @Failure 409 {string} string "Job already exists"
// @Failure 500 {string} string "Internal server error"
// @Router /jobs [post]
func (h *JobHandler) CreateJob(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req model.CreateJobRequest
	// Limit request body to 1MB to prevent DoS via oversized payloads
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request payload", http.StatusBadRequest)
		return
	}

	// Input Validation
	if req.FrameCount <= 0 || req.FrameCount > 1800 {
		http.Error(w, "frame_count must be between 1 and 1800", http.StatusBadRequest)
		return
	}
	if len(req.Bbox) != 4 {
		http.Error(w, "bbox must contain exactly 4 coordinates [minLng, minLat, maxLng, maxLat]", http.StatusBadRequest)
		return
	}
	if req.StartDate == "" || req.EndDate == "" {
		http.Error(w, "start_date and end_date are required", http.StatusBadRequest)
		return
	}
	if req.FPS <= 0 || req.FPS > 60 {
		http.Error(w, "fps must be between 1 and 60", http.StatusBadRequest)
		return
	}
	if len(req.WMSLayers) == 0 {
		http.Error(w, "at least one WMS layer is required", http.StatusBadRequest)
		return
	}

	// 1. Backpressure Check
	count, err := h.repo.GetActiveJobCountByUser(r.Context(), userID)
	if err != nil {
		log.Error().Err(err).Msg("Failed to check active jobs")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if count >= 5 {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "Rate limit exceeded: maximum 5 active jobs allowed", http.StatusTooManyRequests)
		return
	}

	// 2. Deduplication hash (Global, independent of UserID)
	hashInput := fmt.Sprintf("%v-%s-%s-%d-%d-%s-%s", req.Bbox, req.StartDate, req.EndDate, req.FPS, req.FrameCount, req.TimeStep, req.TrackSatellite)
	// Add layers to hash
	for _, layer := range req.WMSLayers {
		hashInput += fmt.Sprintf("-%s-%s-%.2f", layer.Name, layer.URL, layer.Opacity)
	}
	hash := sha256.Sum256([]byte(hashInput))
	requestHash := hex.EncodeToString(hash[:])

	// 2.5 Caching Check: Disabled per user request so jobs always regenerate 
	// even if parameters match exactly, ensuring fresh video files.
	// cachedJob, err := h.repo.GetCompletedJobByHash(r.Context(), requestHash)
	// if err == nil && cachedJob != nil { ... }

	// 3. Parse dates
	startDate, err := time.Parse("2006-01-02", req.StartDate)
	if err != nil {
		http.Error(w, "Invalid start_date format (use YYYY-MM-DD)", http.StatusBadRequest)
		return
	}
	endDate, err := time.Parse("2006-01-02", req.EndDate)
	if err != nil {
		http.Error(w, "Invalid end_date format (use YYYY-MM-DD)", http.StatusBadRequest)
		return
	}

	// 4. API Boundary Validation
	totalDays := endDate.Sub(startDate).Hours() / 24
	if totalDays > 300 {
		http.Error(w, "Time range too large. Maximum allowed is 300 days.", http.StatusBadRequest)
		return
	}
	if totalDays < 0 {
		http.Error(w, "end_date must be after start_date", http.StatusBadRequest)
		return
	}

	// 5. Persist Job
	job := &model.Job{
		ID:             uuid.New(),
		UserID:         userID,
		Status:         model.StatusPending,
		FrameCount:     req.FrameCount,
		RequestHash:    requestHash,
		Bbox:           req.Bbox,
		StartDate:      &startDate,
		EndDate:        &endDate,
		TimeStep:       req.TimeStep,
		TrackSatellite: req.TrackSatellite,
		FPS:            req.FPS,
		WMSLayers:      req.WMSLayers,
	}

	if err := h.repo.CreateJob(r.Context(), job); err != nil {
		log.Error().Err(err).Msg("Failed to create job in DB (duplicate hash?)")
		http.Error(w, "Job with these exact parameters is currently processing", http.StatusConflict)
		return
	}

	// 4. Push to Channel
	if err := h.pipeline.SubmitJob(job.ID); err != nil {
		log.Warn().Err(err).Msg("Channel full, backpressure applied")
		// Could mark job as FAILED immediately, but let's let it sit as PENDING 
		// and the startup/recovery sweep can enqueue it later.
		http.Error(w, "System overloaded, try again later", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{
		"job_id":  job.ID.String(),
		"message": "Job enqueued successfully",
	})
}

// DeleteJobHandler godoc
// @Summary Delete a job and its associated video
// @Description Aborts the active process, deletes the S3 video, and removes the job from the DB
// @Tags Jobs
// @Security ApiKeyAuth
// @Param id path string true "Job UUID"
// @Success 200 {string} string "Job successfully deleted"
// @Failure 400 {string} string "Invalid job ID"
// @Failure 401 {string} string "Unauthorized"
// @Failure 404 {string} string "Job not found"
// @Failure 500 {string} string "Internal server error"
// @Router /jobs/{id} [delete]
func (h *JobHandler) DeleteJobHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	jobIDStr := chi.URLParam(r, "id")
	if jobIDStr == "" {
		http.Error(w, "job ID is required", http.StatusBadRequest)
		return
	}

	jobID, err := uuid.Parse(jobIDStr)
	if err != nil {
		http.Error(w, "invalid job ID", http.StatusBadRequest)
		return
	}

	// Abort Active OS Process if it's currently running
	h.pipeline.AbortJob(jobID)

	// Delete from S3
	err = h.pipeline.DeleteVideoFromS3(r.Context(), jobID)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to delete video from S3 (might not exist)")
	}

	// Delete from DB
	err = h.repo.DeleteJob(r.Context(), jobID, userID)
	if err != nil {
		if err == model.ErrJobNotFound {
			http.Error(w, "Job not found or unauthorized", http.StatusNotFound)
			return
		}
		log.Error().Err(err).Msg("Failed to delete job from DB")
		http.Error(w, "Failed to delete job", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// LiveStreamHandler godoc
// @Summary Stream MJPEG live video
// @Description Streams a real-time HTTP MJPEG boundary stream from OpenCV. (Note: May freeze Swagger UI, use standard browser to view)
// @Tags Jobs
// @Security ApiKeyAuth
// @Produce multipart/x-mixed-replace
// @Param bbox query string false "Bounding box (e.g., -122.4,37.7,-122.3,37.8)"
// @Param start_date query string false "Start Date YYYY-MM-DD"
// @Param end_date query string false "End Date YYYY-MM-DD"
// @Param layers query string false "JSON array of layer objects"
// @Success 200 {string} string "MJPEG Stream"
// @Router /stream/live [get]
func (h *JobHandler) LiveStreamHandler(w http.ResponseWriter, r *http.Request) {
	// Parse query params
	bboxStr := r.URL.Query().Get("bbox")
	if bboxStr == "" {
		bboxStr = "-122.4194,37.7749,-122.3894,37.8049" // default San Francisco
	}
	start := r.URL.Query().Get("start_date")
	if start == "" {
		start = time.Now().AddDate(0, 0, -10).Format("2006-01-02")
	}
	end := r.URL.Query().Get("end_date")
	if end == "" {
		end = time.Now().AddDate(0, 0, -2).Format("2006-01-02")
	}
	layersJSON := r.URL.Query().Get("layers")
	if layersJSON == "" {
		layersJSON = `[{"url":"https://gibs.earthdata.nasa.gov/wms/epsg4326/best/wms.cgi","name":"MODIS_Terra_Thermal_Anomalies_All"}]`
	}

	// Set MJPEG headers
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Cache-Control", "no-cache")
	
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	freq := r.URL.Query().Get("time_step")
	if freq == "" {
		freq = "1d"
	}

	trackSat := r.URL.Query().Get("track_satellite")

	streamArgs := []string{"scripts/stream_video.py",
		fmt.Sprintf("--bbox=%s", bboxStr),
		fmt.Sprintf("--start=%s", start),
		fmt.Sprintf("--end=%s", end),
		fmt.Sprintf("--layers=%s", layersJSON),
		fmt.Sprintf("--freq=%s", freq),
	}
	if trackSat != "" {
		streamArgs = append(streamArgs, fmt.Sprintf("--track=%s", trackSat))
	}
	cmd := exec.CommandContext(r.Context(), "python", streamArgs...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Error().Err(err).Msg("Failed to pipe stdout")
		return
	}
	
	if err := cmd.Start(); err != nil {
		log.Error().Err(err).Msg("Failed to start stream_video.py")
		return
	}

	// Stream chunks directly to the HTTP response
	buf := make([]byte, 4096)
	for {
		n, err := stdout.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			flusher.Flush()
		}
		if err != nil {
			break // EOF or Context Canceled
		}
	}

	cmd.Wait()
}
