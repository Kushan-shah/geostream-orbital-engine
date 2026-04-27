// Copyright 2026 Kushan J
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrJobNotFound = errors.New("job not found")
var ErrOptimisticLock = errors.New("optimistic lock failed")

type JobRepository struct {
	db *pgxpool.Pool
}

func NewJobRepository(db *pgxpool.Pool) *JobRepository {
	return &JobRepository{db: db}
}

func (r *JobRepository) CreateJob(ctx context.Context, job *Job) error {
	query := `
		INSERT INTO jobs (id, user_id, status, frame_count, request_hash, bbox, start_date, end_date, time_step, track_satellite, fps, wms_layers, version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 0)
	`
	_, err := r.db.Exec(ctx, query, job.ID, job.UserID, job.Status, job.FrameCount, job.RequestHash, job.Bbox, job.StartDate, job.EndDate, job.TimeStep, job.TrackSatellite, job.FPS, job.WMSLayers)
	return err
}

func (r *JobRepository) GetActiveJobCountByUser(ctx context.Context, userID uuid.UUID) (int, error) {
	var count int
	query := `SELECT COUNT(*) FROM jobs WHERE user_id = $1 AND status IN ('PENDING', 'PROCESSING')`
	err := r.db.QueryRow(ctx, query, userID).Scan(&count)
	return count, err
}

// ClaimJobCAS atomically claims a PENDING job for this worker.
// The WHERE status='PENDING' clause acts as the CAS guard — only one worker
// can transition PENDING→PROCESSING. PostgreSQL row-level locking ensures atomicity.
func (r *JobRepository) ClaimJobCAS(ctx context.Context, jobID uuid.UUID, workerID string, leaseDuration time.Duration) (bool, error) {
	query := `
		UPDATE jobs 
		SET status = 'PROCESSING', 
			worker_id = $1, 
			lease_expiry = NOW() + $2::interval, 
			version = version + 1,
			processing_started_at = NOW(),
			updated_at = NOW()
		WHERE id = $3 AND status = 'PENDING'
	`
	cmd, err := r.db.Exec(ctx, query, workerID, fmt.Sprintf("%d seconds", int(leaseDuration.Seconds())), jobID)
	if err != nil {
		return false, err
	}
	return cmd.RowsAffected() == 1, nil
}

func (r *JobRepository) VerifyOwnership(ctx context.Context, jobID uuid.UUID, workerID string) (bool, error) {
	var dbWorkerID *string
	var leaseExpiry *time.Time
	
	query := `SELECT worker_id, lease_expiry FROM jobs WHERE id = $1 AND status = 'PROCESSING'`
	err := r.db.QueryRow(ctx, query, jobID).Scan(&dbWorkerID, &leaseExpiry)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}

	if dbWorkerID == nil || *dbWorkerID != workerID {
		return false, nil
	}

	if leaseExpiry != nil && time.Now().After(*leaseExpiry) {
		return false, nil
	}

	return true, nil
}

func (r *JobRepository) ExtendLease(ctx context.Context, jobID uuid.UUID, workerID string, extension time.Duration) error {
	query := `
		UPDATE jobs 
		SET lease_expiry = NOW() + $1::interval, updated_at = NOW()
		WHERE id = $2 AND worker_id = $3 AND status = 'PROCESSING'
	`
	cmd, err := r.db.Exec(ctx, query, fmt.Sprintf("%d seconds", int(extension.Seconds())), jobID, workerID)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrOptimisticLock
	}
	return nil
}

func (r *JobRepository) MarkCompleted(ctx context.Context, jobID uuid.UUID, workerID string, videoURL string, metrics []byte) error {
	query := `
		UPDATE jobs 
		SET status = 'COMPLETED', video_url = $1, activity_metrics = $2, completed_at = NOW(), 
		    version = version + 1, updated_at = NOW()
		WHERE id = $3 AND worker_id = $4 AND status = 'PROCESSING'
	`
	var err error
	var cmd pgconn.CommandTag
	if len(metrics) > 0 {
		cmd, err = r.db.Exec(ctx, query, videoURL, string(metrics), jobID, workerID)
	} else {
		// Fallback if no metrics
		queryNoMetrics := `
			UPDATE jobs 
			SET status = 'COMPLETED', video_url = $1, completed_at = NOW(), 
				version = version + 1, updated_at = NOW()
			WHERE id = $2 AND worker_id = $3 AND status = 'PROCESSING'
		`
		cmd, err = r.db.Exec(ctx, queryNoMetrics, videoURL, jobID, workerID)
	}
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrOptimisticLock
	}
	return nil
}

func (r *JobRepository) GetCompletedJobByHash(ctx context.Context, hash string) (*Job, error) {
	query := `
		SELECT id, user_id, status, frame_count, request_hash, video_url, created_at, completed_at
		FROM jobs
		WHERE request_hash = $1 AND status = 'COMPLETED'
		ORDER BY completed_at DESC
		LIMIT 1
	`
	var job Job
	err := r.db.QueryRow(ctx, query, hash).Scan(
		&job.ID, &job.UserID, &job.Status, &job.FrameCount, &job.RequestHash, &job.VideoURL, &job.CreatedAt, &job.CompletedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // Not found is not an error here, just return nil
		}
		return nil, err
	}
	return &job, nil
}

func (r *JobRepository) IsCancelRequested(ctx context.Context, jobID uuid.UUID) (bool, error) {
	var cancelReq bool
	err := r.db.QueryRow(ctx, "SELECT cancel_requested FROM jobs WHERE id = $1", jobID).Scan(&cancelReq)
	return cancelReq, err
}

