.PHONY: cli-help install-cli check demo tui tui-demo smoke-local smoke-run smoke-mcp build-cli test-go test-wisdev test-python test-python-contract test-all tidy serve wisdev

WISDEV := node scripts/run-wisdev.mjs

cli-help:
	$(WISDEV) --help

install-cli:
	cd orchestrator && go install ./cmd/wisdev

check:
	$(WISDEV) check

demo:
	$(WISDEV) demo --offline

tui:
	$(WISDEV) tui

tui-demo:
	$(WISDEV) tui --demo --autostart

wisdev:
	$(WISDEV) $(ARGS)

smoke-local:
	$(WISDEV) --offline --max-iterations 1 "map open source research agent evidence"

smoke-run: smoke-local

smoke-mcp:
	cd orchestrator && go test ./internal/wisdev -run TestMCPServerStdio -count=1

doctor-cli: check

demo-cli: demo

build-cli:
	powershell -ExecutionPolicy Bypass -File ./scripts/build-wisdev-cli.ps1 -Version 0.1.0-hackathon

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
