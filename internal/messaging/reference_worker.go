package messaging

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/KevVemn/junglegaming-backend/internal/config"
	"github.com/KevVemn/junglegaming-backend/internal/service"
)

type ReferenceWorker struct {
	wagering *service.WageringService
	cfg      config.OutboxConfig
	logger   *slog.Logger
	wg       sync.WaitGroup
	cancel   context.CancelFunc
}

func NewReferenceWorker(
	wagering *service.WageringService,
	cfg *config.Config,
	logger *slog.Logger,
) *ReferenceWorker {
	return &ReferenceWorker{
		wagering: wagering,
		cfg:      cfg.Outbox,
		logger:   logger,
	}
}

func (w *ReferenceWorker) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	w.cancel = cancel

	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.run(ctx)
	}()

	w.logger.Info("pending reference worker started",
		slog.Duration("pollInterval", w.cfg.ReferencePoll),
		slog.Int("maxAttempts", w.cfg.ReferenceMax),
		slog.Duration("ttl", w.cfg.ReferenceTTL))
}

func (w *ReferenceWorker) Stop(ctx context.Context) error {
	if w.cancel == nil {
		return nil
	}
	w.cancel()

	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		w.logger.Info("pending reference worker stopped cleanly")
		return nil
	case <-ctx.Done():
		w.logger.Warn("pending reference worker shutdown deadline reached")
		return ctx.Err()
	}
}

func (w *ReferenceWorker) run(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.ReferencePoll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.sweep(ctx); err != nil && ctx.Err() == nil {
				w.logger.Error("pending reference sweep failed", slog.String("error", err.Error()))
			}
		}
	}
}

func (w *ReferenceWorker) sweep(ctx context.Context) error {
	keys, err := w.wagering.ClaimPendingReferences(ctx, w.cfg.BatchSize)
	if err != nil {
		return err
	}

	policy := service.ReferencePolicy{
		MaxAttempts: w.cfg.ReferenceMax,
		TTL:         w.cfg.ReferenceTTL,
		BaseBackoff: w.cfg.BaseBackoff,
	}

	for _, key := range keys {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		outcome, err := w.wagering.ResumePendingReference(ctx, key, policy)
		if err != nil {
			w.logger.Error("resuming pending reference failed",
				slog.String("idempotencyKey", key),
				slog.String("error", err.Error()))
			continue
		}

		switch outcome {
		case service.ResumeProcessed:
			w.logger.Info("pending reference resolved and processed",
				slog.String("idempotencyKey", key))
		case service.ResumeRejected:
			w.logger.Info("pending reference rejected",
				slog.String("idempotencyKey", key))
		}
	}

	return nil
}
