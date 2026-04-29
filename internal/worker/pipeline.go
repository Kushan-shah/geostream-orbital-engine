// Copyright 2026 Kushan J
// SPDX-License-Identifier: Apache-2.0

package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	
	"github.com/kushanj/geo-backend/internal/model"
)

type Pipeline struct {
	jobChan       chan uuid.UUID      // bounded queue (buffered channel)
	wg            sync.WaitGroup      // tracks active workers
	cancelFunc    context.CancelFunc  // for graceful shutdown
	jobRepo       *model.JobRepository
	s3Uploader    *S3Uploader         // real AWS S3 uploader (nil if not configured)
	leaseDuration time.Duration
	jobCancels    sync.Map            // tracks active job context cancels
	TotalProcessed uint64             // telemetry metric
}

func NewPipeline(workerCount, queueSize int, jobRepo *model.JobRepository, s3Uploader *S3Uploader) *Pipeline {
	ctx, cancel := context.WithCancel(context.Background())
	p := &Pipeline{
		jobChan:       make(chan uuid.UUID, queueSize),
		cancelFunc:    cancel,
		jobRepo:       jobRepo,
		s3Uploader:    s3Uploader,
		leaseDuration: 5 * time.Minute,
	}

	for i := 0; i < workerCount; i++ {
		p.wg.Add(1)
		go p.worker(ctx, i)
	}

	return p
}

// SubmitJob is called by the HTTP handler
func (p *Pipeline) SubmitJob(jobID uuid.UUID) error {
	select {
	case p.jobChan <- jobID:
		return nil // successfully enqueued
	default:
		return fmt.Errorf("queue is full (backpressure applied)") // returns 503
	}
}

// DeleteVideoFromS3 asks the configured S3 uploader to permanently delete the video.
// If S3 is not configured, it deletes the local files.
func (p *Pipeline) DeleteVideoFromS3(ctx context.Context, jobID uuid.UUID) error {
	if p.s3Uploader != nil {
		return p.s3Uploader.DeleteVideo(ctx, jobID)
	}
	
	// Delete local files if S3 is not in use
	videoPath := filepath.Join("videos", fmt.Sprintf("%s.mp4", jobID.String()))
	metricsPath := filepath.Join("videos", fmt.Sprintf("%s_metrics.json", jobID.String()))
	
	_ = os.Remove(videoPath)
	_ = os.Remove(metricsPath)
	
	return nil
}

// AbortJob forcefully sends a SIGKILL to an active Python OS process if the job is deleted
func (p *Pipeline) AbortJob(jobID uuid.UUID) {
	if cancel, ok := p.jobCancels.Load(jobID); ok {
		log.Warn().Str("job_id", jobID.String()).Msg("Aborting active job process via OS context cancellation")
		cancel.(context.CancelFunc)()
	}
}

// QueueDepth returns the current number of jobs waiting in the channel.
func (p *Pipeline) QueueDepth() int {
	return len(p.jobChan)
}

func (p *Pipeline) worker(ctx context.Context, id int) {
	defer p.wg.Done()
	workerID := fmt.Sprintf("worker-%d-%s", id, uuid.New().String()[:8])
	log.Info().Str("worker_id", workerID).Msg("Worker started")

	for {
		select {
		case jobID, ok := <-p.jobChan:
			if !ok {
				log.Info().Str("worker_id", workerID).Msg("Channel closed, shutting down")
				return
			}
			if ctx.Err() != nil {
				log.Info().Str("worker_id", workerID).Msg("Context cancelled, shutting down")
				return
			}
			p.processJob(ctx, jobID, workerID)
		 case <-ctx.Done():
			log.Info().Str("worker_id", workerID).Msg("Worker shutting down safely")
			return
		}
	}
}

