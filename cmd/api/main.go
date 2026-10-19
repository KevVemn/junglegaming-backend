package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/KevVemn/junglegaming-backend/internal/auth"
	"github.com/KevVemn/junglegaming-backend/internal/config"
	"github.com/KevVemn/junglegaming-backend/internal/httpapi"
	"github.com/KevVemn/junglegaming-backend/internal/messaging"
	"github.com/KevVemn/junglegaming-backend/internal/observability"
	"github.com/KevVemn/junglegaming-backend/internal/postgres"
	"github.com/KevVemn/junglegaming-backend/internal/service"
)

func main() {
	fx.New(
		configModule,
		observabilityModule,
		databaseModule,
		authModule,
		serviceModule,
		messagingModule,
		httpModule,
		fx.Invoke(func(*http.Server) {}),
	).Run()
}

var configModule = fx.Module("config",
	fx.Provide(
		config.Load,
		func(c *config.Config) config.DatabaseConfig { return c.Database },
		func(c *config.Config) config.AuthConfig { return c.Auth },
		func(c *config.Config) config.ServerConfig { return c.Server },
		func(c *config.Config) config.LogConfig { return c.Log },
		func(c *config.Config) config.SQSConfig { return c.SQS },
	),
)

var observabilityModule = fx.Module("observability",
	fx.Provide(observability.NewLogger, observability.NewMetrics),
	fx.WithLogger(func(logger *slog.Logger) fxevent.Logger {
		return &fxSlogLogger{logger: logger}
	}),
)

var databaseModule = fx.Module("database",
	fx.Provide(
		newPool,
		postgres.NewTxManager,
		postgres.NewWalletRepository,
		postgres.NewTransactionRepository,
		postgres.NewLedgerRepository,
		postgres.NewInboxRepository,
		postgres.NewOutboxRepository,
	),
)

var authModule = fx.Module("auth",
	fx.Provide(newVerifier),
)

var serviceModule = fx.Module("service",
	fx.Provide(
		service.NewWalletService,
		service.NewWageringService,
	),
)

var messagingModule = fx.Module("messaging",
	fx.Provide(
		newSQSClient,
		messaging.NewConsumer,
		messaging.NewOutboxPublisher,
		messaging.NewReferenceWorker,
	),
	fx.Invoke(registerConsumer, registerPublisher, registerReferenceWorker),
)

var httpModule = fx.Module("http",
	fx.Provide(
		newHealthChecker,
		httpapi.NewHandler,
		newHTTPServer,
	),
)

func newPool(lc fx.Lifecycle, cfg config.DatabaseConfig, logger *slog.Logger) (*pgxpool.Pool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := postgres.NewPool(ctx, cfg)
	if err != nil {
		return nil, err
	}

	lc.Append(fx.Hook{
		OnStop: func(context.Context) error {
			logger.Info("closing database pool")
			pool.Close()
			return nil
		},
	})

	return pool, nil
}

func newVerifier(lc fx.Lifecycle, cfg config.AuthConfig, logger *slog.Logger) (auth.Verifier, error) {
	if !cfg.Enabled {
		logger.Warn("authentication is disabled; every request runs unauthenticated")
		return auth.DisabledVerifier{}, nil
	}

	var verifier auth.Verifier

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			var lastErr error
			for attempt := 0; attempt < 10; attempt++ {
				oidcVerifier, err := auth.NewOIDCVerifier(startCtx, cfg)
				if err == nil {
					verifier = oidcVerifier
					logger.Info("oidc provider discovered", slog.String("issuer", cfg.IssuerURL))
					return nil
				}
				lastErr = err
				select {
				case <-startCtx.Done():
					return fmt.Errorf("oidc discovery cancelled: %w", lastErr)
				case <-time.After(2 * time.Second):
				}
			}
			return fmt.Errorf("oidc discovery failed: %w", lastErr)
		},
	})

	return &lazyVerifier{get: func() auth.Verifier { return verifier }}, nil
}

type lazyVerifier struct {
	get func() auth.Verifier
}

