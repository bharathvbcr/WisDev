# Go build stage for the WisDev Agent OS orchestrator
FROM golang:1.25-bookworm AS go-builder

RUN apt-get update && apt-get install -y git ca-certificates && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace/orchestrator

# Copy module files first for layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . ./

# Build statically-linked binary with optimizations
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w -X main.version=$(date +%Y%m%d-%H%M%S)" \
    -o /wisdev-agent-orchestrator ./cmd/server

# Runtime stage - debian-slim to allow for pandoc
FROM debian:12-slim

# Install pandoc for Go-owned CSL/citation processing.
RUN apt-get update && apt-get install -y \
    pandoc \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=go-builder /wisdev-agent-orchestrator /wisdev-agent-orchestrator

# Cloud Run uses PORT env var
ENV PORT=8081

EXPOSE 8081 50053

# Create non-root user
RUN groupadd -r nonroot && useradd -r -g nonroot nonroot
USER nonroot:nonroot

ENTRYPOINT ["/wisdev-agent-orchestrator"]
