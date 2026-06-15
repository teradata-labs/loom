# Production Fly image: builds the real looms + loom-mcp (with -tags fts5) and
# runs both in one machine (looms on loopback; loom-mcp is the authenticated edge).
# NOTE: this is distinct from deploy/Dockerfile, which builds the benchmark binaries.

FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v3 \
    go build -tags fts5 -ldflags="-s -w" -o /out/looms    ./cmd/looms
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v3 \
    go build -tags fts5 -ldflags="-s -w" -o /out/loom-mcp ./cmd/loom-mcp

# alpine (not distroless) so the entrypoint shell can supervise two processes.
FROM alpine:3.20
# ca-certificates: outbound TLS to Supabase / Anthropic / Tavily / JWKS.
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 loom \
    && mkdir -p /etc/loom/backends && chown -R loom /etc/loom

COPY --from=builder /out/looms    /usr/local/bin/looms
COPY --from=builder /out/loom-mcp /usr/local/bin/loom-mcp
COPY deploy/fly/looms.yaml            /etc/loom/looms.yaml
COPY deploy/fly/looms.lab.yaml        /etc/loom/looms.lab.yaml
COPY deploy/fly/backends/supabase.yaml /etc/loom/backends/supabase.yaml
COPY deploy/fly/entrypoint.sh         /entrypoint.sh
RUN chmod +x /entrypoint.sh

USER loom
EXPOSE 8765
ENTRYPOINT ["/entrypoint.sh"]
