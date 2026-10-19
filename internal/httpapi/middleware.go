package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

type contextKey string

const (
	correlationIDKey contextKey = "correlationId"
	providerIDKey    contextKey = "providerId"
	internalKey      contextKey = "internalCaller"
)

func CorrelationIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(correlationIDKey).(string); ok {
		return v
	}
	return ""
}

func ProviderIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(providerIDKey).(string); ok {
		return v
	}
	return ""
}

func WithProviderID(ctx context.Context, providerID string) context.Context {
	return context.WithValue(ctx, providerIDKey, providerID)
}

func IsInternalCaller(ctx context.Context) bool {
	v, ok := ctx.Value(internalKey).(bool)
	return ok && v
}

func WithInternalCaller(ctx context.Context, internal bool) context.Context {
	return context.WithValue(ctx, internalKey, internal)
}

type Middleware func(http.Handler) http.Handler

func Chain(h http.Handler, middlewares ...Middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}

func CorrelationIDMiddleware() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			correlationID := r.Header.Get("X-Correlation-Id")
			if correlationID == "" {
				correlationID = uuid.NewString()
			}

			ctx := context.WithValue(r.Context(), correlationIDKey, correlationID)
			w.Header().Set("X-Correlation-Id", correlationID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

func LoggingMiddleware(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(recorder, r)

			logger.LogAttrs(r.Context(), slog.LevelInfo, "http_request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", recorder.status),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
				slog.String("correlationId", CorrelationIDFrom(r.Context())),
				slog.String("providerId", ProviderIDFrom(r.Context())),
			)
		})
	}
}

func RecoveryMiddleware(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.LogAttrs(r.Context(), slog.LevelError, "panic_recovered",
						slog.Any("panic", rec),
						slog.String("path", r.URL.Path),
						slog.String("correlationId", CorrelationIDFrom(r.Context())),
					)
					writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR",
						"unexpected error while processing the request")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type Identity struct {
	ProviderID string
	Internal   bool
}

type TokenVerifier interface {
	Verify(ctx context.Context, bearerToken string) (Identity, error)
}

func AuthMiddleware(verifier TokenVerifier, publicPrefixes []string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, prefix := range publicPrefixes {
				if len(r.URL.Path) >= len(prefix) && r.URL.Path[:len(prefix)] == prefix {
					next.ServeHTTP(w, r)
					return
				}
			}

			header := r.Header.Get("Authorization")
			const bearer = "Bearer "
			if len(header) <= len(bearer) || header[:len(bearer)] != bearer {
				writeError(w, r, http.StatusUnauthorized, "MISSING_TOKEN",
					"authorization header with bearer token is required")
				return
			}

			identity, err := verifier.Verify(r.Context(), header[len(bearer):])
			if err != nil {
				writeError(w, r, http.StatusUnauthorized, "INVALID_TOKEN", "token is invalid or expired")
				return
			}

			ctx := WithProviderID(r.Context(), identity.ProviderID)
			ctx = WithInternalCaller(ctx, identity.Internal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
