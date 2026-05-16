// Copyright 2026 Kushan Shah
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
	"github.com/kushanj/geo-backend/internal/queue"
)

type Pipeline struct {
	queue              queue.JobQueue         // pluggable: ChannelQueue or RabbitMQQueue
	wg                 sync.WaitGroup         // tracks active workers
	cancelFunc         context.CancelFunc     // for graceful shutdown
	jobRepo            *model.JobRepository
	s3Uploader         *S3Uploader            // real AWS S3 uploader (nil if not configured)
	rendererClient     *RendererClient        // HTTP client for FastAPI renderer (nil if not configured)
	grpcRendererClient *GRPCRendererClient    // gRPC client (preferred over HTTP, nil if not configured)
	leaseDuration      time.Duration
	jobCancels         sync.Map               // tracks active job context cancels
	TotalProcessed     uint64                 // telemetry metric
}

// NewPipeline creates a pipeline with a pluggable queue and optional renderer client.
// If rendererClient is nil, jobs are processed via subprocess (original behavior).
// If queue is ChannelQueue, behavior is identical to the original buffered channel.
func NewPipeline(workerCount int, jobQueue queue.JobQueue, jobRepo *model.JobRepository, s3Uploader *S3Uploader, rendererClient *RendererClient, grpcRendererClient *GRPCRendererClient) *Pipeline {
	ctx, cancel := context.WithCancel(context.Background())
	p := &Pipeline{
		queue:              jobQueue,
		cancelFunc:         cancel,
		jobRepo:            jobRepo,
		s3Uploader:         s3Uploader,
		rendererClient:     rendererClient,
		grpcRendererClient: grpcRendererClient,
		leaseDuration:      5 * time.Minute,
	}

	// Start consuming from the queue
	jobChan, err := jobQueue.Consume(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to start consuming from job queue")
	}

	for i := 0; i < workerCount; i++ {
		p.wg.Add(1)
		go p.worker(ctx, i, jobChan)
	}

	return p
}

