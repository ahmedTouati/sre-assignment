FROM golang:1.24-alpine AS build

WORKDIR /app

COPY go.mod go.sum ./
# Source changes can reuse the downloaded modules layer.
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /token-service .

FROM alpine:3.22

RUN addgroup -g 10001 app && adduser -D -H -u 10001 -G app app

COPY --from=build --chown=10001:10001 /token-service /usr/local/bin/token-service

USER 10001:10001

EXPOSE 50051 9090

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=5 \
    CMD wget -qO- http://127.0.0.1:9090/ready >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/token-service"]
