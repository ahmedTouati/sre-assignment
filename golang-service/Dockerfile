FROM golang:1.24-alpine AS build

WORKDIR /app

COPY go.mod go.sum ./
# Source changes can reuse the downloaded modules layer.
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -trimpath -o /token-service .

FROM alpine:3.22

RUN addgroup -S app && adduser -S -G app app

COPY --from=build /token-service /usr/local/bin/token-service

USER app

EXPOSE 50051 9090

# The metrics listener is enough for a basic liveness check.
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=5 \
    CMD wget -qO- http://127.0.0.1:9090/metrics >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/token-service"]
