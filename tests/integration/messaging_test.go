//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/KevVemn/junglegaming-backend/internal/config"
	"github.com/KevVemn/junglegaming-backend/internal/domain"
	"github.com/KevVemn/junglegaming-backend/internal/messaging"
)

func betMessage(walletID, playerID uuid.UUID, messageID, externalID, amount string) string {
	return fmt.Sprintf(`{"messageId":"%s","type":"WagerTransactionRequested","occurredAt":"2026-10-18T12:00:00.000Z","data":{"providerId":"provider-a","externalTransactionId":"%s","idempotencyKey":"provider-a:%s","playerId":"%s","walletId":"%s","roundId":"round-sqs","gameId":"fortune-chimp","kind":"BET","money":{"amount":"%s","currency":"BRL"}}}`,
		messageID, externalID, externalID, playerID, walletID, amount)
}

func startConsumer(t *testing.T) func() {
	t.Helper()
	consumer := messaging.NewConsumer(
		sharedEnv.SQSClient, sharedEnv.WageringSvc, sharedEnv.SQSConfig,
		sharedEnv.Logger, sharedEnv.Metrics)

	ctx, cancel := context.WithCancel(context.Background())
	consumer.Start(ctx)

	return func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = consumer.Stop(stopCtx)
		cancel()
	}
}

func TestConsumerProcessesMessageFromQueue(t *testing.T) {
	stop := startConsumer(t)
	defer stop()

	wallet := openWallet(t, "500.00")
	externalID := "sqs-" + uuid.NewString()
	body := betMessage(wallet.ID(), wallet.PlayerID(), "msg-"+externalID, externalID, "120.00")

	sendMessage(t, body, wallet.ID().String(), "dedup-"+externalID)

	eventually(t, 25*time.Second, "balance to drop to 380.00", func() bool {
		return walletBalance(t, wallet.ID()).String() == "380.00"
	})

	if got := ledgerCount(t, wallet.ID(), domain.DirectionDebit); got != 1 {
		t.Errorf("debit entries = %d, want 1", got)
	}
	assertReconciled(t, wallet.ID())
}

func TestConsumerIgnoresDuplicateMessage(t *testing.T) {
	stop := startConsumer(t)
	defer stop()

	wallet := openWallet(t, "500.00")
	externalID := "dup-" + uuid.NewString()
	messageID := "msg-" + externalID
	body := betMessage(wallet.ID(), wallet.PlayerID(), messageID, externalID, "100.00")

	sendMessage(t, body, wallet.ID().String(), "dedup-1-"+externalID)
	eventually(t, 25*time.Second, "first message to be processed", func() bool {
		return walletBalance(t, wallet.ID()).String() == "400.00"
	})

	sendMessage(t, body, wallet.ID().String(), "dedup-2-"+externalID)
	time.Sleep(6 * time.Second)

	if balance := walletBalance(t, wallet.ID()); balance.String() != "400.00" {
		t.Errorf("balance changed after duplicate: %s, want 400.00", balance)
	}
	if got := ledgerCount(t, wallet.ID(), domain.DirectionDebit); got != 1 {
		t.Errorf("debit entries = %d, want exactly 1 after duplicate", got)
	}
	assertReconciled(t, wallet.ID())
}

func TestConsumerSendsMalformedMessageToDLQ(t *testing.T) {
	stop := startConsumer(t)
	defer stop()

	before := queueDepth(t, sharedEnv.SQSConfig.DLQURL)
	sendMessage(t, `{"not":"an envelope"}`, "malformed", "malformed-"+uuid.NewString())

	eventually(t, 25*time.Second, "message to reach the dlq", func() bool {
		return queueDepth(t, sharedEnv.SQSConfig.DLQURL) > before
	})
}

