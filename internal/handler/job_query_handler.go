// Copyright 2026 Kushan J
// SPDX-License-Identifier: Apache-2.0

package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/kushanj/geo-backend/internal/middleware"
	"github.com/kushanj/geo-backend/internal/model"
)

type JobQueryHandler struct {
	repo *model.JobRepository
}

func NewJobQueryHandler(repo *model.JobRepository) *JobQueryHandler {
	return &JobQueryHandler{repo: repo}
}

// ListUserJobs godoc
// @Summary List user jobs
// @Description Fetch all video generation jobs for the authenticated user
// @Tags Jobs
// @Security ApiKeyAuth
// @Produce json
// @Success 200 {array} model.Job "List of jobs"
// @Failure 401 {string} string "Unauthorized"
// @Failure 500 {string} string "Internal server error"
// @Router /jobs [get]
func (h *JobQueryHandler) ListUserJobs(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	jobs, err := h.repo.GetJobsByUser(r.Context(), userID)
	if err != nil {
		log.Error().Err(err).Msg("Failed to fetch user jobs")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(jobs)
}

// GetJob godoc
// @Summary Get a specific job
// @Description Fetch a specific job by its UUID
// @Tags Jobs
// @Security ApiKeyAuth
// @Produce json
// @Param id path string true "Job UUID"
// @Success 200 {object} model.Job "Job details"
// @Failure 400 {string} string "Invalid job ID"
// @Failure 404 {string} string "Job not found"
// @Router /jobs/{id} [get]
func (h *JobQueryHandler) GetJob(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	jobIDStr := chi.URLParam(r, "id")
	jobID, err := uuid.Parse(jobIDStr)
	if err != nil {
		http.Error(w, "Invalid job ID", http.StatusBadRequest)
		return
	}

	job, err := h.repo.GetJobByID(r.Context(), jobID)
	if err != nil {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}

	// Enforce ownership: users can only view their own jobs
	if job.UserID != userID {
		http.Error(w, "Job not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(job)
}
