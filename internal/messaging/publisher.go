package messaging

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/KevVemn/junglegaming-backend/internal/config"
	"github.com/KevVemn/junglegaming-backend/internal/observability"
	"github.com/KevVemn/junglegaming-backend/internal/postgres"
)

type OutboxPublisher struct {
	client  *Client
	tx      *postgres.TxManager
	outbox  *postgres.OutboxRepository
	cfg     config.OutboxConfig
	queue   string
	logger  *slog.Logger
	metrics *observability.Metrics
	wg      sync.WaitGroup
	cancel  context.CancelFunc
}

func NewOutboxPublisher(
	client *Client,
	tx *postgres.TxManager,
	outbox *postgres.OutboxRepository,
	cfg *config.Config,
	logger *slog.Logger,
	metrics *observability.Metrics,
) *OutboxPublisher {
	return &OutboxPublisher{
		client:  client,
		tx:      tx,
		outbox:  outbox,
		cfg:     cfg.Outbox,
		queue:   cfg.SQS.EventsQueueURL,
		logger:  logger,
		metrics: metrics,
	}
}

func (p *OutboxPublisher) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	p.cancel = cancel

	workers := p.cfg.WorkerCount
	if workers < 1 {
		workers = 1
	}

	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go func(id int) {
			defer p.wg.Done()
			p.run(ctx, id)
		}(i)
	}

	p.logger.Info("outbox publisher started", slog.Int("workers", workers))
}

func (p *OutboxPublisher) Stop(ctx context.Context) error {
	if p.cancel == nil {
		return nil
	}
	p.cancel()

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		p.logger.Info("outbox publisher stopped cleanly")
		return nil
	case <-ctx.Done():
		p.logger.Warn("outbox publisher shutdown deadline reached")
		return ctx.Err()
	}
}

func (p *OutboxPublisher) run(ctx context.Context, id int) {
	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.refreshLagMetrics(ctx)

			published, err := p.publishBatch(ctx)
			if err != nil && ctx.Err() == nil {
				p.logger.Error("outbox batch failed",
					slog.Int("worker", id), slog.String("error", err.Error()))
			}
			if published > 0 {
				p.logger.Debug("outbox batch published",
					slog.Int("worker", id), slog.Int("count", published))
			}
		}
	}
}

func (p *OutboxPublisher) publishBatch(ctx context.Context) (int, error) {
	now := time.Now().UTC()
	lockUntil := now.Add(p.cfg.LockDuration)

	var records []postgres.OutboxRecord
	err := p.tx.WithinTx(ctx, func(q postgres.Querier) error {
		claimed, err := p.outbox.ClaimBatch(ctx, q, p.cfg.BatchSize, now, lockUntil)
		if err != nil {
			return err
		}
		records = claimed
		return nil
	})
	if err != nil {
		return 0, err
	}

	published := 0
	for _, record := range records {
		if ctx.Err() != nil {
			return published, ctx.Err()
		}

		if err := p.publish(ctx, record); err != nil {
			p.metrics.OutboxPublishedTotal.WithLabelValues("failure").Inc()
			p.metrics.RetriesTotal.WithLabelValues("outbox_publish").Inc()
			p.markFailed(ctx, record, err)
			continue
		}
		p.metrics.OutboxPublishedTotal.WithLabelValues("success").Inc()

		markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		markErr := p.tx.WithinTx(markCtx, func(q postgres.Querier) error {
			return p.outbox.MarkPublished(ctx, q, record.EventID, time.Now().UTC())
		})
		cancel()

		if markErr != nil {
			p.logger.Error("event published but outbox not updated; it will be republished with the same eventId",
				slog.String("eventId", record.EventID.String()),
				slog.String("error", markErr.Error()))
			continue
		}
		published++
	}

	return published, nil
}

func (p *OutboxPublisher) publish(ctx context.Context, record postgres.OutboxRecord) error {
	if p.queue == "" {
		return nil
	}

	sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	_, err := p.client.SQS().SendMessage(sendCtx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(p.queue),
		MessageBody:            aws.String(string(record.Payload)),
		MessageGroupId:         aws.String(record.AggregateID.String()),
		MessageDeduplicationId: aws.String(record.EventID.String()),
	})
	return err
}

func (p *OutboxPublisher) markFailed(ctx context.Context, record postgres.OutboxRecord, cause error) {
	attempts := record.Attempts + 1
	backoff := time.Duration(math.Pow(2, float64(min(attempts, 10)))) * p.cfg.BaseBackoff
	nextRetry := time.Now().UTC().Add(backoff)

	failCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	err := p.tx.WithinTx(failCtx, func(q postgres.Querier) error {
		return p.outbox.MarkFailed(failCtx, q, record.EventID, nextRetry, cause.Error())
	})
	if err != nil {
		p.logger.Error("recording outbox failure failed", slog.String("error", err.Error()))
		return
	}

	level := slog.LevelWarn
	if attempts >= p.cfg.MaxAttempts {
		level = slog.LevelError
	}
	p.logger.Log(ctx, level, "outbox event publish failed",
		slog.String("eventId", record.EventID.String()),
		slog.String("eventType", record.EventType),
		slog.Int("attempts", attempts),
		slog.Time("nextRetryAt", nextRetry),
		slog.String("error", cause.Error()))
}

func (p *OutboxPublisher) refreshLagMetrics(ctx context.Context) {
	count, lag, err := p.PendingSnapshot(ctx)
	if err != nil {
		return
	}
	p.metrics.OutboxPendingEvents.Set(float64(count))
	p.metrics.OutboxLagSeconds.Set(lag.Seconds())
}

func (p *OutboxPublisher) PendingSnapshot(ctx context.Context) (int64, time.Duration, error) {
	var (
		count int64
		lag   time.Duration
	)

	err := p.tx.WithinTx(ctx, func(q postgres.Querier) error {
		c, err := p.outbox.PendingCount(ctx, q)
		if err != nil {
			return err
		}
		l, err := p.outbox.OldestPendingAge(ctx, q, time.Now().UTC())
		if err != nil {
			return err
		}
		count, lag = c, l
		return nil
	})

	return count, lag, err
}

func DecodePayload(payload []byte) (map[string]any, error) {
	var out map[string]any
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, err
	}
	return out, nil
}
