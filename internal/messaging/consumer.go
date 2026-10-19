package messaging

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/KevVemn/junglegaming-backend/internal/config"
	"github.com/KevVemn/junglegaming-backend/internal/domain"
	"github.com/KevVemn/junglegaming-backend/internal/observability"
	"github.com/KevVemn/junglegaming-backend/internal/postgres"
	"github.com/KevVemn/junglegaming-backend/internal/service"
)

type Consumer struct {
	client   *Client
	wagering *service.WageringService
	cfg      config.SQSConfig
	logger   *slog.Logger
	metrics  *observability.Metrics
	wg       sync.WaitGroup
	cancel   context.CancelFunc
}

func NewConsumer(
	client *Client,
	wagering *service.WageringService,
	cfg config.SQSConfig,
	logger *slog.Logger,
	metrics *observability.Metrics,
) *Consumer {
	return &Consumer{
		client:   client,
		wagering: wagering,
		cfg:      cfg,
		logger:   logger,
		metrics:  metrics,
	}
}

func (c *Consumer) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel

	workers := c.cfg.WorkerCount
	if workers < 1 {
		workers = 1
	}

	for i := 0; i < workers; i++ {
		c.wg.Add(1)
		go func(id int) {
			defer c.wg.Done()
			c.runWorker(ctx, id)
		}(i)
	}

	c.logger.Info("sqs consumer started",
		slog.Int("workers", workers),
		slog.String("queue", c.cfg.QueueURL))
}

func (c *Consumer) Stop(ctx context.Context) error {
	if c.cancel == nil {
		return nil
	}

	c.logger.Info("stopping sqs consumer, waiting for in-flight messages")
	c.cancel()

	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		c.logger.Info("sqs consumer stopped cleanly")
		return nil
	case <-ctx.Done():
		c.logger.Warn("sqs consumer shutdown deadline reached; in-flight messages will be redelivered")
		return ctx.Err()
	}
}

func (c *Consumer) runWorker(ctx context.Context, id int) {
	for {
		if ctx.Err() != nil {
			return
		}

		messages, err := c.receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.logger.Error("receiving messages failed",
				slog.Int("worker", id), slog.String("error", err.Error()))
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}

		for _, message := range messages {
			c.handle(ctx, message)
		}
	}
}

func (c *Consumer) receive(ctx context.Context) ([]types.Message, error) {
	out, err := c.client.SQS().ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:                    aws.String(c.cfg.QueueURL),
		MaxNumberOfMessages:         c.cfg.MaxMessages,
		WaitTimeSeconds:             c.cfg.WaitTimeSeconds,
		VisibilityTimeout:           c.cfg.VisibilityTimeout,
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll},
	})
	if err != nil {
		return nil, err
	}
	return out.Messages, nil
}

