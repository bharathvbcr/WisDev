import structlog
from slowapi import Limiter
from slowapi.util import get_remote_address

logger = structlog.get_logger(__name__)

limiter = Limiter(key_func=get_remote_address)
logger.info("rate_limiter_configured", storage="memory")