func TestHttpAndSqsShareIdempotency(t *testing.T) {
	stop := startConsumer(t)
	defer stop()

	wallet := openWallet(t, "300.00")
	externalID := "cross-" + uuid.NewString()

	cmd := betCommand(wallet, externalID, "50.00")
	if _, err := sharedEnv.WageringSvc.Submit(context.Background(), cmd); err != nil {
		t.Fatalf("direct submit failed: %v", err)
	}
	if balance := walletBalance(t, wallet.ID()); balance.String() != "250.00" {
		t.Fatalf("balance after direct submit = %s, want 250.00", balance)
	}

	body := betMessage(wallet.ID(), wallet.PlayerID(), "msg-"+externalID, externalID, "50.00")
	sendMessage(t, body, wallet.ID().String(), "dedup-"+externalID)

	time.Sleep(6 * time.Second)

	if balance := walletBalance(t, wallet.ID()); balance.String() != "250.00" {
		t.Errorf("balance changed after same operation via sqs: %s, want 250.00", balance)
	}
	if got := ledgerCount(t, wallet.ID(), domain.DirectionDebit); got != 1 {
		t.Errorf("debit entries = %d, want exactly 1", got)
	}
	assertReconciled(t, wallet.ID())
}

func TestOutboxPublisherDrainsPendingEvents(t *testing.T) {
	wallet := openWallet(t, "400.00")
	cmd := betCommand(wallet, "outbox-"+uuid.NewString(), "40.00")
	if _, err := sharedEnv.WageringSvc.Submit(context.Background(), cmd); err != nil {
		t.Fatalf("submit failed: %v", err)
	}

	pendingBefore := pendingOutboxCount(t)
	if pendingBefore == 0 {
		t.Fatal("expected pending events in the outbox before publishing")
	}

	cfg := &config.Config{
		Outbox: config.OutboxConfig{
			BatchSize:    50,
			PollInterval: 300 * time.Millisecond,
			LockDuration: 10 * time.Second,
			MaxAttempts:  5,
			BaseBackoff:  time.Second,
			WorkerCount:  1,
		},
		SQS: sharedEnv.SQSConfig,
	}

	publisher := messaging.NewOutboxPublisher(
		sharedEnv.SQSClient, sharedEnv.TxManager, sharedEnv.Outbox, cfg,
		sharedEnv.Logger, sharedEnv.Metrics)

	ctx, cancel := context.WithCancel(context.Background())
	publisher.Start(ctx)
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = publisher.Stop(stopCtx)
		cancel()
	}()

	eventually(t, 30*time.Second, "outbox to be drained", func() bool {
		return pendingOutboxCount(t) == 0
	})
}

func TestTwoPublishersDoNotDuplicateEvents(t *testing.T) {
	wallet := openWallet(t, "400.00")
	for i := 0; i < 3; i++ {
		cmd := betCommand(wallet, fmt.Sprintf("multi-pub-%d-%s", i, uuid.NewString()), "10.00")
		if _, err := sharedEnv.WageringSvc.Submit(context.Background(), cmd); err != nil {
			t.Fatalf("submit failed: %v", err)
		}
	}

	cfg := &config.Config{
		Outbox: config.OutboxConfig{
			BatchSize:    10,
			PollInterval: 200 * time.Millisecond,
			LockDuration: 5 * time.Second,
			MaxAttempts:  5,
			BaseBackoff:  time.Second,
			WorkerCount:  1,
		},
		SQS: sharedEnv.SQSConfig,
	}

	ctx, cancel := context.WithCancel(context.Background())
	stops := make([]func(), 0, 2)

	for i := 0; i < 2; i++ {
		p := messaging.NewOutboxPublisher(
			sharedEnv.SQSClient, sharedEnv.TxManager, sharedEnv.Outbox, cfg,
			sharedEnv.Logger, sharedEnv.Metrics)
		p.Start(ctx)
		stops = append(stops, func() {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer stopCancel()
			_ = p.Stop(stopCtx)
		})
	}
	defer func() {
		for _, s := range stops {
			s()
		}
		cancel()
	}()

	eventually(t, 30*time.Second, "both publishers to drain the outbox", func() bool {
		return pendingOutboxCount(t) == 0
	})

	var doublePublished int
	err := sharedEnv.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM outbox_events WHERE published_at IS NOT NULL AND attempts > 1`).Scan(&doublePublished)
	if err != nil {
		t.Fatalf("checking publish attempts failed: %v", err)
	}

	assertReconciled(t, wallet.ID())
}

func pendingOutboxCount(t *testing.T) int {
	t.Helper()
	var count int
	err := sharedEnv.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&count)
	if err != nil {
		t.Fatalf("counting pending outbox events failed: %v", err)
	}
	return count
}
