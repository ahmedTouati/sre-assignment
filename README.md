# OpenTelemetry Demo: Token Issuance Service

> **Note:** This is not production code. It is intended for learning, testing,
> and experimenting with OpenTelemetry observability patterns (metrics, logs,
> traces). Do not use in production environments.

## Architecture

```text
Client → Python (FastAPI) → PostgreSQL
              ↓ gRPC
         Golang → Redis
```

**Python Service** (port 8000): REST API that looks up user permissions in
PostgreSQL, then calls Go service to mint tokens. Exposes `/metrics` for
Prometheus.

**Go Service** (port 50051 gRPC, port 9090 HTTP): gRPC service that generates
tokens and stores them in Redis with TTL. Exposes `/metrics` on port 9090.

## Observability

Both services are pre-instrumented with OpenTelemetry:

<!-- markdownlint-disable MD013 -->

| Type        | Python                                      | Go                                 |
| ----------- | ------------------------------------------- | ---------------------------------- |
| **Metrics** | `http://localhost:8000/metrics`             | `http://localhost:9090/metrics`    |
| **Logging** | OTLP export with trace correlation          | OTLP export with trace correlation |
| **Tracing** | Auto-instrumented (FastAPI, gRPC, psycopg2) | Auto-instrumented (gRPC, Redis)    |

<!-- markdownlint-enable MD013 -->

Cross-service trace correlation uses W3C TraceContext propagation. When
`OTEL_EXPORTER_OTLP_ENDPOINT` is set, traces and logs are exported via OTLP
protocol.

## Project Structure

```text
.
├── protos/
│   └── token.proto          # gRPC service definition
├── python-service/
│   ├── main.py              # FastAPI application (with observability)
│   ├── pyproject.toml       # Python dependencies (uv)
│   ├── token_pb2.py         # Generated protobuf code (included)
│   └── token_pb2_grpc.py    # Generated gRPC code (included)
├── golang-service/
│   ├── main.go              # gRPC server (with observability)
│   ├── go.mod               # Go dependencies
│   └── proto/               # Generated protobuf code (included)
└── database/
    └── init.sql             # Schema and seed data
```

## Prerequisites

The services require the following dependencies:

- **PostgreSQL** — Database for user permissions (see `database/init.sql` for
  schema)
- **Redis** — Token storage with TTL support

## Setup

### 1. Initialize Database

Load the schema and seed data into PostgreSQL:

```bash
psql -h <host> -U <user> -d tokendb -f database/init.sql
```

### 2. Run the Services

**Go service:**

```bash
cd golang-service
export REDIS_ADDR=<redis-host>:6379
export REDIS_PASSWORD=<redis-password>
go run main.go
```

**Python service:**

```bash
cd python-service
uv sync
export DATABASE_URL=postgres://<user>:<password>@<host>:5432/tokendb
export TOKEN_SERVICE_HOST=localhost:50051
uv run uvicorn main:app --reload
```

### 3. Test

```bash
# Request a token
curl -X POST "http://localhost:8000/token" \
  -H "Content-Type: application/json" \
  -d '{"user_email": "alice@example.com"}'

# Check metrics
curl http://localhost:8000/metrics   # Python service
curl http://localhost:9090/metrics   # Go service
```

## Sample Data

| User  | Email               | Permissions            |
| ----- | ------------------- | ---------------------- |
| Alice | <alice@example.com> | read:data, write:data  |
| Bob   | <bob@example.com>   | read:data, admin:users |

## Environment Variables

**Python Service:**

- `DATABASE_URL` — PostgreSQL connection string (default:
  `postgres://postgres:postgres@localhost:5432/tokendb`)
- `TOKEN_SERVICE_HOST` — Go gRPC service address (default: `localhost:50051`)
- `OTEL_EXPORTER_OTLP_ENDPOINT` — OpenTelemetry collector endpoint (optional)

**Go Service:**

- `REDIS_ADDR` — Redis address (default: `localhost:6379`)
- `REDIS_PASSWORD` — Redis password (optional, no default)
- `OTEL_EXPORTER_OTLP_ENDPOINT` — OpenTelemetry collector endpoint (optional)

## Developers

If you modify `protos/token.proto`, regenerate and commit the generated code:

**Python:**

```bash
cd python-service
uv run python -m grpc_tools.protoc -I../protos --python_out=. \
  --grpc_python_out=. ../protos/token.proto
```

**Go:**

```bash
cd golang-service
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
protoc -I../protos --go_out=proto --go-grpc_out=proto \
  --go_opt=module=token-service/proto \
  --go-grpc_opt=module=token-service/proto \
  ../protos/token.proto
```