func (p *Pipeline) processJob(ctx context.Context, jobID uuid.UUID, workerID string) {
	log.Info().Str("job_id", jobID.String()).Str("worker_id", workerID).Msg("Starting job processing")

	// Wrap context for active job cancellation
	jobCtx, jobCancel := context.WithCancel(ctx)
	p.jobCancels.Store(jobID, jobCancel)
	defer func() {
		jobCancel()
		p.jobCancels.Delete(jobID)
	}()

	// Stage 0: Atomic CAS Pickup
	claimed, err := p.jobRepo.ClaimJobCAS(ctx, jobID, workerID, p.leaseDuration)
	if err != nil || !claimed {
		log.Warn().Str("job_id", jobID.String()).Msg("Failed to claim job via CAS (already claimed or cancelled)")
		return
	}

	// Get job details for frame count
	job, err := p.jobRepo.GetJobByID(ctx, jobID)
	if err != nil {
		log.Error().Err(err).Str("job_id", jobID.String()).Msg("Failed to get job details")
		_ = p.jobRepo.MarkFailed(ctx, jobID, workerID, "failed to load job details")
		return
	}

	// Start Heartbeat Goroutine
	hbCancel := p.startHeartbeat(jobCtx, jobID, workerID)
	defer hbCancel()

	// OpenCV Stage: Generate and Encode Video directly to MP4
	frameCount := job.FrameCount
	if frameCount > 300 {
		frameCount = 300
	}
	fps := job.FPS
	if fps <= 0 { fps = 30 }
	
	videoPath := fmt.Sprintf("videos/%s.mp4", jobID.String())
	
	// Create bbox string
	bboxStr := "-122.4194,37.7749,-122.3894,37.8049"
	if len(job.Bbox) == 4 {
		bboxStr = fmt.Sprintf("%f,%f,%f,%f", job.Bbox[0], job.Bbox[1], job.Bbox[2], job.Bbox[3])
	}
	
	// Dates
	start := time.Now().AddDate(0, 0, -10).Format("2006-01-02")
	end := time.Now().AddDate(0, 0, -2).Format("2006-01-02")
	if job.StartDate != nil { start = job.StartDate.Format("2006-01-02") }
	if job.EndDate != nil { end = job.EndDate.Format("2006-01-02") }

	importJSON, _ := json.Marshal(job.WMSLayers)
	layersStr := string(importJSON)
	if len(job.WMSLayers) == 0 {
		layersStr = `[{"url":"https://gibs.earthdata.nasa.gov/wms/epsg4326/best/wms.cgi","name":"MODIS_Terra_Thermal_Anomalies_All"}]`
	}

	timeStep := job.TimeStep
	if timeStep == "" { timeStep = "1d" }

	cmdArgs := []string{"scripts/generate_video.py",
		fmt.Sprintf("--job_id=%s", jobID.String()),
		fmt.Sprintf("--bbox=%s", bboxStr),
		fmt.Sprintf("--start=%s", start),
		fmt.Sprintf("--end=%s", end),
		fmt.Sprintf("--fps=%d", fps),
		fmt.Sprintf("--frames=%d", frameCount),
		fmt.Sprintf("--layers=%s", layersStr),
		fmt.Sprintf("--freq=%s", timeStep),
		fmt.Sprintf("--out=%s", videoPath),
	}
	if job.TrackSatellite != "" {
		cmdArgs = append(cmdArgs, fmt.Sprintf("--track=%s", job.TrackSatellite))
	}
	cmd := exec.CommandContext(jobCtx, "python", cmdArgs...)
	
	// Pipe stdout for real-time progress parsing
	stdoutPipe, pipeErr := cmd.StdoutPipe()
	if pipeErr != nil {
		log.Error().Err(pipeErr).Msg("Failed to create stdout pipe")
		_ = p.jobRepo.MarkFailed(ctx, jobID, workerID, "pipe error")
		return
	}
	cmd.Stderr = os.Stderr // let Python warnings flow to Go stderr

	if err := cmd.Start(); err != nil {
		log.Error().Err(err).Msg("OpenCV script failed to start")
		p.checkCancelOrError(jobCtx, jobID, workerID, fmt.Errorf("opencv start error: %w", err))
		return
	}

	// Parse progress JSON lines from stdout
	type ProgressMsg struct {
		Progress int `json:"progress"`
		Total    int `json:"total"`
	}
	scanner := bufio.NewScanner(stdoutPipe)
	for scanner.Scan() {
		line := scanner.Text()
		var msg ProgressMsg
		if err := json.Unmarshal([]byte(line), &msg); err == nil && msg.Total > 0 {
			_ = p.jobRepo.UpdateProgress(ctx, jobID, msg.Progress, msg.Total)
		}
	}

	if err := cmd.Wait(); err != nil {
		log.Error().Err(err).Msg("OpenCV script failed")
		p.checkCancelOrError(jobCtx, jobID, workerID, fmt.Errorf("opencv error: %w", err))
		return
	}
	log.Info().Str("job_id", jobID.String()).Msg("OpenCV video generated successfully")

	// Stage 3: Verify Ownership -> Finalize
	validOwner, err := p.jobRepo.VerifyOwnership(ctx, jobID, workerID)
	if err != nil || !validOwner {
		log.Warn().Str("job_id", jobID.String()).Msg("Lost lease ownership, aborting")
		return
	}

	// Stage 4: Upload to S3 if configured, else serve locally
	var videoURL string
	if p.s3Uploader != nil {
		s3URL, uploadErr := p.s3Uploader.UploadVideo(ctx, jobID, videoPath)
		if uploadErr != nil {
			log.Error().Err(uploadErr).Str("job_id", jobID.String()).Msg("S3 upload failed, falling back to local")
			videoURL = fmt.Sprintf("/videos/%s.mp4", jobID.String())
		} else {
			videoURL = s3URL
		}
	} else {
		videoURL = fmt.Sprintf("/videos/%s.mp4", jobID.String())
	}

	// Read metrics file generated by Python
	metricsPath := filepath.Join("videos", fmt.Sprintf("%s_metrics.json", jobID.String()))
	var metricsBytes []byte
	metricsBytes, readErr := os.ReadFile(metricsPath)
	if readErr != nil {
		log.Warn().Err(readErr).Str("job_id", jobID.String()).Msg("Could not read metrics JSON file, saving without metrics")
	}

	err = p.jobRepo.MarkCompleted(jobCtx, jobID, workerID, videoURL, metricsBytes)
	if err != nil {
		log.Error().Err(err).Str("job_id", jobID.String()).Msg("Failed to mark job completed")
	} else {
		// Telemetry increment on success
		atomic.AddUint64(&p.TotalProcessed, 1)
		log.Info().Str("job_id", jobID.String()).Str("video_url", videoURL[:min(80, len(videoURL))]).Msg("Job successfully completed")
	}
}

