# OpenTelemetry Demo: Token Issuance Service

> **Note:** This is not production code. It is intended for learning, testing,
> and experimenting with OpenTelemetry observability patterns (metrics, logs,
> traces). Do not use in production environments.

## Architecture

```mermaid
flowchart LR
    client["Client"] --> python["Python REST API"]
    python --> postgres[(PostgreSQL)]
    python -->|"gRPC + W3C trace context"| go["Go token service"]
    go --> redis[(Redis)]
    python -->|"OTLP logs and traces"| collector["OpenTelemetry Collector"]
    go -->|"OTLP logs and traces"| collector
    collector --> loki["Loki"]
    collector --> tempo["Tempo"]
    prometheus["Prometheus"] -->|"scrape"| python
    prometheus -->|"scrape"| go
    grafana["Grafana"] --> prometheus
    grafana --> loki
    grafana --> tempo
```

**Python Service** (port 8000): REST API that looks up user permissions in
PostgreSQL, then calls Go service to mint tokens. Exposes `/metrics` for
Prometheus, `/health` for liveness, and `/ready` for dependency readiness.

**Go Service** (port 50051 gRPC, port 9090 HTTP): gRPC service that generates
tokens and stores them in Redis with TTL. Exposes `/metrics` on port 9090.
The HTTP listener also exposes `/health` and a Redis-backed `/ready` check.

## Observability

Both services are pre-instrumented with OpenTelemetry:

<!-- markdownlint-disable MD013 -->

| Type        | Python                                      | Go                                 |
| ----------- | ------------------------------------------- | ---------------------------------- |
| **Metrics** | `http://localhost:8000/metrics`             | `http://localhost:9090/metrics`    |
| **Logging** | JSON stdout and OTLP with trace correlation | JSON stdout and OTLP with trace correlation |
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
├── charts/token-stack/      # Helm chart for Kubernetes
├── observability/           # Prometheus, Grafana, Loki, Tempo, and OTel
├── istio/                  # Optional service mesh profile and routes
├── argocd/                 # GitOps Application
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
    ├── init.sql             # Schema and seed data
    └── 002-app-role.sql     # Read-only application role
```

## Prerequisites

For local processes, install Go, uv/Python 3.12, PostgreSQL, and Redis. The
Kubernetes demo also needs Docker, kubectl, Helm, and Kind. `istioctl` and Argo
CD are needed only for their optional examples.

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
read -s "REDIS_PASSWORD?Redis password: "
echo
export REDIS_PASSWORD
go run main.go
```

**Python service:**

```bash
cd python-service
uv sync
read -s "PGPASSWORD?PostgreSQL password: "
echo
export PGPASSWORD
export DATABASE_URL="postgresql://<user>@<host>:5432/tokendb"
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

## Docker Compose

Copy the example environment file and set the PostgreSQL admin, application,
and Redis passwords:

```bash
cp .env.example .env
```

Then start the stack:

```bash
docker compose up --build
```

The API connects to PostgreSQL with a separate read-only role. Ports `8000` and
`9090` are available only on the local machine and can be changed in `.env`.

Stop the services with `docker compose down`. The database initialization
scripts run only when the PostgreSQL volume is empty. To reset all local data,
use `docker compose down -v`; this deletes both PostgreSQL and Redis volumes.

## Kubernetes with Helm

The chart deploys both applications with single-instance PostgreSQL and Redis.
It is intended for the assignment and local testing, not production HA.

```bash
kind create cluster --name token-stack
kubectl config current-context
```

The current context must be `kind-token-stack` before continuing.

```bash
docker build -t token-python-service:0.1.0 ./python-service
docker build -t token-go-service:0.1.0 ./golang-service
kind load docker-image token-python-service:0.1.0 --name token-stack
kind load docker-image token-go-service:0.1.0 --name token-stack
```

Create the namespace and Secret:

```bash
kubectl create namespace token-stack

read -s "POSTGRES_ADMIN_PASSWORD?PostgreSQL admin password: "; echo
read -s "POSTGRES_APP_PASSWORD?PostgreSQL application password: "; echo
read -s "REDIS_PASSWORD?Redis password: "; echo

