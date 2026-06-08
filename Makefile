.PHONY: cli-help install-cli smoke-local smoke-run test-go test-wisdev test-python test-python-contract test-all tidy serve

.PHONY: gitnexus-index gitnexus-status gitnexus-refresh

cli-help:
	cd orchestrator && go run ./cmd/wisdev --help

install-cli:
	cd orchestrator && go install ./cmd/wisdev

smoke-local:
	cd orchestrator && go run ./cmd/wisdev yolo --local --offline --max-iterations 1 "map open source research agent evidence"

smoke-run:
	cd orchestrator && go run ./cmd/wisdev search --offline --max-iterations 1 "map open source research agent evidence"

serve:
	cd orchestrator && go run ./cmd/server

test-go:
	cd orchestrator && go test ./internal/api ./internal/search ./internal/wisdev ./internal/rag ./internal/evidence ./internal/evidence/citations ./internal/telemetry ./internal/stackconfig ./internal/cli ./cmd/server ./cmd/wisdev ./pkg/wisdev -count=1 -parallel=1

test-wisdev:
	cd orchestrator && go test ./internal/wisdev -count=1 -parallel=1

test-python-contract:
	cd sidecar && python -m pytest -q tests/unit/test_stack_contract.py

test-python:
	cd sidecar && python -m pytest -q tests/unit/test_stack_contract.py tests/unit/test_wisdev_prompts.py tests/unit/test_wisdev_action_router.py

test-all: test-go test-python-contract

tidy:
	cd orchestrator && go mod tidy

gitnexus-refresh:
	npx --yes -p node@22 -p gitnexus@1.6.3 gitnexus analyze . --skip-git --skip-agents-md

gitnexus-index: gitnexus-refresh

gitnexus-status:
	npx --yes -p node@22 -p gitnexus@1.6.3 gitnexus status
