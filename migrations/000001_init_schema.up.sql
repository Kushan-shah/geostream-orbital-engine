CREATE TYPE job_status AS ENUM ('PENDING', 'PROCESSING', 'COMPLETED', 'FAILED', 'CANCELLED');

CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email VARCHAR(255) UNIQUE NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE jobs (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id               UUID NOT NULL REFERENCES users(id),
    status                job_status NOT NULL DEFAULT 'PENDING',

    -- Progress tracking
    frame_count           INT NOT NULL,
    processed_frames      INT NOT NULL DEFAULT 0,
    video_url             TEXT,
    activity_metrics      JSONB, -- Array of daily metric values for charting

    -- Idempotency (non-unique: allows re-submission of same parameters)
    request_hash          VARCHAR(64) NOT NULL,

    -- Retry & recovery
    retry_count           INT NOT NULL DEFAULT 0,
    worker_id             VARCHAR(64),
    lease_expiry          TIMESTAMPTZ,

    -- Timestamps
    processing_started_at TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at          TIMESTAMPTZ,
    cancelled_at          TIMESTAMPTZ,
    expires_at            TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '7 days',
    cancel_requested      BOOLEAN NOT NULL DEFAULT FALSE,

    -- Error tracking
    error_message         TEXT,

    -- Geospatial parameters
    bbox                  FLOAT8[],
    start_date            DATE,
    end_date              DATE,
    fps                   INT NOT NULL DEFAULT 30,

    -- Temporal and orbital config
    time_step             VARCHAR(10) DEFAULT '1d',
    track_satellite       VARCHAR(32) DEFAULT '',

    -- Dynamic WMS layer configuration
    wms_layers            JSONB DEFAULT '[]',

    -- Optimistic locking
    version               INT NOT NULL DEFAULT 0,

    -- Constraints
    CONSTRAINT valid_frame_count CHECK (frame_count > 0 AND frame_count <= 1800),
    CONSTRAINT valid_retry CHECK (retry_count >= 0 AND retry_count <= 5)
);

-- Indexes
CREATE INDEX idx_jobs_user_id ON jobs(user_id);
CREATE INDEX idx_jobs_status_created ON jobs(status, created_at);
CREATE INDEX idx_jobs_lease_recovery ON jobs(status, lease_expiry);
CREATE INDEX idx_jobs_expires_at ON jobs(expires_at);
CREATE INDEX idx_jobs_user_active ON jobs(user_id, status)
    WHERE status IN ('PENDING', 'PROCESSING');
CREATE INDEX idx_jobs_request_hash ON jobs(request_hash, status);