func (l *lazyVerifier) Verify(ctx context.Context, token string) (auth.Identity, error) {
	inner := l.get()
	if inner == nil {
		return auth.Identity{}, auth.ErrInvalidToken
	}
	return inner.Verify(ctx, token)
}

type httpVerifierAdapter struct {
	inner auth.Verifier
}

func (a httpVerifierAdapter) Verify(ctx context.Context, token string) (httpapi.Identity, error) {
	identity, err := a.inner.Verify(ctx, token)
	if err != nil {
		return httpapi.Identity{}, err
	}
	return httpapi.Identity{
		ProviderID: identity.ProviderID,
		Internal:   identity.Internal,
	}, nil
}

func newHealthChecker(pool *pgxpool.Pool, client *messaging.Client, cfg config.SQSConfig) *httpapi.HealthChecker {
	if cfg.QueueURL == "" {
		return httpapi.NewHealthChecker(pool, nil)
	}
	return httpapi.NewHealthChecker(pool, client)
}

func newSQSClient(cfg config.SQSConfig) (*messaging.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return messaging.NewClient(ctx, cfg)
}

func registerConsumer(
	lc fx.Lifecycle,
	consumer *messaging.Consumer,
	cfg config.SQSConfig,
	logger *slog.Logger,
) {
	if cfg.QueueURL == "" {
		logger.Warn("SQS_QUEUE_URL is empty; sqs consumer disabled")
		return
	}

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			consumer.Start(context.Background())
			return nil
		},
		OnStop: func(ctx context.Context) error {
			stopCtx, cancel := context.WithTimeout(ctx, cfg.ShutdownGracePeriod)
			defer cancel()
			return consumer.Stop(stopCtx)
		},
	})
}

func registerReferenceWorker(
	lc fx.Lifecycle,
	worker *messaging.ReferenceWorker,
	cfg config.ServerConfig,
) {
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			worker.Start(context.Background())
			return nil
		},
		OnStop: func(ctx context.Context) error {
			stopCtx, cancel := context.WithTimeout(ctx, cfg.ShutdownTimeout)
			defer cancel()
			return worker.Stop(stopCtx)
		},
	})
}

func registerPublisher(
	lc fx.Lifecycle,
	publisher *messaging.OutboxPublisher,
	cfg config.SQSConfig,
	logger *slog.Logger,
) {
	if cfg.EventsQueueURL == "" {
		logger.Warn("SQS_EVENTS_QUEUE_URL is empty; outbox publisher disabled")
		return
	}

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			publisher.Start(context.Background())
			return nil
		},
		OnStop: func(ctx context.Context) error {
			stopCtx, cancel := context.WithTimeout(ctx, cfg.ShutdownGracePeriod)
			defer cancel()
			return publisher.Stop(stopCtx)
		},
	})
}

func newHTTPServer(
	lc fx.Lifecycle,
	cfg config.ServerConfig,
	authCfg config.AuthConfig,
	handler *httpapi.Handler,
	verifier auth.Verifier,
	logger *slog.Logger,
) *http.Server {
	middlewares := []httpapi.Middleware{
		httpapi.CorrelationIDMiddleware(),
		httpapi.RecoveryMiddleware(logger),
		httpapi.LoggingMiddleware(logger),
	}
	if authCfg.Enabled {
		middlewares = append(middlewares,
			httpapi.AuthMiddleware(httpVerifierAdapter{inner: verifier},
				[]string{"/health", "/metrics"}))
	}

	routes := httpapi.Chain(handler.Routes(), middlewares...)

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      routes,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			listener, err := net.Listen("tcp", server.Addr)
			if err != nil {
				return fmt.Errorf("binding %s: %w", server.Addr, err)
			}

			logger.Info("http server listening", slog.String("addr", server.Addr))

			go func() {
				if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
					logger.Error("http server stopped", slog.String("error", err.Error()))
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			shutdownCtx, cancel := context.WithTimeout(ctx, cfg.ShutdownTimeout)
			defer cancel()

			logger.Info("shutting down http server")
			return server.Shutdown(shutdownCtx)
		},
	})

	return server
}
