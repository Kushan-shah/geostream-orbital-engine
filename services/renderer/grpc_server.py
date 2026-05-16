# Copyright 2026 Kushan Shah
# SPDX-License-Identifier: Apache-2.0

"""gRPC Server for the GeoStream Renderer Microservice.

Runs alongside the FastAPI HTTP server:
  - HTTP (port 8000): Swagger docs, health probes, debugging
  - gRPC (port 50051): Binary Protobuf communication with Go orchestrator

This dual-protocol pattern is standard in production microservices:
HTTP for human-readable APIs, gRPC for inter-service communication."""

import os
import sys
import time
import logging
from concurrent import futures

import grpc

# Add proto generated code to path
sys.path.insert(0, os.path.dirname(__file__))

import renderer_pb2
import renderer_pb2_grpc
from compositor import render_video
from cache import TieredCache

logger = logging.getLogger("renderer.grpc")

# Global cache instance (shared with FastAPI server)
_cache: TieredCache | None = None


def set_cache(cache: TieredCache):
    """Set the shared cache instance (called from main.py during startup)."""
    global _cache
    _cache = cache


class _RenderRequest:
    """Adapter: converts gRPC protobuf message to the format compositor.render_video() expects."""

    def __init__(self, pb_req):
        self.job_id = pb_req.job_id
        self.bbox = list(pb_req.bbox)
        self.start_date = pb_req.start_date
        self.end_date = pb_req.end_date
        self.fps = pb_req.fps
        self.frame_count = pb_req.frame_count
        self.time_step = pb_req.time_step or "1d"
        self.track_satellite = pb_req.track_satellite or ""
        self.output_path = pb_req.output_path

        # Convert protobuf WMSLayer messages to objects with .url, .name, .opacity
        self.layers = []
        for layer in pb_req.layers:
            self.layers.append(type("WMSLayer", (), {
                "url": layer.url,
                "name": layer.name,
                "opacity": layer.opacity if layer.opacity > 0 else 1.0,
            })())


class RendererServicer(renderer_pb2_grpc.RendererServiceServicer):
    """gRPC service implementation for the renderer."""

    def RenderVideo(self, request, context):
        """Handle a video rendering request via gRPC."""
        start_time = time.time()
        job_id = request.job_id

        logger.info(f"gRPC RenderVideo called: job_id={job_id}")

        try:
            adapted_req = _RenderRequest(request)
            result = render_video(adapted_req, _cache)
            duration = time.time() - start_time

            logger.info(f"gRPC render completed: job_id={job_id} duration={duration:.2f}s")

            return renderer_pb2.RenderResponse(
                status="completed",
                video_path=result["video_path"],
                metrics=result.get("metrics", []),
                duration_seconds=round(duration, 2),
            )

        except Exception as e:
            duration = time.time() - start_time
            logger.error(f"gRPC render failed: job_id={job_id} error={e} duration={duration:.2f}s")
            context.set_code(grpc.StatusCode.INTERNAL)
            context.set_details(str(e))
            return renderer_pb2.RenderResponse(status="failed")

    def HealthCheck(self, request, context):
        """Health probe via gRPC."""
        cache_type = "redis+disk" if (_cache and _cache.redis_client) else "disk-only"
        return renderer_pb2.HealthResponse(
            status="healthy",
            cache_type=cache_type,
            version="1.0.0",
        )


def start_grpc_server(port: int = 50051, max_workers: int = 2):
    """Start the gRPC server in a background thread.
    Called from main.py alongside the FastAPI (uvicorn) server."""
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=max_workers))
    renderer_pb2_grpc.add_RendererServiceServicer_to_server(RendererServicer(), server)
    server.add_insecure_port(f"[::]:{port}")
    server.start()
    logger.info(f"gRPC server listening on port {port}")
    return server
