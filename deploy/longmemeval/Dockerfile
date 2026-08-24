# Builds the real loom server + LongMemEval harness for the AKS rig.
# Unlike deploy/Dockerfile (mock-LLM bench, CGO off), these binaries need
# CGO for sqlite FTS5, so the build stage is debian-based and the runtime
# keeps glibc + bash (the runner job is a shell slice-loop).
FROM golang:1.26-bookworm AS builder

ARG GIT_COMMIT=unknown

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=1 GOOS=linux go build -tags fts5 \
    -ldflags="-s -w -X main.gitCommit=${GIT_COMMIT}" \
    -o /looms ./cmd/looms

RUN CGO_ENABLED=1 GOOS=linux go build -tags fts5 \
    -ldflags="-s -w -X main.gitCommit=${GIT_COMMIT}" \
    -o /loom-longmemeval ./cmd/loom-longmemeval

FROM debian:bookworm-slim

# jq is required by the runner's chunk validation (deploy/longmemeval/runner-script.yaml)
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates jq \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /looms /looms
COPY --from=builder /loom-longmemeval /loom-longmemeval
