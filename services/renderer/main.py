# Copyright 2026 Kushan Shah
# SPDX-License-Identifier: Apache-2.0

"""GeoStream Renderer Microservice

Standalone FastAPI service that handles geospatial video rendering.
Decoupled from the Go orchestrator to enable independent scaling,
technology-specific optimization, and clear service boundaries.

Architecture:
    Go API (orchestrator) → HTTP POST /render → This Service (renderer)

Caching Strategy:
    L1: Redis (if REDIS_URL is set) — sub-millisecond shared cache
    L2: MD5 disk cache (always available) — filesystem fallback
    L3: NASA WMS API (network fetch on cache miss)

Run locally:
    uvicorn main:app --host 0.0.0.0 --port 8000 --reload
"""

import asyncio
import os
import time
import logging
from contextlib import asynccontextmanager
from concurrent.futures import ThreadPoolExecutor

from fastapi import FastAPI, HTTPException, status
from fastapi.middleware.cors import CORSMiddleware

from schemas import RenderRequest, RenderResponse, HealthResponse
from compositor import render_video
from cache import TieredCache

# ── Logging ──
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
)
logger = logging.getLogger("renderer")

# ── Thread Pool for CPU-bound rendering ──
# Video rendering (OpenCV + FFmpeg) is CPU-bound. Running it in the async
# event loop would block all other requests. ThreadPoolExecutor isolates
# rendering work while keeping the health endpoint responsive.
RENDERER_WORKERS = int(os.getenv("RENDERER_WORKERS", "2"))
executor = ThreadPoolExecutor(max_workers=RENDERER_WORKERS)

# ── Global State ──
cache: TieredCache | None = None


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Application startup and shutdown lifecycle."""
    global cache

    redis_url = os.getenv("REDIS_URL", "")
    cache = TieredCache(redis_url=redis_url if redis_url else None)

    logger.info(f"Renderer service started (workers={RENDERER_WORKERS})")
    if cache.redis_client:
        logger.info("Redis L1 cache: CONNECTED")
    else:
        logger.info("Redis L1 cache: UNAVAILABLE (using disk-only)")

    # Start gRPC server alongside FastAPI (dual-protocol pattern)
    grpc_server = None
    try:
        from grpc_server import start_grpc_server, set_cache
        set_cache(cache)
        grpc_port = int(os.getenv("GRPC_PORT", "50051"))
        grpc_server = start_grpc_server(port=grpc_port, max_workers=RENDERER_WORKERS)
        logger.info(f"gRPC server: LISTENING on port {grpc_port}")
    except Exception as e:
        logger.warning(f"gRPC server failed to start: {e} (HTTP-only mode)")

    yield

    if grpc_server:
        grpc_server.stop(grace=5)
    executor.shutdown(wait=True)
    logger.info("Renderer service stopped")


# ── FastAPI Application ──
app = FastAPI(
    title="GeoStream Renderer Service",
    description=(
        "Microservice for geospatial video rendering with tiered caching. "
        "Accepts render requests from the Go orchestrator, processes NASA WMS "
        "imagery through OpenCV, and returns H.264-encoded MP4 videos."
    ),
    version="1.0.0",
    lifespan=lifespan,
    docs_url="/docs",      # Swagger UI
    redoc_url="/redoc",    # ReDoc alternative
)

app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_methods=["*"],
    allow_headers=["*"],
)


# ── Health Endpoint ──

@app.get("/health", response_model=HealthResponse, tags=["Telemetry"])
async def health_check():
    """Readiness probe for the renderer service.
    Returns cache tier status and version information."""
    return HealthResponse(
        status="healthy",
        cache_type="redis+disk" if (cache and cache.redis_client) else "disk-only",
        version="1.0.0",
    )


@app.get("/cache/stats", tags=["Telemetry"])
async def cache_stats():
    """Returns cache hit/miss statistics for observability."""
    if cache:
        return cache.stats
    return {"error": "Cache not initialized"}


# ── Render Endpoint ──

@app.post("/render", response_model=RenderResponse, tags=["Rendering"])
async def render(request: RenderRequest):
    """Submit a video rendering job.

    The rendering pipeline runs in a thread pool to avoid blocking
    the async event loop. This keeps the /health endpoint responsive
    even during heavy rendering workloads.

    The Go orchestrator calls this endpoint after claiming a job via
    CAS locking in PostgreSQL. On failure, the orchestrator falls back
    to the subprocess pipeline automatically."""
    start_time = time.time()

    try:
        loop = asyncio.get_event_loop()
        result = await loop.run_in_executor(
            executor, render_video, request, cache
        )
        duration = time.time() - start_time

        logger.info(
            f"Render completed: job_id={request.job_id} "
            f"duration={duration:.2f}s frames={request.frame_count}"
        )

        return RenderResponse(
            status="completed",
            video_path=result["video_path"],
            metrics=result.get("metrics", []),
            duration_seconds=round(duration, 2),
        )

    except Exception as e:
        duration = time.time() - start_time
        logger.error(f"Render failed: job_id={request.job_id} error={e} duration={duration:.2f}s")
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail=f"Rendering failed: {str(e)}",
        )
