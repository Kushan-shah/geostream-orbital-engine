// Copyright 2026 Kushan J
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/kushanj/geo-backend/internal/config"
	"github.com/kushanj/geo-backend/internal/handler"
	mymiddleware "github.com/kushanj/geo-backend/internal/middleware"
	"github.com/kushanj/geo-backend/internal/model"
	"github.com/kushanj/geo-backend/internal/worker"
	
	_ "github.com/kushanj/geo-backend/docs"
	httpSwagger "github.com/swaggo/http-swagger"
)

// @title GeoStream Orbital Intelligence API
// @version 1.0
// @description Distributed satellite data visualization platform capable of processing Earth observation data concurrently.
// @termsOfService http://swagger.io/terms/

// @contact.name API Support
// @contact.email kushanj@example.com

// @license.name MIT
// @license.url https://opensource.org/licenses/MIT

// @host localhost:8080
// @BasePath /api

// @securityDefinitions.apikey ApiKeyAuth
// @in header
// @name Authorization
func main() {
	// 1. Setup structured logging
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	log.Info().Msg("Starting GeoStream Backend...")

	// 2. Load Configuration
	cfg := config.LoadConfig()

	// 3. Setup Database Connection Pool
	ctx := context.Background()
	dbPool, err := config.NewDBPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to connect to database")
	}
	defer dbPool.Close()

	// 3a. Auto-Migrate Schema Updates
	migrations := []string{
		"ALTER TABLE jobs ADD COLUMN IF NOT EXISTS time_step VARCHAR(10) DEFAULT '1d';",
		"ALTER TABLE jobs ADD COLUMN IF NOT EXISTS track_satellite VARCHAR(30) DEFAULT '';",
		"CREATE INDEX IF NOT EXISTS idx_jobs_status_lease ON jobs (status, lease_expiry);",
	}
	for _, m := range migrations {
		_, err = dbPool.Exec(ctx, m)
		if err != nil {
			log.Warn().Err(err).Str("sql", m).Msg("Migration warning")
		}
	}

	// 4. Initialize Repositories and Workers
	userRepo := model.NewUserRepository(dbPool)
	jobRepo := model.NewJobRepository(dbPool)

	// 4.setup: Create video output directory
	exeDir, _ := os.Getwd()
	if err := worker.EnsureOutputDir(exeDir); err != nil {
		log.Fatal().Err(err).Msg("Failed to create video output directory")
	}
	log.Info().Str("video_dir", filepath.Join(exeDir, "videos")).Msg("Video output directory ready")

	// 4b. Initialize S3 Uploader (nil if AWS credentials are not configured)
	var s3Uploader *worker.S3Uploader
	if cfg.S3Bucket != "" {
		uploader, s3Err := worker.NewS3Uploader(cfg.AWSRegion, cfg.S3Bucket)
		if s3Err != nil {
			log.Warn().Err(s3Err).Msg("S3 uploader not available — videos will be served locally")
		} else {
			s3Uploader = uploader
			log.Info().Str("bucket", cfg.S3Bucket).Str("region", cfg.AWSRegion).Msg("S3 uploader initialized")
		}
	}

	pipeline := worker.NewPipeline(cfg.WorkerCount, cfg.QueueSize, jobRepo, s3Uploader)

	// 4a. Run Startup Recovery (re-enqueue orphaned jobs from last crash)
	pipeline.StartupRecovery(ctx)

	// 4b. Start Scheduled Recovery Sweep (every 60s)
	pipeline.StartScheduledRecovery(ctx)

	// 5. Initialize HTTP Handlers
	authHandler := handler.NewAuthHandler(userRepo, cfg.JWTSecret)
	jobHandler := handler.NewJobHandler(jobRepo, pipeline)
	jobQueryHandler := handler.NewJobQueryHandler(jobRepo)
	sseHandler := handler.NewSSEHandler(dbPool)
	healthHandler := handler.NewHealthHandler(pipeline)

	// 6. Setup Chi Router
	r := chi.NewRouter()

	// Basic Middleware (applied globally)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// CORS Middleware — allow frontend on localhost:3000
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{cfg.CORSOrigin, "http://localhost:3000"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// Public Routes
	r.Get("/health/live", healthHandler.Live)
	r.Get("/health/metrics", healthHandler.Metrics)

	// Swagger UI
	r.Get("/swagger/*", httpSwagger.WrapHandler)

	// Static file server for generated videos
	videoDir := filepath.Join(exeDir, "videos")
	fileServer := http.StripPrefix("/videos/", http.FileServer(http.Dir(videoDir)))
	r.Get("/videos/*", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "video/mp4")
		fileServer.ServeHTTP(w, r)
	})

	// Auth Routes (public, with timeout)
	r.Group(func(r chi.Router) {
		r.Use(middleware.Timeout(10 * time.Second))
		r.Post("/api/auth/register", authHandler.Register)
		r.Post("/api/auth/login", authHandler.Login)
	})

	// Protected Routes (with 60s request timeout)
	r.Group(func(r chi.Router) {
		r.Use(middleware.Timeout(60 * time.Second))
		r.Use(mymiddleware.AuthMiddleware(cfg.JWTSecret))

		r.Post("/api/jobs", jobHandler.CreateJob)
		r.Get("/api/jobs", jobQueryHandler.ListUserJobs)
		r.Get("/api/jobs/{id}", jobQueryHandler.GetJob)
		r.Delete("/api/jobs/{id}", jobHandler.DeleteJobHandler)
	})

	// SSE and Streaming endpoints — excluded from Timeout middleware because connections
	// are long-lived. Uses its own auth but no request timeout.
	r.Group(func(r chi.Router) {
		r.Use(mymiddleware.AuthMiddleware(cfg.JWTSecret))
		r.Get("/api/jobs/progress/stream", sseHandler.StreamProgress)
		r.Get("/api/stream/live", jobHandler.LiveStreamHandler)
	})

	// 7. Start HTTP Server
	server := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: r,
	}

	go func() {
		log.Info().Str("port", cfg.Port).Msg("HTTP server listening")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("HTTP server failed")
		}
	}()

	// 8. Graceful Shutdown (os.Signal)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	log.Info().Msg("Shutdown signal received")

	// Shutdown HTTP Server FIRST (stop accepting new requests)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("HTTP server shutdown error")
	}

	// THEN drain the pipeline workers (they finish in-flight jobs)
	pipeline.GracefulShutdown()

	// FINALLY close the database connection pool
	dbPool.Close()

	log.Info().Msg("GeoStream Backend gracefully stopped")
}
