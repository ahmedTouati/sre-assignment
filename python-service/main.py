"""Token Issuance Service - with observability"""
import logging
import os
import time
from contextlib import asynccontextmanager, contextmanager
from threading import Lock

import grpc
import psycopg2
from psycopg2.pool import ThreadedConnectionPool
from fastapi import FastAPI, HTTPException
from fastapi.responses import JSONResponse
from pydantic import BaseModel
from opentelemetry import trace
from opentelemetry._logs import set_logger_provider
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.sdk._logs import LoggerProvider, LoggingHandler
from opentelemetry.sdk._logs.export import (
    BatchLogRecordProcessor,
    ConsoleLogRecordExporter,
    SimpleLogRecordProcessor,
)
from opentelemetry.sdk.resources import Resource
from opentelemetry.propagate import set_global_textmap
from opentelemetry.propagators.composite import CompositePropagator
from opentelemetry.trace.propagation.tracecontext import TraceContextTextMapPropagator
from opentelemetry.instrumentation.fastapi import FastAPIInstrumentor
from opentelemetry.instrumentation.grpc import GrpcInstrumentorClient
from opentelemetry.instrumentation.psycopg2 import Psycopg2Instrumentor
from prometheus_client import Counter, Gauge, Histogram
from prometheus_fastapi_instrumentator import Instrumentator
import token_pb2
import token_pb2_grpc

# Set W3C Trace Context propagator for cross-service trace correlation
set_global_textmap(CompositePropagator([TraceContextTextMapPropagator()]))

# Create shared resource
resource = Resource.create({"service.name": "python-token-service"})

# Initialize OpenTelemetry Tracing
trace_provider = TracerProvider(resource=resource)
otlp_endpoint = os.getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
if otlp_endpoint:
    from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter
    trace_provider.add_span_processor(BatchSpanProcessor(OTLPSpanExporter(endpoint=otlp_endpoint, insecure=True)))
trace.set_tracer_provider(trace_provider)

# Initialize OpenTelemetry Logging
log_provider = LoggerProvider(resource=resource)
log_provider.add_log_record_processor(
    SimpleLogRecordProcessor(
        ConsoleLogRecordExporter(
            formatter=lambda record: record.to_json(indent=None) + "\n"
        )
    )
)
if otlp_endpoint:
    from opentelemetry.exporter.otlp.proto.grpc._log_exporter import OTLPLogExporter
    log_provider.add_log_record_processor(BatchLogRecordProcessor(OTLPLogExporter(endpoint=otlp_endpoint, insecure=True)))
set_logger_provider(log_provider)

# Configure Python logging to use OTel handler
handler = LoggingHandler(level=logging.INFO, logger_provider=log_provider)
handler.setFormatter(logging.Formatter("%(message)s"))
logging.basicConfig(level=logging.INFO, handlers=[handler], force=True)
for logger_name in ("uvicorn", "uvicorn.error", "uvicorn.access"):
    uvicorn_logger = logging.getLogger(logger_name)
    uvicorn_logger.handlers = [handler]
    uvicorn_logger.propagate = False
logger = logging.getLogger(__name__)

# Instrument gRPC client for trace propagation
GrpcInstrumentorClient().instrument()

# Instrument psycopg2 for automatic SQL tracing
Psycopg2Instrumentor().instrument()


class TokenRequest(BaseModel):
    user_email: str

DB_URL = os.getenv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/tokendb")
GRPC_HOST = os.getenv("TOKEN_SERVICE_HOST", "localhost:50051")
DB_POOL_MIN_SIZE = int(os.getenv("DB_POOL_MIN_SIZE", "1"))
DB_POOL_MAX_SIZE = int(os.getenv("DB_POOL_MAX_SIZE", "10"))
READINESS_TIMEOUT_SECONDS = 1
REQUEST_TIMEOUT_SECONDS = 5

postgres_pool_connections_in_use = Gauge(
    "postgres_pool_connections_in_use",
    "PostgreSQL connections currently checked out from the pool",
)
postgres_pool_connections_limit = Gauge(
    "postgres_pool_connections_limit",
    "Maximum PostgreSQL connections allowed by the pool",
)
postgres_pool_acquire_total = Counter(
    "postgres_pool_acquire_total",
    "PostgreSQL pool acquisition attempts",
    ["status"],
)
postgres_pool_acquire_duration = Histogram(
    "postgres_pool_acquire_duration_seconds",
    "Time spent acquiring a PostgreSQL connection",
)
postgres_pool_connections_limit.set(DB_POOL_MAX_SIZE)

_db_pool = None
_db_pool_lock = Lock()


