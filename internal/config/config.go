// Copyright 2026 Kushan Shah
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	Port         string
	DatabaseURL  string
	AWSRegion    string
	S3Bucket     string
	WorkerCount  int
	QueueSize    int
	JWTSecret    string
	CORSOrigin   string

	// Microservice URLs (feature-flagged: empty = disabled, uses fallback)
	RendererURL     string // FastAPI renderer HTTP URL (e.g., http://renderer:8000)
	RendererGRPCURL string // Renderer gRPC address (e.g., renderer:50051) — preferred over HTTP
	RabbitMQURL     string // AMQP connection string (e.g., amqp://guest:guest@rabbitmq:5672/)
	RedisURL        string // Redis connection string (e.g., redis://localhost:6379)
}

func LoadConfig() *Config {
	// Load .env if it exists (useful for local dev)
	_ = godotenv.Load()

	workerCount, err := strconv.Atoi(getEnv("WORKER_COUNT", "5"))
	if err != nil {
		workerCount = 5
	}

	queueSize, err := strconv.Atoi(getEnv("QUEUE_SIZE", "100"))
	if err != nil {
		queueSize = 100
	}

	return &Config{
		Port:        getEnv("PORT", "8080"),
		DatabaseURL: getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/geostream?sslmode=disable"),
		AWSRegion:   getEnv("AWS_REGION", "ap-south-1"),
		S3Bucket:    getEnv("S3_BUCKET", ""),
		WorkerCount: workerCount,
		QueueSize:   queueSize,
		JWTSecret:   getEnv("JWT_SECRET", "super-secret-key-change-in-prod"),
		CORSOrigin:  getEnv("CORS_ORIGIN", "http://localhost:3000"),

		// Microservice URLs — empty = feature disabled (uses fallback path)
		RendererURL:     getEnv("RENDERER_URL", ""),
		RendererGRPCURL: getEnv("RENDERER_GRPC_URL", ""),
		RabbitMQURL:     getEnv("RABBITMQ_URL", ""),
		RedisURL:        getEnv("REDIS_URL", ""),
	}
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