func (c *Consumer) handle(ctx context.Context, message types.Message) {
	c.metrics.MessagesInFlight.Inc()
	defer c.metrics.MessagesInFlight.Dec()

	body := aws.ToString(message.Body)

	parsed, err := ParseInbound(body)
	if err != nil {
		c.logger.Error("malformed message sent to dlq",
			slog.String("sqsMessageId", aws.ToString(message.MessageId)),
			slog.String("error", err.Error()))
		c.metrics.DLQMessagesTotal.WithLabelValues("malformed").Inc()
		c.sendToDLQ(ctx, message, err.Error())
		c.delete(ctx, message)
		return
	}

	logger := c.logger.With(
		slog.String("messageId", parsed.Envelope.MessageID),
		slog.String("providerId", parsed.Envelope.Data.ProviderID),
		slog.String("walletId", parsed.Envelope.Data.WalletID),
	)

	result, err := c.wagering.Submit(ctx, service.SubmitCommand{
		ProviderID:            parsed.Envelope.Data.ProviderID,
		ExternalTransactionID: parsed.Envelope.Data.ExternalTransactionID,
		IdempotencyKey:        parsed.Envelope.Data.IdempotencyKey,
		PlayerID:              parsed.PlayerID,
		WalletID:              parsed.WalletID,
		RoundID:               parsed.Envelope.Data.RoundID,
		GameID:                parsed.Envelope.Data.GameID,
		Kind:                  parsed.Kind,
		Money:                 parsed.Envelope.Data.Money,
		ReferenceExternalID:   parsed.Envelope.Data.ReferenceExternalID,
		CorrelationID:         parsed.Envelope.MessageID,
		Inbox: &service.InboxClaim{
			ConsumerName: c.cfg.ConsumerName,
			MessageID:    parsed.Envelope.MessageID,
			MessageHash:  parsed.BodyHash,
		},
	})

	switch {
	case err == nil:
		if result.IdempotentReplay {
			logger.Info("duplicate message replayed from stored result",
				slog.String("transactionId", result.TransactionID.String()),
				slog.String("status", string(result.State)))
		} else {
			logger.Info("message processed",
				slog.String("transactionId", result.TransactionID.String()),
				slog.String("status", string(result.State)))
		}
		c.delete(ctx, message)

	case errors.Is(err, postgres.ErrInboxDuplicate):
		logger.Info("duplicate message ignored")
		c.delete(ctx, message)

	case errors.Is(err, postgres.ErrInboxHashMismatch):
		logger.Error("message id reused with different payload, sent to dlq")
		c.metrics.DLQMessagesTotal.WithLabelValues("hash_mismatch").Inc()
		c.sendToDLQ(ctx, message, "inbox hash mismatch")
		c.delete(ctx, message)

	case isPermanent(err):
		c.metrics.DLQMessagesTotal.WithLabelValues("permanent_failure").Inc()
		logger.Error("permanent failure, message deleted",
			slog.String("error", err.Error()))
		c.sendToDLQ(ctx, message, err.Error())
		c.delete(ctx, message)

	default:
		c.metrics.RetriesTotal.WithLabelValues("sqs_redelivery").Inc()
		logger.Warn("transient failure, message will be redelivered",
			slog.String("error", err.Error()))
	}
}

func isPermanent(err error) bool {
	switch {
	case errors.Is(err, service.ErrIdempotencyConflict),
		errors.Is(err, service.ErrWalletMismatch),
		errors.Is(err, service.ErrProviderForbidden),
		errors.Is(err, postgres.ErrWalletNotFound),
		errors.Is(err, domain.ErrInvalidTransactionKind),
		errors.Is(err, domain.ErrInternalKindNotAllowed),
		errors.Is(err, domain.ErrInvalidMoneyFormat),
		errors.Is(err, domain.ErrNegativeAmount),
		errors.Is(err, domain.ErrInvalidCurrency),
		errors.Is(err, domain.ErrNonPositiveAmount),
		errors.Is(err, domain.ErrReferenceRequired),
		errors.Is(err, domain.ErrReferenceNotAllowed),
		errors.Is(err, domain.ErrInvalidProviderID),
		errors.Is(err, domain.ErrInvalidExternalID),
		errors.Is(err, domain.ErrInvalidIdempotencyKey):
		return true
	default:
		return false
	}
}

func (c *Consumer) delete(ctx context.Context, message types.Message) {
	deleteCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	_, err := c.client.SQS().DeleteMessage(deleteCtx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.cfg.QueueURL),
		ReceiptHandle: message.ReceiptHandle,
	})
	if err != nil {
		c.logger.Error("deleting message failed; it will be redelivered and deduplicated by the inbox",
			slog.String("error", err.Error()))
	}
}

func (c *Consumer) sendToDLQ(ctx context.Context, message types.Message, reason string) {
	if c.cfg.DLQURL == "" {
		return
	}

	dlqCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	groupID := "malformed"
	dedupID := aws.ToString(message.MessageId)

	_, err := c.client.SQS().SendMessage(dlqCtx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(c.cfg.DLQURL),
		MessageBody:            message.Body,
		MessageGroupId:         aws.String(groupID),
		MessageDeduplicationId: aws.String(dedupID),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"failureReason": {DataType: aws.String("String"), StringValue: aws.String(reason)},
		},
	})
	if err != nil {
		c.logger.Error("sending message to dlq failed", slog.String("error", err.Error()))
	}
}