def get_db_pool():
    global _db_pool
    if _db_pool is None:
        with _db_pool_lock:
            if _db_pool is None:
                _db_pool = ThreadedConnectionPool(
                    DB_POOL_MIN_SIZE,
                    DB_POOL_MAX_SIZE,
                    DB_URL,
                    connect_timeout=REQUEST_TIMEOUT_SECONDS,
                )
    return _db_pool


@contextmanager
def database_connection():
    started = time.perf_counter()
    try:
        pool = get_db_pool()
        conn = pool.getconn()
    except psycopg2.Error:
        postgres_pool_acquire_total.labels(status="error").inc()
        postgres_pool_acquire_duration.observe(time.perf_counter() - started)
        raise

    postgres_pool_acquire_total.labels(status="success").inc()
    postgres_pool_acquire_duration.observe(time.perf_counter() - started)
    postgres_pool_connections_in_use.inc()
    try:
        yield conn
    finally:
        discard = bool(conn.closed)
        if not discard:
            try:
                conn.rollback()
            except psycopg2.Error:
                discard = True
        try:
            pool.putconn(conn, close=discard)
        finally:
            postgres_pool_connections_in_use.dec()


@asynccontextmanager
async def lifespan(_app):
    yield
    if _db_pool is not None:
        _db_pool.closeall()


app = FastAPI(lifespan=lifespan)

# Instrument FastAPI for automatic tracing
FastAPIInstrumentor.instrument_app(app)

# Instrument FastAPI for automatic Prometheus metrics (exposes /metrics)
Instrumentator().instrument(
    app,
    latency_lowr_buckets=(0.1, 0.25, 0.5, 0.75, 1, 2.5, 5),
).expose(app)


def postgres_ready():
    try:
        with database_connection() as conn:
            with conn.cursor() as cur:
                cur.execute("SET LOCAL statement_timeout = 1000")
                cur.execute("SELECT 1 FROM users LIMIT 1")
        return True
    except psycopg2.Error:
        return False


def token_service_ready():
    channel = grpc.insecure_channel(GRPC_HOST)
    try:
        grpc.channel_ready_future(channel).result(timeout=READINESS_TIMEOUT_SECONDS)
        return True
    except (grpc.FutureTimeoutError, grpc.RpcError):
        return False
    finally:
        channel.close()


@app.get("/health")
def health():
    return {"status": "healthy"}


@app.get("/ready")
def ready():
    checks = {
        "postgresql": postgres_ready(),
        "go_service": token_service_ready(),
    }
    if not all(checks.values()):
        return JSONResponse(
            status_code=503,
            content={"status": "not ready", "checks": checks},
        )
    return {"status": "ready", "checks": checks}


@app.post("/token")
def request_token(req: TokenRequest):
    logger.info("token request received", extra={"event": "token.request.received"})

    try:
        with database_connection() as conn:
            with conn.cursor() as cur:
                cur.execute(
                    "SET LOCAL statement_timeout = %s",
                    (REQUEST_TIMEOUT_SECONDS * 1000,),
                )
                cur.execute("""
                    SELECT ARRAY_AGG(DISTINCT gp.permission)
                    FROM users u JOIN memberships m ON u.id = m.user_id
                    JOIN group_permissions gp ON m.group_id = gp.group_id
                    WHERE u.email = %s
                """, (req.user_email,))
                row = cur.fetchone()
    except psycopg2.Error as exc:
        logger.error(
            "database request failed",
            extra={"event": "dependency.error", "dependency": "postgresql"},
            exc_info=True,
        )
        raise HTTPException(status_code=503, detail="Database unavailable") from exc

    if not row or not row[0]:
        logger.info("user not found", extra={"event": "token.user_not_found"})
        raise HTTPException(status_code=404, detail="User not found")

    try:
        with grpc.insecure_channel(GRPC_HOST) as channel:
            stub = token_pb2_grpc.TokenServiceStub(channel)
            resp = stub.MintToken(
                token_pb2.MintTokenRequest(
                    user_email=req.user_email,
                    permissions=row[0],
                    ttl_seconds=3600,
                ),
                timeout=REQUEST_TIMEOUT_SECONDS,
            )
    except grpc.RpcError as exc:
        logger.error(
            "token service request failed",
            extra={
                "event": "dependency.error",
                "dependency": "go-token-service",
                "grpc_code": exc.code().name,
            },
            exc_info=True,
        )
        status_code = 504 if exc.code() == grpc.StatusCode.DEADLINE_EXCEEDED else 503
        raise HTTPException(status_code=status_code, detail="Token service unavailable") from exc

    logger.info(
        "token issued",
        extra={"event": "token.issued", "permission_count": len(row[0])},
    )
    return {"token": resp.token, "permissions": row[0]}
