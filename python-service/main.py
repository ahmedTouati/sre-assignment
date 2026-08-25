"""Token Issuance Service - with observability"""
import os, logging, grpc, psycopg2
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from opentelemetry import trace
from opentelemetry._logs import set_logger_provider
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.sdk._logs import LoggerProvider, LoggingHandler
from opentelemetry.sdk._logs.export import BatchLogRecordProcessor, ConsoleLogExporter
from opentelemetry.sdk.resources import Resource
from opentelemetry.propagate import set_global_textmap
from opentelemetry.propagators.composite import CompositePropagator
from opentelemetry.trace.propagation.tracecontext import TraceContextTextMapPropagator
from opentelemetry.instrumentation.fastapi import FastAPIInstrumentor
from opentelemetry.instrumentation.grpc import GrpcInstrumentorClient
from opentelemetry.instrumentation.psycopg2 import Psycopg2Instrumentor
from prometheus_fastapi_instrumentator import Instrumentator
import token_pb2, token_pb2_grpc

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
if otlp_endpoint:
    from opentelemetry.exporter.otlp.proto.grpc._log_exporter import OTLPLogExporter
    log_provider.add_log_record_processor(BatchLogRecordProcessor(OTLPLogExporter(endpoint=otlp_endpoint, insecure=True)))
else:
    log_provider.add_log_record_processor(BatchLogRecordProcessor(ConsoleLogExporter()))
set_logger_provider(log_provider)

# Configure Python logging to use OTel handler
handler = LoggingHandler(level=logging.INFO, logger_provider=log_provider)
logging.basicConfig(level=logging.INFO, handlers=[handler])
logger = logging.getLogger(__name__)

# Instrument gRPC client for trace propagation
GrpcInstrumentorClient().instrument()

# Instrument psycopg2 for automatic SQL tracing
Psycopg2Instrumentor().instrument()

class TokenRequest(BaseModel):
    user_email: str

app = FastAPI()

# Instrument FastAPI for automatic tracing
FastAPIInstrumentor.instrument_app(app)

# Instrument FastAPI for automatic Prometheus metrics (exposes /metrics)
Instrumentator().instrument(app).expose(app)

DB_URL = os.getenv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/tokendb")
GRPC_HOST = os.getenv("TOKEN_SERVICE_HOST", "localhost:50051")

@app.get("/health")
def health():
    return {"status": "healthy"}

@app.post("/token")
def request_token(req: TokenRequest):
    logger.info(f"Token request for {req.user_email}")

    conn = psycopg2.connect(DB_URL)
    cur = conn.cursor()
    cur.execute("""
        SELECT ARRAY_AGG(DISTINCT gp.permission)
        FROM users u JOIN memberships m ON u.id = m.user_id
        JOIN group_permissions gp ON m.group_id = gp.group_id
        WHERE u.email = %s
    """, (req.user_email,))
    row = cur.fetchone()
    conn.close()

    if not row or not row[0]:
        logger.info(f"User not found: {req.user_email}")
        raise HTTPException(status_code=404, detail="User not found")

    channel = grpc.insecure_channel(GRPC_HOST)
    stub = token_pb2_grpc.TokenServiceStub(channel)
    resp = stub.MintToken(token_pb2.MintTokenRequest(
        user_email=req.user_email, permissions=row[0], ttl_seconds=3600))

    logger.info(f"Token issued for {req.user_email}")
    return {"token": resp.token, "permissions": row[0]}
