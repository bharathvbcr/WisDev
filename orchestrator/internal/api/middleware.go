package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/llm"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/resilience"
	"github.com/wisdev/wisdev-agent-os/orchestrator/internal/stackconfig"
)

func shouldSkipResilienceProbe(path string) bool {
	if path == "/health" || path == "/healthz" || path == "/readiness" || path == "/metrics" {
		return true
	}
	return strings.HasPrefix(path, "/internal/")
}

const resilienceProbeInterval = 15 * time.Second

// PanicRecoveryMiddleware catches panics in downstream handlers, logs a
// structured error with the stack trace, and writes a proper 500 JSON envelope
// instead of letting net/http silently close the connection.
func PanicRecoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				stack := string(debug.Stack())
				slog.Error("panic recovered in HTTP handler",
					"component", "panic_recovery",
					"method", r.Method,
					"path", r.URL.Path,
					"panic", fmt.Sprint(recovered),
					"stack", stack,
					"trace_id", requestTraceIDFromContext(r.Context()),
				)
				WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, ErrInternal, "internal server error", map[string]any{
					"panic": fmt.Sprint(recovered),
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// RequestLogger logs method, path, status, and latency for every request.
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rw, r)

		latency := time.Since(start)
		slog.Info("request completed",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"latency_ms", latency.Milliseconds(),
			"trace_id", requestTraceIDFromContext(r.Context()),
			"user_agent", r.UserAgent(),
		)
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// CORSMiddleware handles Cross-Origin Resource Sharing.
func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Internal-Service-Key, X-Trace-Id, X-User-Id, X-User-Email")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// RequestTraceContextMiddleware stores the inbound request trace identifier in context so
// downstream middleware and error handlers can include it even when an active trace span
// is not present.
func RequestTraceContextMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := requestTraceIDFromRequest(r)
		ctx := r.Context()
		if traceID != "" {
			ctx = context.WithValue(ctx, ctxRequestTraceID, traceID)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ==========================================
// Resilience & Degraded Mode
// ==========================================

// ResilienceMiddleware checks for system degradation (e.g. Python sidecar down).
func ResilienceMiddleware(llmClient *llm.Client) func(http.Handler) http.Handler {
	var (
		mu           sync.RWMutex
		lastCheck    time.Time
		sidecarReady bool = true
	)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if shouldSkipResilienceProbe(r.URL.Path) {
				ctx := resilience.SetDegraded(r.Context(), false)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			mu.RLock()
			// Check health on a fixed interval to avoid probing on every request.
			checkNeeded := time.Since(lastCheck) > resilienceProbeInterval
			isDegraded := !sidecarReady
			mu.RUnlock()

			if checkNeeded {
				mu.Lock()
				// Double check after lock
				if time.Since(lastCheck) > resilienceProbeInterval {
					ready := true
					var err error
					if llmClient != nil {
						ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
						_, err = llmClient.Health(ctx)
						cancel()
						ready = (err == nil)
					}
					if ready != sidecarReady {
						if !ready {
							slog.Warn("system entering degraded mode: sidecar unavailable", "error", err)
						} else {
							slog.Info("system leaving degraded mode: sidecar restored")
						}
					}
					sidecarReady = ready
					isDegraded = !sidecarReady
					lastCheck = time.Now()
				}
				mu.Unlock()
			}

			ctx := resilience.SetDegraded(r.Context(), isDegraded)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func IsDegraded(ctx context.Context) bool {
	return resilience.IsDegraded(ctx)
}

// ==========================================
// Authentication Middleware
// ==========================================

// contextKey avoids collisions with other context value keys.
type contextKey string

const (
	ctxUserID         contextKey = "user_id"
	ctxUserEmail      contextKey = "user_email"
	ctxRequestTraceID contextKey = "request_trace_id"
)

func resolveInternalServiceKey() string {
	return stackconfig.ResolveInternalServiceKey()
}

// AuthMiddleware extracts authentication context from trusted internal headers.
func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requestTraceIDFromContext(r.Context()) == "" {
			if traceID := requestTraceIDFromRequest(r); traceID != "" {
				r = r.WithContext(context.WithValue(r.Context(), ctxRequestTraceID, traceID))
			}
		}
		if r.URL.Path == "/health" || r.URL.Path == "/wisdev-brain/runtime-health" || r.URL.Path == "/healthz" || r.URL.Path == "/readiness" || r.URL.Path == "/metrics" || r.URL.Path == "/runtime/health" {
			next.ServeHTTP(w, r)
			return
		}

		internalKey := resolveInternalServiceKey()
		isInternal := internalKey != "" && (r.Header.Get("X-Internal-Service-Key") == internalKey || r.Header.Get("Authorization") == "Bearer "+internalKey)

		var uid, email string
		if isInternal {
			// If request is internal/proxied, trust the headers
			uid = r.Header.Get("X-User-Id")
			email = r.Header.Get("X-User-Email")

			// If headers are missing, it's a direct system-to-system call
			if uid == "" {
				uid = "internal-service"
				email = "service@internal"
			}
		} else {
			// In development (no key), we might allow direct access with headers
			// but in production this is a security risk.
			if internalKey != "" {
				WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, ErrUnauthorized, "direct access forbidden; must go through gateway", nil)
				return
			}
			uid = r.Header.Get("X-User-Id")
			email = r.Header.Get("X-User-Email")
		}

		if uid == "" {
			WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, ErrUnauthorized, "missing authentication context from gateway", nil)
			return
		}

		ctx := context.WithValue(r.Context(), ctxUserID, uid)
		ctx = context.WithValue(ctx, ctxUserEmail, email)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// InternalServiceMiddleware enforces that requests to /internal/* must have a valid service key.
func InternalServiceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/internal/") {
			internalKey := resolveInternalServiceKey()
			if internalKey == "" {
				slog.Error("INTERNAL_SERVICE_KEY not configured, blocking internal route access")
				WriteErrorCtx(r.Context(), w, http.StatusForbidden, ErrForbidden, "internal routes disabled", nil)
				return
			}

			providedKey := r.Header.Get("X-Internal-Service-Key")
			if providedKey == "" {
				authHeader := r.Header.Get("Authorization")
				if strings.HasPrefix(authHeader, "Bearer ") {
					providedKey = strings.TrimPrefix(authHeader, "Bearer ")
				}
			}

			if providedKey != internalKey {
				slog.Warn("unauthorized access attempt to internal route", "path", r.URL.Path, "remote_addr", r.RemoteAddr)
				WriteErrorCtx(r.Context(), w, http.StatusForbidden, ErrForbidden, "access denied", nil)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func GetUserID(r *http.Request) string {
	if uid, ok := r.Context().Value(ctxUserID).(string); ok {
		return uid
	}
	return "anonymous"
}

func GetUserEmail(r *http.Request) string {
	if email, ok := r.Context().Value(ctxUserEmail).(string); ok {
		return email
	}
	return ""
}
