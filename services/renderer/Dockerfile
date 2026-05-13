# GeoStream Renderer Microservice
# Standalone FastAPI container for video rendering with tiered caching.

FROM python:3.11-slim

WORKDIR /app

# System dependencies for OpenCV and FFmpeg
RUN apt-get update && apt-get install -y --no-install-recommends \
    ffmpeg \
    libgl1 \
    libglib2.0-0 \
    && rm -rf /var/lib/apt/lists/*

# Python dependencies
COPY services/renderer/requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy renderer service code
COPY services/renderer/ .

# Copy shared orbital tracker module from scripts/
COPY scripts/orbital_tracker.py ./orbital_tracker.py

# Create output directories
RUN mkdir -p /app/videos /app/cache/wms

EXPOSE 8000

CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8000", "--workers", "2"]
