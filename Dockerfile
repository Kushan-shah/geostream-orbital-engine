# Build Stage
FROM golang:alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o geostream-api ./cmd/server

# Final Stage
FROM python:3.11-slim

WORKDIR /app

# Install system dependencies for OpenCV and FFmpeg
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    tzdata \
    ffmpeg \
    libgl1 \
    libglib2.0-0 \
    && rm -rf /var/lib/apt/lists/*

RUN pip install --no-cache-dir opencv-python-headless numpy requests sgp4

# Copy Go binary from builder
COPY --from=builder /app/geostream-api .
COPY --from=builder /app/scripts/ ./scripts/
COPY --from=builder /app/migrations/ ./migrations/

RUN mkdir -p /app/videos

EXPOSE 8080

CMD ["./geostream-api"]