func (p *Pipeline) startHeartbeat(ctx context.Context, jobID uuid.UUID, workerID string) context.CancelFunc {
	hbCtx, hbCancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(p.leaseDuration / 3)
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				err := p.jobRepo.ExtendLease(context.Background(), jobID, workerID, p.leaseDuration)
				if err != nil {
					log.Error().Err(err).Str("job_id", jobID.String()).Msg("Failed to extend lease")
					// If lease extension fails due to optimistic lock, we should technically cancel hbCtx
					if err == model.ErrOptimisticLock {
						hbCancel()
						return
					}
				}
			}
		}
	}()
	return hbCancel
}

func (p *Pipeline) checkCancelOrError(ctx context.Context, jobID uuid.UUID, workerID string, err error) bool {
	if err != nil {
		log.Error().Err(err).Str("job_id", jobID.String()).Msg("Job processing error")
		_ = p.jobRepo.MarkFailed(ctx, jobID, workerID, err.Error())
		return true
	}

	if ctx.Err() != nil {
		log.Warn().Str("job_id", jobID.String()).Msg("Context cancelled, aborting job")
		return true
	}

	cancelReq, _ := p.jobRepo.IsCancelRequested(ctx, jobID)
	if cancelReq {
		log.Info().Str("job_id", jobID.String()).Msg("Job cancellation requested via DB flag")
		_ = p.jobRepo.MarkCancelled(ctx, jobID, workerID)
		return true
	}

	return false
}

func (p *Pipeline) GracefulShutdown() {
	// 1. Cancel context FIRST so workers see the signal
	p.cancelFunc()

	// 2. Close channel AFTER context cancel (workers check ctx.Err() before reading)
	close(p.jobChan)

	// 3. Wait for all workers to drain (30s timeout)
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Info().Msg("All workers drained cleanly")
	case <-time.After(30 * time.Second):
		log.Warn().Msg("Force shutdown - in-flight jobs will be recovered via lease expiry")
	}
}


// StartupRecovery re-enqueues orphaned PENDING jobs and reclaims stalled PROCESSING jobs.
func (p *Pipeline) StartupRecovery(ctx context.Context) {
	log.Info().Msg("Running startup recovery sweep...")

	// 1. Reclaim stalled PROCESSING jobs (lease expired)
	recovered, err := p.jobRepo.RecoverStalledJobs(ctx, 5)
	if err != nil {
		log.Error().Err(err).Msg("Failed to recover stalled jobs")
	} else if len(recovered) > 0 {
		log.Info().Int("count", len(recovered)).Msg("Recovered stalled jobs")
	}

	// 2. Re-enqueue all PENDING jobs
	pending, err := p.jobRepo.GetPendingJobs(ctx, 5)
	if err != nil {
		log.Error().Err(err).Msg("Failed to fetch pending jobs")
		return
	}

	for _, jobID := range pending {
		select {
		case p.jobChan <- jobID:
			log.Info().Str("job_id", jobID.String()).Msg("Re-enqueued pending job")
		default:
			log.Warn().Str("job_id", jobID.String()).Msg("Channel full during recovery, skipping")
		}
	}
}

// StartScheduledRecovery runs periodic recovery sweeps for crash recovery.
func (p *Pipeline) StartScheduledRecovery(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				recovered, err := p.jobRepo.RecoverStalledJobs(ctx, 5)
				if err != nil {
					log.Error().Err(err).Msg("Scheduled recovery sweep failed")
					continue
				}
				for _, jobID := range recovered {
					select {
					case p.jobChan <- jobID:
						log.Info().Str("job_id", jobID.String()).Msg("Re-enqueued recovered job")
					default:
						log.Warn().Str("job_id", jobID.String()).Msg("Channel full during sweep, skipping")
					}
				}
			}
		}
	}()
}
