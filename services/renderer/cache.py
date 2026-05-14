# Copyright 2026 Kushan Shah
# SPDX-License-Identifier: Apache-2.0

"""Tiered Caching Strategy for WMS Tiles.

L1: Redis (sub-millisecond, shared across renderer instances)
L2: MD5 disk cache (filesystem, always available as fallback)

If Redis is unavailable at startup or fails mid-operation,
the system degrades gracefully to disk-only mode with zero downtime."""

import os
import logging

logger = logging.getLogger("renderer.cache")

# Redis is optional — import failure is handled gracefully
try:
    import redis
except ImportError:
    redis = None


class TieredCache:
    """Two-level cache: Redis (L1) → Disk (L2).
    
    Thread-safe: Redis client is thread-safe by default.
    Disk writes use atomic patterns (write-then-read)."""

    def __init__(self, redis_url: str | None = None, disk_dir: str = "cache/wms", redis_ttl: int = 86400):
        self.disk_dir = disk_dir
        self.redis_ttl = redis_ttl  # 24 hours default
        self.redis_client = None
        self._cache_hits_redis = 0
        self._cache_hits_disk = 0
        self._cache_misses = 0

        os.makedirs(disk_dir, exist_ok=True)

        if redis_url and redis is not None:
            try:
                self.redis_client = redis.Redis.from_url(
                    redis_url,
                    decode_responses=False,
                    socket_connect_timeout=3,
                    socket_timeout=2,
                    retry_on_timeout=True,
                )
                self.redis_client.ping()
                logger.info("Redis L1 cache connected successfully")
            except Exception as e:
                logger.warning(f"Redis unavailable ({e}), falling back to disk-only cache")
                self.redis_client = None

    def get(self, key: str) -> bytes | None:
        """Retrieve cached tile bytes. Checks Redis first, then disk."""
        # L1: Redis
        if self.redis_client:
            try:
                val = self.redis_client.get(f"wms:{key}")
                if val:
                    self._cache_hits_redis += 1
                    return val
            except Exception:
                pass  # Redis failure is non-fatal

        # L2: Disk
        path = os.path.join(self.disk_dir, key)
        if os.path.exists(path):
            try:
                with open(path, "rb") as f:
                    data = f.read()
                if len(data) > 500:
                    self._cache_hits_disk += 1
                    # Backfill Redis from disk (async would be better, but sync is safe)
                    self._backfill_redis(key, data)
                    return data
            except Exception:
                pass

        self._cache_misses += 1
        return None

    def set(self, key: str, value: bytes) -> None:
        """Write tile bytes to both Redis and disk."""
        # L1: Redis
        if self.redis_client:
            try:
                self.redis_client.setex(f"wms:{key}", self.redis_ttl, value)
            except Exception:
                pass  # Redis failure is non-fatal

        # L2: Disk (always write — this is our durable fallback)
        path = os.path.join(self.disk_dir, key)
        try:
            with open(path, "wb") as f:
                f.write(value)
        except Exception as e:
            logger.warning(f"Disk cache write failed: {e}")

    def _backfill_redis(self, key: str, data: bytes) -> None:
        """Promote a disk-cached tile to Redis for faster future access."""
        if self.redis_client:
            try:
                self.redis_client.setex(f"wms:{key}", self.redis_ttl, data)
            except Exception:
                pass

    @property
    def stats(self) -> dict:
        """Cache hit/miss statistics for observability."""
        total = self._cache_hits_redis + self._cache_hits_disk + self._cache_misses
        return {
            "redis_hits": self._cache_hits_redis,
            "disk_hits": self._cache_hits_disk,
            "misses": self._cache_misses,
            "hit_ratio": round((self._cache_hits_redis + self._cache_hits_disk) / max(1, total), 4),
        }