// SubmitJob is called by the HTTP handler to enqueue a job.
func (p *Pipeline) SubmitJob(jobID uuid.UUID) error {
	return p.queue.Publish(context.Background(), jobID)
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

// QueueDepth returns the current number of jobs waiting in the queue.
func (p *Pipeline) QueueDepth() int {
	return p.queue.Depth()
}

func (p *Pipeline) worker(ctx context.Context, id int, jobChan <-chan uuid.UUID) {
	defer p.wg.Done()
	workerID := fmt.Sprintf("worker-%d-%s", id, uuid.New().String()[:8])
	log.Info().Str("worker_id", workerID).Msg("Worker started")

	for {
		select {
		case jobID, ok := <-jobChan:
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

	// ── Rendering Strategy (Fallback Chain) ──
	// Priority: gRPC (fastest) → HTTP (fallback) → subprocess (original)
	var videoPath string
	var metricsBytes []byte

	if p.grpcRendererClient != nil {
		// Preferred: gRPC/Protobuf (binary, ~10x faster serialization)
		videoPath, metricsBytes, err = p.renderViaGRPC(jobCtx, job, workerID)
		if err != nil {
			log.Warn().Err(err).Str("job_id", jobID.String()).Msg("gRPC renderer failed, trying HTTP fallback")
			err = nil // reset for next attempt
		}
	}
	if videoPath == "" && p.rendererClient != nil {
		// Fallback: HTTP/JSON
		videoPath, metricsBytes, err = p.renderViaService(jobCtx, job, workerID)
		if err != nil {
			log.Warn().Err(err).Str("job_id", jobID.String()).Msg("HTTP renderer failed, falling back to subprocess")
			err = nil
		}
	}
	if videoPath == "" {
		// Final fallback: subprocess (original behavior, always works)
		videoPath, metricsBytes, err = p.renderViaSubprocess(jobCtx, job, workerID)
	}

	if err != nil {
		p.checkCancelOrError(jobCtx, jobID, workerID, err)
		return
	}

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

	err = p.jobRepo.MarkCompleted(jobCtx, jobID, workerID, videoURL, metricsBytes)
	if err != nil {
		log.Error().Err(err).Str("job_id", jobID.String()).Msg("Failed to mark job completed")
	} else {
		// Telemetry increment on success
		atomic.AddUint64(&p.TotalProcessed, 1)
		log.Info().Str("job_id", jobID.String()).Str("video_url", videoURL[:min(80, len(videoURL))]).Msg("Job successfully completed")
	}
}

// renderViaGRPC dispatches the render job via gRPC/Protobuf (preferred path).
func (p *Pipeline) renderViaGRPC(ctx context.Context, job *model.Job, workerID string) (string, []byte, error) {
	req := p.buildRendererRequest(job)
	resp, err := p.grpcRendererClient.Render(ctx, req)
	if err != nil {
		return "", nil, fmt.Errorf("gRPC renderer error: %w", err)
	}
	metricsJSON, _ := json.Marshal(resp.Metrics)
	return req.OutputPath, metricsJSON, nil
}

// renderViaService dispatches the render job to the FastAPI microservice via HTTP.
func (p *Pipeline) renderViaService(ctx context.Context, job *model.Job, workerID string) (string, []byte, error) {
	req := p.buildRendererRequest(job)
	resp, err := p.rendererClient.Render(ctx, req)
	if err != nil {
		return "", nil, fmt.Errorf("HTTP renderer error: %w", err)
	}
	metricsJSON, _ := json.Marshal(resp.Metrics)
	return req.OutputPath, metricsJSON, nil
}

// buildRendererRequest constructs the shared request payload for both gRPC and HTTP paths.
func (p *Pipeline) buildRendererRequest(job *model.Job) RendererRequest {
	videoPath := fmt.Sprintf("videos/%s.mp4", job.ID.String())

	start := time.Now().AddDate(0, 0, -10).Format("2006-01-02")
	end := time.Now().AddDate(0, 0, -2).Format("2006-01-02")
	if job.StartDate != nil {
		start = job.StartDate.Format("2006-01-02")
	}
	if job.EndDate != nil {
		end = job.EndDate.Format("2006-01-02")
	}

	var layers []RendererLayer
	for _, l := range job.WMSLayers {
		opacity := l.Opacity
		if opacity <= 0 {
			opacity = 1.0
		}
		layers = append(layers, RendererLayer{
			URL:     l.URL,
			Name:    l.Name,
			Opacity: opacity,
		})
	}
	if len(layers) == 0 {
		layers = []RendererLayer{{
			URL:     "https://gibs.earthdata.nasa.gov/wms/epsg4326/best/wms.cgi",
			Name:    "MODIS_Terra_Thermal_Anomalies_All",
			Opacity: 1.0,
		}}
	}

	frameCount := job.FrameCount
	if frameCount > 300 {
		frameCount = 300
	}
	fps := job.FPS
	if fps <= 0 {
		fps = 30
	}
	timeStep := job.TimeStep
	if timeStep == "" {
		timeStep = "1d"
	}

	return RendererRequest{
		JobID:          job.ID.String(),
		Bbox:           job.Bbox,
		StartDate:      start,
		EndDate:        end,
		FPS:            fps,
		FrameCount:     frameCount,
		Layers:         layers,
		TimeStep:       timeStep,
		TrackSatellite: job.TrackSatellite,
		OutputPath:     videoPath,
	}
}

// renderViaSubprocess runs the Python script directly (original behavior, zero changes).
func (p *Pipeline) renderViaSubprocess(ctx context.Context, job *model.Job, workerID string) (string, []byte, error) {
	frameCount := job.FrameCount
	if frameCount > 300 {
		frameCount = 300
	}
	fps := job.FPS
	if fps <= 0 {
		fps = 30
	}

	videoPath := fmt.Sprintf("videos/%s.mp4", job.ID.String())

	// Create bbox string
	bboxStr := "-122.4194,37.7749,-122.3894,37.8049"
	if len(job.Bbox) == 4 {
		bboxStr = fmt.Sprintf("%f,%f,%f,%f", job.Bbox[0], job.Bbox[1], job.Bbox[2], job.Bbox[3])
	}

	// Dates
	start := time.Now().AddDate(0, 0, -10).Format("2006-01-02")
	end := time.Now().AddDate(0, 0, -2).Format("2006-01-02")
	if job.StartDate != nil {
		start = job.StartDate.Format("2006-01-02")
	}
	if job.EndDate != nil {
		end = job.EndDate.Format("2006-01-02")
	}

	importJSON, _ := json.Marshal(job.WMSLayers)
	layersStr := string(importJSON)
	if len(job.WMSLayers) == 0 {
		layersStr = `[{"url":"https://gibs.earthdata.nasa.gov/wms/epsg4326/best/wms.cgi","name":"MODIS_Terra_Thermal_Anomalies_All"}]`
	}

	timeStep := job.TimeStep
	if timeStep == "" {
		timeStep = "1d"
	}

	cmdArgs := []string{"scripts/generate_video.py",
		fmt.Sprintf("--job_id=%s", job.ID.String()),
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
	cmd := exec.CommandContext(ctx, "python", cmdArgs...)

	// Pipe stdout for real-time progress parsing
	stdoutPipe, pipeErr := cmd.StdoutPipe()
	if pipeErr != nil {
		return "", nil, fmt.Errorf("pipe error: %w", pipeErr)
	}
	cmd.Stderr = os.Stderr // let Python warnings flow to Go stderr

	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("opencv start error: %w", err)
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
			_ = p.jobRepo.UpdateProgress(ctx, job.ID, msg.Progress, msg.Total)
		}
	}

	if err := cmd.Wait(); err != nil {
		return "", nil, fmt.Errorf("opencv error: %w", err)
	}
	log.Info().Str("job_id", job.ID.String()).Msg("OpenCV video generated successfully (subprocess)")

	// Read metrics file generated by Python
	metricsPath := filepath.Join("videos", fmt.Sprintf("%s_metrics.json", job.ID.String()))
	metricsBytes, readErr := os.ReadFile(metricsPath)
	if readErr != nil {
		log.Warn().Err(readErr).Str("job_id", job.ID.String()).Msg("Could not read metrics JSON file")
	}

	return videoPath, metricsBytes, nil
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

	// 2. Close the queue (signals workers to drain)
	p.queue.Close()

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
		if err := p.queue.Publish(ctx, jobID); err != nil {
			log.Warn().Str("job_id", jobID.String()).Msg("Queue full during recovery, skipping")
		} else {
			log.Info().Str("job_id", jobID.String()).Msg("Re-enqueued pending job")
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
					if err := p.queue.Publish(ctx, jobID); err != nil {
						log.Warn().Str("job_id", jobID.String()).Msg("Queue full during sweep, skipping")
					} else {
						log.Info().Str("job_id", jobID.String()).Msg("Re-enqueued recovered job")
					}
				}
			}
		}
	}()
}
