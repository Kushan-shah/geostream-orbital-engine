# Copyright 2026 Kushan Shah
# SPDX-License-Identifier: Apache-2.0

"""Pydantic schemas for the GeoStream Renderer Microservice.
These models auto-generate OpenAPI/Swagger documentation at /docs."""

from pydantic import BaseModel, Field
from typing import Optional


class WMSLayer(BaseModel):
    """A single WMS layer configuration."""
    url: str = Field(..., description="WMS endpoint URL")
    name: str = Field(..., description="Layer identifier (e.g., MODIS_Terra_CorrectedReflectance_TrueColor)")
    opacity: float = Field(default=1.0, ge=0.0, le=1.0, description="Layer opacity (0.0 = transparent, 1.0 = opaque)")


class RenderRequest(BaseModel):
    """Request payload for video rendering."""
    job_id: str = Field(..., description="Unique job UUID assigned by the orchestrator")
    bbox: list[float] = Field(..., min_length=4, max_length=4, description="Bounding box [minLng, minLat, maxLng, maxLat]")
    start_date: str = Field(..., description="Start date in YYYY-MM-DD format")
    end_date: str = Field(..., description="End date in YYYY-MM-DD format")
    fps: int = Field(default=30, ge=1, le=60, description="Frames per second")
    frame_count: int = Field(default=100, ge=1, le=1800, description="Total frames to generate")
    layers: list[WMSLayer] = Field(..., min_length=1, description="WMS layers to composite (first = base layer)")
    time_step: str = Field(default="1d", description="Temporal resolution (1d, 1h, 10m)")
    track_satellite: str = Field(default="", description="Satellite name for orbital tracking (e.g., ISS, TERRA)")
    output_path: str = Field(..., description="Output video file path")

    model_config = {
        "json_schema_extra": {
            "examples": [{
                "job_id": "550e8400-e29b-41d4-a716-446655440000",
                "bbox": [-122.4194, 37.7749, -122.3894, 37.8049],
                "start_date": "2026-01-01",
                "end_date": "2026-01-10",
                "fps": 30,
                "frame_count": 100,
                "layers": [{"url": "https://gibs.earthdata.nasa.gov/wms/epsg4326/best/wms.cgi", "name": "MODIS_Terra_CorrectedReflectance_TrueColor", "opacity": 1.0}],
                "time_step": "1d",
                "track_satellite": "",
                "output_path": "videos/550e8400.mp4"
            }]
        }
    }


class RenderResponse(BaseModel):
    """Response payload after video rendering completes."""
    status: str = Field(..., description="Render outcome: completed | failed")
    video_path: str = Field(..., description="Path to the generated MP4 file")
    metrics: list[float] = Field(default=[], description="Per-frame activity percentages")
    duration_seconds: float = Field(..., description="Total render wall-clock time in seconds")


class HealthResponse(BaseModel):
    """Health check response."""
    status: str = Field(..., description="Service health: healthy | degraded")
    cache_type: str = Field(..., description="Active cache tier: redis+disk | disk-only")
    version: str = Field(default="1.0.0", description="Service version")
