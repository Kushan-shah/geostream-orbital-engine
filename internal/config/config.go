// Copyright 2026 Kushan J
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
	}
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