kubectl -n token-stack create secret generic token-stack-credentials \
  --from-literal=postgres-admin-password="$POSTGRES_ADMIN_PASSWORD" \
  --from-literal=postgres-app-password="$POSTGRES_APP_PASSWORD" \
  --from-literal=redis-password="$REDIS_PASSWORD"

unset POSTGRES_ADMIN_PASSWORD POSTGRES_APP_PASSWORD REDIS_PASSWORD
```

Install the chart:

```bash
helm upgrade --install token-stack ./charts/token-stack \
  --namespace token-stack \
  --wait \
  --timeout 5m

kubectl -n token-stack get pods,services,pvc
```

Forward the API port:

```bash
kubectl -n token-stack port-forward service/token-stack-python 8000:8000
```

Test it from another terminal:

```bash
curl --fail http://127.0.0.1:8000/health
curl --fail http://127.0.0.1:8000/ready
curl -X POST http://127.0.0.1:8000/token \
  -H "Content-Type: application/json" \
  -d '{"user_email":"alice@example.com"}'
```

Remove the workloads with `helm uninstall token-stack -n token-stack`. PVCs are
retained; deleting them permanently removes the data. PostgreSQL initialization
runs only on an empty PVC, and changing the Secret does not rotate passwords
already stored in PostgreSQL.

## Observability stack

The applications expose Prometheus metrics and send logs and traces through the
OpenTelemetry Collector. Prometheus and Grafana handle metrics, Loki stores
logs, and Tempo stores traces. The values use small, single-replica deployments
for a local Kubernetes demo.

Install the pinned official charts:

```bash
helm repo add grafana-community https://grafana-community.github.io/helm-charts
helm repo add open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update

helm upgrade --install loki grafana-community/loki \
  --version 18.11.3 \
  --namespace observability \
  --create-namespace \
  --values observability/loki-values.yaml \
  --wait --timeout 5m

helm upgrade --install monitoring prometheus-community/kube-prometheus-stack \
  --version 88.5.4 \
  --namespace observability \
  --values observability/prometheus-values.yaml \
  --wait --timeout 10m

helm upgrade --install tempo grafana-community/tempo \
  --version 2.3.0 \
  --namespace observability \
  --values observability/tempo-values.yaml \
  --wait --timeout 5m

helm upgrade --install otel-collector open-telemetry/opentelemetry-collector \
  --version 0.171.0 \
  --namespace observability \
  --values observability/otel-collector-values.yaml \
  --wait --timeout 5m

kubectl apply -f observability/monitoring-resources.yaml
kubectl apply -f observability/grafana-dashboard.yaml
```

Connect the applications to the Collector:

```bash
helm upgrade --install token-stack ./charts/token-stack \
  --namespace token-stack \
  --set-string otel.endpoint=otel-collector.observability.svc.cluster.local:4317 \
  --wait --timeout 5m
```

Generate a request with a known trace ID:

```bash
kubectl -n token-stack port-forward service/token-stack-python 8000:8000
```

```bash
TRACE_ID=$(openssl rand -hex 16)
PARENT_SPAN_ID=$(openssl rand -hex 8)

curl -X POST http://127.0.0.1:8000/token \
  -H "Content-Type: application/json" \
  -H "traceparent: 00-${TRACE_ID}-${PARENT_SPAN_ID}-01" \
  -d '{"user_email":"alice@example.com"}'

echo "$TRACE_ID"
```

Open Grafana in another terminal:

```bash
kubectl -n observability port-forward service/monitoring-grafana 3000:80
kubectl -n observability get secret monitoring-grafana \
  -o jsonpath='{.data.admin-password}' | base64 --decode; echo
```

Sign in at `http://127.0.0.1:3000` as `admin`. The **Token Services**
dashboard shows request rate, error rate, latency, PostgreSQL pool usage, and
Redis operation depth and duration. In Explore, select Tempo and paste the
printed trace ID; one trace contains the FastAPI, PostgreSQL, gRPC, Go, and Redis
spans.

To prove cross-service log correlation, forward Loki and query the same trace:

```bash
kubectl -n observability port-forward service/loki 3100:3100
```

```bash
LOG_QUERY="{service_name=~\"python-token-service|go-token-service\"} | trace_id=\"${TRACE_ID}\""

curl -fsSG http://127.0.0.1:3100/loki/api/v1/query_range \
  --data-urlencode 'since=10m' \
  --data-urlencode 'limit=100' \
  --data-urlencode "query=${LOG_QUERY}"
```

