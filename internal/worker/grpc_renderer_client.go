// Copyright 2026 Kushan Shah
// SPDX-License-Identifier: Apache-2.0

package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/kushanj/geo-backend/internal/proto"
)

// GRPCRendererClient handles gRPC communication with the FastAPI renderer microservice.
// Uses binary Protocol Buffers for ~10x more efficient serialization than JSON/REST.
type GRPCRendererClient struct {
	conn   *grpc.ClientConn
	client pb.RendererServiceClient
	cb     *CircuitBreaker
}

// NewGRPCRendererClient establishes a gRPC connection with the renderer service.
// Connection pooling is handled by the gRPC framework automatically.
func NewGRPCRendererClient(target string) (*GRPCRendererClient, error) {
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(50*1024*1024), // 50MB max response (video metadata)
		),
	)
	if err != nil {
		return nil, fmt.Errorf("gRPC connection failed: %w", err)
	}

	return &GRPCRendererClient{
		conn:   conn,
		client: pb.NewRendererServiceClient(conn),
		cb:     NewCircuitBreaker(3, 30*time.Second),
	}, nil
}

// Render sends a render request via gRPC (binary Protocol Buffers).
func (c *GRPCRendererClient) Render(ctx context.Context, req RendererRequest) (*RendererResponse, error) {
	if err := c.cb.Allow(); err != nil {
		return nil, fmt.Errorf("gRPC circuit breaker open: %w", err)
	}

	// Convert to protobuf message
	pbLayers := make([]*pb.WMSLayer, len(req.Layers))
	for i, l := range req.Layers {
		pbLayers[i] = &pb.WMSLayer{
			Url:     l.URL,
			Name:    l.Name,
			Opacity: l.Opacity,
		}
	}

	pbReq := &pb.RenderRequest{
		JobId:          req.JobID,
		Bbox:           req.Bbox,
		StartDate:      req.StartDate,
		EndDate:        req.EndDate,
		Fps:            int32(req.FPS),
		FrameCount:     int32(req.FrameCount),
		Layers:         pbLayers,
		TimeStep:       req.TimeStep,
		TrackSatellite: req.TrackSatellite,
		OutputPath:     req.OutputPath,
	}

	log.Info().
		Str("target", c.conn.Target()).
		Str("job_id", req.JobID).
		Str("protocol", "gRPC/Protobuf").
		Msg("Dispatching render request via gRPC")

	resp, err := c.client.RenderVideo(ctx, pbReq)
	if err != nil {
		c.cb.RecordFailure()
		return nil, fmt.Errorf("gRPC RenderVideo failed: %w", err)
	}

	c.cb.RecordSuccess()
	log.Info().
		Str("job_id", req.JobID).
		Float64("duration_s", resp.DurationSeconds).
		Msg("gRPC render completed")

	return &RendererResponse{
		Status:          resp.Status,
		VideoPath:       resp.VideoPath,
		Metrics:         resp.Metrics,
		DurationSeconds: resp.DurationSeconds,
	}, nil
}

// HealthCheck pings the renderer via gRPC.
func (c *GRPCRendererClient) HealthCheck(ctx context.Context) error {
	resp, err := c.client.HealthCheck(ctx, &pb.HealthRequest{})
	if err != nil {
		return fmt.Errorf("gRPC health check failed: %w", err)
	}
	if resp.Status != "healthy" {
		return fmt.Errorf("renderer unhealthy: %s", resp.Status)
	}
	return nil
}

// Close shuts down the gRPC connection.
func (c *GRPCRendererClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
