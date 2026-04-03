.PHONY: help proto proto-go proto-python build build-cli build-server test test-go test-python test-race tidy clean

help:
	@echo "WisDev Agent - Development Targets"
	@echo ""
	@echo "  Build:"
	@echo "    build          Build CLI and server binaries"
	@echo "    build-cli      Build CLI binary only"
	@echo "    build-server   Build server binary only"
	@echo ""
	@echo "  Test:"
	@echo "    test           Run all tests"
	@echo "    test-go        Run Go tests"
	@echo "    test-python    Run Python tests"
	@echo "    test-race      Run Go tests with race detector"
	@echo ""
	@echo "  Proto:"
	@echo "    proto          Generate protobuf stubs for Go and Python"
	@echo "    proto-go       Generate protobuf stubs for Go only"
	@echo "    proto-python   Generate protobuf stubs for Python only"
	@echo ""
	@echo "  Maintenance:"
	@echo "    tidy           Tidy Go module dependencies"
	@echo "    clean          Remove generated files and binaries"

# ── Build ─────────────────────────────────────────────────────────────────────
build: build-cli build-server

build-cli:
	@mkdir -p bin
	cd orchestrator && go build -ldflags="-s -w -X main.version=$$(git describe --tags --always 2>/dev/null || echo dev)" -o ../bin/wisdev ./cmd/wisdev

build-server:
	@mkdir -p bin
	cd orchestrator && go build -ldflags="-s -w -X main.version=$$(git describe --tags --always 2>/dev/null || echo dev)" -o ../bin/wisdev-server ./cmd/server

# ── Test ──────────────────────────────────────────────────────────────────────
test: test-go test-python

test-go:
	cd orchestrator && go test ./...

test-race:
	cd orchestrator && go test -race ./...

test-python:
	cd sidecar && python -m pytest

# ── Protobuf ──────────────────────────────────────────────────────────────────
proto: proto-go proto-python

proto-go:
	cd proto && protoc --go_out=orchestrator --go_opt=module=github.com/wisdev-agent/wisdev-agent-os/orchestrator --go-grpc_out=orchestrator --go-grpc_opt=module=github.com/wisdev-agent/wisdev-agent-os/orchestrator -I. proto/v2/wisdev_v2.proto proto/llm/v1/llm_v1.proto

proto-python:
	cd proto && protoc --python_out=../sidecar/proto --grpc_python_out=../sidecar/proto -I. proto/v2/wisdev_v2.proto proto/llm/v1/llm_v1.proto

# ── Maintenance ───────────────────────────────────────────────────────────────
tidy:
	cd orchestrator && go mod tidy

clean:
	rm -rf bin/
	rm -f orchestrator/proto/v2/*.pb.go
	rm -f orchestrator/proto/v2/*.grpc.pb.go
	rm -f orchestrator/proto/llm/v1/*.pb.go
	rm -f orchestrator/proto/llm/v1/*.grpc.pb.go
	rm -f sidecar/proto/*_pb2.py
	rm -f sidecar/proto/*_pb2_grpc.py
	rm -f sidecar/sidecar_proto/*_pb2.py
	rm -f sidecar/sidecar_proto/*_pb2_grpc.py
	find sidecar -type d -name __pycache__ -exec rm -rf {} + 2>/dev/null || true
	find sidecar -name "*.pyc" -delete 2>/dev/null || true
