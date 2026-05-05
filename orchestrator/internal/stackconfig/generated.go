// Code generated for WisDev Agent OS open-source stack. DO NOT EDIT.
package stackconfig

const generatedManifestJSON = `{
  "version": 4,
  "environment": "open-source",
  "canonicalRequestFlow": ["go_orchestrator", "python_sidecar"],
  "ports": {
    "go_orchestrator": 8081,
    "python_sidecar_http": 8090,
    "python_sidecar_grpc": 50052,
    "go_internal_grpc": 50053,
    "go_metrics": 9090
  },
  "services": {
    "go_orchestrator": {
      "displayName": "WisDev Go Orchestrator",
      "path": "orchestrator",
      "entrypoint": "orchestrator/cmd/server/server.go",
      "transport": "http-json",
      "defaultBaseUrl": "http://127.0.0.1:8081",
      "healthRoutes": ["/health", "/healthz", "/readiness", "/runtime/health", "/metrics"],
      "requiredEnv": ["PYTHON_SIDECAR_HTTP_URL", "PYTHON_SIDECAR_LLM_TRANSPORT", "PYTHON_SIDECAR_GRPC_ADDR", "GO_INTERNAL_GRPC_ADDR"],
      "listenPorts": {"http": 8081, "metrics": 9090, "grpc": 50053}
    },
    "python_sidecar": {
      "displayName": "WisDev Python ML Sidecar",
      "path": "sidecar",
      "entrypoint": "sidecar/main.py",
      "transport": "http-json+grpc-protobuf",
      "defaultBaseUrl": "http://127.0.0.1:8090",
      "healthRoutes": ["/health", "/readiness", "/metrics"],
      "requiredEnv": ["PYTHON_SIDECAR_GRPC_ADDR"],
      "listenPorts": {"http": 8090, "grpc": 50052}
    }
  },
  "dependencies": [
    {"from": "go_orchestrator", "to": "python_sidecar", "transport": "grpc-protobuf", "description": "Go uses gRPC for optional local/container LLM sidecar RPCs."},
    {"from": "go_orchestrator", "to": "python_sidecar", "transport": "http-json", "description": "Go uses HTTP for optional ML primitives and remote LLM RPCs."}
  ],
  "httpRoutes": {
    "go_orchestrator": ["/health", "/healthz", "/readiness", "/runtime/health", "/metrics", "/search/*", "/wisdev/*", "/rag/*", "/paper/*", "/papers/*", "/topic-tree/*"],
    "python_sidecar": ["/health", "/readiness", "/metrics", "/llm/generate", "/llm/generate/stream", "/llm/structured-output", "/llm/embed", "/llm/embed/batch", "/llm/health", "/ml/pdf", "/ml/embed", "/ml/bm25/index", "/ml/bm25/search", "/skills/register"]
  },
  "grpcTargets": {
    "python_sidecar": {"envVar": "PYTHON_SIDECAR_GRPC_ADDR", "transport": "grpc-protobuf", "source": "go_orchestrator", "target": "python_sidecar", "defaultAddress": "127.0.0.1:50052"},
    "go_internal": {"envVar": "GO_INTERNAL_GRPC_ADDR", "transport": "grpc-protobuf", "source": "go_orchestrator", "target": "go_orchestrator", "defaultAddress": "127.0.0.1:50053"}
  },
  "authMode": {
    "go_orchestrator_to_python_sidecar": "internal_service_key_or_local"
  },
  "requiredEnv": ["PYTHON_SIDECAR_HTTP_URL", "PYTHON_SIDECAR_LLM_TRANSPORT", "PYTHON_SIDECAR_GRPC_ADDR", "GO_INTERNAL_GRPC_ADDR"],
  "canonicalEnvVars": ["PYTHON_SIDECAR_HTTP_URL", "PYTHON_SIDECAR_LLM_TRANSPORT", "PYTHON_SIDECAR_GRPC_ADDR", "GO_INTERNAL_GRPC_ADDR", "INTERNAL_SERVICE_KEY"],
  "overlays": {
    "local": {
      "env": {
        "PYTHON_SIDECAR_HTTP_URL": "http://127.0.0.1:8090",
        "PYTHON_SIDECAR_LLM_TRANSPORT": "grpc",
        "PYTHON_SIDECAR_GRPC_ADDR": "127.0.0.1:50052",
        "GO_INTERNAL_GRPC_ADDR": "127.0.0.1:50053",
        "INTERNAL_SERVICE_KEY": "dev-internal-key"
      },
      "serviceBaseUrls": {
        "go_orchestrator": "http://127.0.0.1:8081",
        "python_sidecar": "http://127.0.0.1:8090"
      }
    },
    "docker": {
      "env": {
        "PYTHON_SIDECAR_HTTP_URL": "http://sidecar:8090",
        "PYTHON_SIDECAR_LLM_TRANSPORT": "grpc",
        "PYTHON_SIDECAR_GRPC_ADDR": "sidecar:50052",
        "GO_INTERNAL_GRPC_ADDR": "0.0.0.0:50053",
        "INTERNAL_SERVICE_KEY": "dev-internal-key"
      },
      "serviceBaseUrls": {
        "go_orchestrator": "http://127.0.0.1:8081",
        "python_sidecar": "http://127.0.0.1:8090"
      }
    },
    "cloudrun": {
      "env": {
        "PYTHON_SIDECAR_HTTP_URL": "https://your-python-sidecar.run.app",
        "PYTHON_SIDECAR_LLM_TRANSPORT": "http-json",
        "PYTHON_SIDECAR_GRPC_ADDR": "your-python-sidecar:50052",
        "GO_INTERNAL_GRPC_ADDR": "0.0.0.0:50053",
        "INTERNAL_SERVICE_KEY": "resolve-from-secret-manager"
      },
      "serviceBaseUrls": {
        "go_orchestrator": "https://your-go-orchestrator.run.app",
        "python_sidecar": "https://your-python-sidecar.run.app"
      }
    },
    "azure_container_apps": {
      "env": {
        "PYTHON_SIDECAR_HTTP_URL": "https://your-python-sidecar.azurecontainerapps.io",
        "PYTHON_SIDECAR_LLM_TRANSPORT": "http-json",
        "PYTHON_SIDECAR_GRPC_ADDR": "python-sidecar:50052",
        "GO_INTERNAL_GRPC_ADDR": "0.0.0.0:50053",
        "INTERNAL_SERVICE_KEY": "resolve-from-key-vault"
      },
      "serviceBaseUrls": {
        "go_orchestrator": "https://your-go-orchestrator.azurecontainerapps.io",
        "python_sidecar": "https://your-python-sidecar.azurecontainerapps.io"
      }
    }
  },
  "legacyEnvVars": []
}`

var Manifest = mustParseManifest(generatedManifestJSON)