func (r *JobRepository) MarkFailed(ctx context.Context, jobID uuid.UUID, workerID string, errMsg string) error {
	query := `
		UPDATE jobs 
		SET status = 'FAILED', error_message = $1, retry_count = retry_count + 1, 
		    version = version + 1, updated_at = NOW()
		WHERE id = $2 AND worker_id = $3 AND status = 'PROCESSING'
	`
	_, err := r.db.Exec(ctx, query, errMsg, jobID, workerID)
	return err
}

func (r *JobRepository) MarkCancelled(ctx context.Context, jobID uuid.UUID, workerID string) error {
	query := `
		UPDATE jobs 
		SET status = 'CANCELLED', cancelled_at = NOW(), version = version + 1, updated_at = NOW()
		WHERE id = $1 AND worker_id = $2 AND status = 'PROCESSING'
	`
	_, err := r.db.Exec(ctx, query, jobID, workerID)
	return err
}

func (r *JobRepository) GetJobByID(ctx context.Context, jobID uuid.UUID) (*Job, error) {
	var job Job
	var metricsBytes []byte
	query := `SELECT id, user_id, status, frame_count, processed_frames, video_url, activity_metrics, 
	          request_hash, retry_count, created_at, updated_at, completed_at, cancelled_at, 
	          expires_at, error_message, version, bbox, start_date, end_date, fps, wms_layers,
	          COALESCE(time_step, '1d'), COALESCE(track_satellite, '') FROM jobs WHERE id = $1`
	err := r.db.QueryRow(ctx, query, jobID).Scan(
		&job.ID, &job.UserID, &job.Status, &job.FrameCount, &job.ProcessedFrames,
		&job.VideoURL, &metricsBytes, &job.RequestHash, &job.RetryCount, &job.CreatedAt, &job.UpdatedAt,
		&job.CompletedAt, &job.CancelledAt, &job.ExpiresAt, &job.ErrorMessage, &job.Version,
		&job.Bbox, &job.StartDate, &job.EndDate, &job.FPS, &job.WMSLayers,
		&job.TimeStep, &job.TrackSatellite,
	)
	if err != nil {
		return nil, err
	}
	if len(metricsBytes) > 0 {
		_ = json.Unmarshal(metricsBytes, &job.ActivityMetrics)
	}
	return &job, nil
}

// GetPendingJobs is used during startup recovery sweep to re-enqueue orphaned PENDING jobs.
func (r *JobRepository) GetPendingJobs(ctx context.Context, maxRetry int) ([]uuid.UUID, error) {
	query := `SELECT id FROM jobs WHERE status = 'PENDING' AND retry_count < $1 ORDER BY created_at ASC`
	rows, err := r.db.Query(ctx, query, maxRetry)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

// RecoverStalledJobs reclaims jobs whose leases have expired (crash recovery).
func (r *JobRepository) RecoverStalledJobs(ctx context.Context, maxRetry int) ([]uuid.UUID, error) {
	query := `
		UPDATE jobs 
		SET status = 'PENDING', worker_id = NULL, lease_expiry = NULL, 
		    version = version + 1, updated_at = NOW()
		WHERE status = 'PROCESSING' AND lease_expiry < NOW() AND retry_count < $1
		RETURNING id
	`
	rows, err := r.db.Query(ctx, query, maxRetry)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

// GetJobsByUser returns all jobs for a specific user, ordered by newest first.
func (r *JobRepository) GetJobsByUser(ctx context.Context, userID uuid.UUID) ([]Job, error) {
	query := `SELECT id, user_id, status, frame_count, processed_frames, video_url, activity_metrics,
	          request_hash, retry_count, created_at, updated_at, completed_at, cancelled_at,
	          expires_at, error_message, version, time_step, wms_layers FROM jobs WHERE user_id = $1 ORDER BY created_at DESC LIMIT 50`
	rows, err := r.db.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		var job Job
		var metricsBytes []byte
		if err := rows.Scan(
			&job.ID, &job.UserID, &job.Status, &job.FrameCount, &job.ProcessedFrames,
			&job.VideoURL, &metricsBytes, &job.RequestHash, &job.RetryCount, &job.CreatedAt, &job.UpdatedAt,
			&job.CompletedAt, &job.CancelledAt, &job.ExpiresAt, &job.ErrorMessage, &job.Version,
			&job.TimeStep, &job.WMSLayers,
		); err != nil {
			return nil, err
		}
		if len(metricsBytes) > 0 {
			_ = json.Unmarshal(metricsBytes, &job.ActivityMetrics)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return jobs, nil
}

// DeleteJob permanently deletes a job record from the database.
func (r *JobRepository) DeleteJob(ctx context.Context, jobID uuid.UUID, userID uuid.UUID) error {
	query := `DELETE FROM jobs WHERE id = $1 AND user_id = $2`
	cmd, err := r.db.Exec(ctx, query, jobID, userID)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrJobNotFound
	}
	return nil
}

// UpdateProgress sets the processed_frames count for a running job.
// Called by the pipeline as it parses real-time JSON output from the Python script.
func (r *JobRepository) UpdateProgress(ctx context.Context, jobID uuid.UUID, processed, total int) error {
	query := `UPDATE jobs SET processed_frames = $1, frame_count = $2, updated_at = NOW() WHERE id = $3`
	_, err := r.db.Exec(ctx, query, processed, total, jobID)
	return err
}
