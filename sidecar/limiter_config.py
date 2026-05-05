import os
import structlog
from slowapi import Limiter
from slowapi.util import get_remote_address

logger = structlog.get_logger(__name__)

# Rate limiter
redis_url = os.environ.get("UPSTASH_REDIS_URL")
if redis_url:
    # Upstash uses a proprietary "upstash://" scheme; translate it to
    # "rediss://" (Redis-over-TLS) which slowapi's storage backend understands.
    if redis_url.startswith("upstash://"):
        redis_url = "rediss://" + redis_url[len("upstash://"):]

    limiter = Limiter(
        key_func=get_remote_address,
        storage_uri=redis_url,
        strategy="fixed-window",  # or "moving-window"
    )
    logger.info("rate_limiter_configured", storage="redis", url_masked=redis_url[:15] + "...")
else:
    # Fallback to memory (not suitable for horizontal scaling)
    limiter = Limiter(key_func=get_remote_address)
    logger.warning("rate_limiter_configured", storage="memory", warning="Not suitable for scaling")