The result contains both service names with one trace ID and different span
IDs. The same query works in Grafana Explore with the Loki data source.

The SLI rules use a 99% availability target. They alert on sustained HTTP or
gRPC errors, high p95 latency, PostgreSQL pool saturation, and Redis backlog.
Here `redis_queue_depth` means Redis commands currently awaiting completion;
this application does not use a Redis list or stream queue.

## Optional Kubernetes features

HPA and NetworkPolicies are included in the application chart but disabled by
default. HPA requires the Kubernetes resource metrics API.

```bash
helm upgrade token-stack ./charts/token-stack \
  --namespace token-stack \
  --reuse-values \
  --set python.autoscaling.enabled=true \
  --set go.autoscaling.enabled=true \
  --set networkPolicy.enabled=true

kubectl -n token-stack get hpa,networkpolicy
```

For the optional Istio mesh, install the supplied minimal profile before
deploying the application so every pod receives a sidecar:

```bash
istioctl install -f istio/profile.yaml -y
kubectl apply -f istio/resources.yaml
helm upgrade --install token-stack ./charts/token-stack \
  --namespace token-stack --wait
```

The mesh uses strict mTLS except on the two HTTP ports scraped by Prometheus,
which accept both mTLS and plaintext. It exposes `/token`, `/health`, and
`/ready` through the ingress gateway. If the workloads already exist, restart
both Deployments and StatefulSets after enabling injection.

Argo CD can reconcile the application chart after this repository is pushed:

```bash
kubectl apply -f argocd/application.yaml
```

Create `token-stack-credentials` first; credentials are intentionally not kept
in Git. Before using a remote cluster, point the chart image values at images
that cluster can pull; images loaded into Kind exist only in that Kind cluster.
The Argo CD Application enables HPA and NetworkPolicies, so its target cluster
also needs the resource metrics API. GitHub Actions runs Python and Go tests,
validates the Helm chart, and builds both images on every push and pull request.

## Design and production readiness

- One small first-party Helm chart keeps the application and its local backing
  stores easy to review. PostgreSQL and Redis are single-instance demo
  StatefulSets; production should use managed or operator-backed services.
- Workloads run as fixed non-root users with read-only filesystems, seccomp,
  dropped capabilities, no privilege escalation, and no service-account token.
  Secrets stay outside Git, and the API uses a read-only PostgreSQL role.
- Liveness checks only the process. Readiness checks PostgreSQL/gRPC for Python
  and Redis for Go, avoiding restart loops during dependency outages.
- Logs remain on JSON stdout and are also exported with OTLP. Trace and span IDs
  correlate Python and Go without logging tokens, passwords, or email addresses.
- RED metrics follow each service's API: HTTP method/status/latency for Python,
  gRPC method/code/latency for Go, and HTTP metrics for Go's health endpoints.
  The Go `/metrics` endpoint is excluded from its own request metrics.
- The PostgreSQL pool is bounded and connections are rolled back before reuse.
  SQL and gRPC calls have deadlines; pool size is reviewed with HPA limits and
  the database connection budget.
- Redis stores the email with a short-lived token because validation returns it.
  Production should use a token hash and an opaque user ID or encrypted value.
- The included storage, alert thresholds, CPU HPA, and single-window SLO rules
  are suitable for a demo. Production needs HA storage, backups, TLS for OTLP,
  multi-window burn-rate alerts, and an external secret manager.

## Sample Data

| User  | Email               | Permissions            |
| ----- | ------------------- | ---------------------- |
| Alice | <alice@example.com> | read:data, write:data  |
| Bob   | <bob@example.com>   | read:data, admin:users |

## Environment Variables

**Python Service:**

- `DATABASE_URL` — PostgreSQL connection string (default:
  `postgres://postgres:postgres@localhost:5432/tokendb`)
- `PGPASSWORD` — PostgreSQL password kept separate from the connection URL
- `TOKEN_SERVICE_HOST` — Go gRPC service address (default: `localhost:50051`)
- `DB_POOL_MIN_SIZE` / `DB_POOL_MAX_SIZE` — PostgreSQL pool bounds (defaults:
  `1` / `10`)
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
