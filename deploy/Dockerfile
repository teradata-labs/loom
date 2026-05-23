FROM golang:1.25-alpine AS builder

ARG GIT_COMMIT=unknown

WORKDIR /src

# Cache dependency downloads
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build both binaries
COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v3 go build \
    -ldflags="-s -w -X main.gitCommit=${GIT_COMMIT}" \
    -o /loom-bench-server ./cmd/loom-bench-server

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v3 go build \
    -ldflags="-s -w -X main.gitCommit=${GIT_COMMIT}" \
    -o /loom-bench-harness ./cmd/loom-bench-harness

# Runtime image: minimal, no shell, no libc
FROM gcr.io/distroless/static-debian12

COPY --from=builder /loom-bench-server /loom-bench-server
COPY --from=builder /loom-bench-harness /loom-bench-harness
