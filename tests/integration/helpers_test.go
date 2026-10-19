//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"

	"github.com/KevVemn/junglegaming-backend/internal/domain"
	"github.com/KevVemn/junglegaming-backend/internal/messaging"
	"github.com/KevVemn/junglegaming-backend/internal/service"
)

func createQueues(ctx context.Context, client *messaging.Client) (map[string]string, error) {
	names := []string{"wager-transactions-dlq.fifo", "wager-transactions.fifo", "wager-events.fifo"}
	urls := make(map[string]string, len(names))

	for _, name := range names {
		out, err := client.SQS().CreateQueue(ctx, &sqs.CreateQueueInput{
			QueueName: aws.String(name),
			Attributes: map[string]string{
				"FifoQueue":                 "true",
				"ContentBasedDeduplication": "false",
				"VisibilityTimeout":         "5",
			},
		})
		if err != nil {
			return nil, fmt.Errorf("creating queue %s: %w", name, err)
		}
		urls[name] = aws.ToString(out.QueueUrl)
	}

	dlqAttrs, err := client.SQS().GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(urls["wager-transactions-dlq.fifo"]),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	if err != nil {
		return nil, err
	}

	dlqArn := dlqAttrs.Attributes[string(types.QueueAttributeNameQueueArn)]
	_, err = client.SQS().SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
		QueueUrl: aws.String(urls["wager-transactions.fifo"]),
		Attributes: map[string]string{
			"RedrivePolicy": fmt.Sprintf(`{"deadLetterTargetArn":"%s","maxReceiveCount":"3"}`, dlqArn),
		},
	})
	if err != nil {
		return nil, err
	}

	return urls, nil
}

func money(t *testing.T, amount string) domain.Money {
	t.Helper()
	m, err := domain.ParseMoney(amount, "BRL")
	if err != nil {
		t.Fatalf("ParseMoney(%q) failed: %v", amount, err)
	}
	return m
}

func openWallet(t *testing.T, initial string) *domain.Wallet {
	t.Helper()
	wallet, err := sharedEnv.WalletSvc.Open(context.Background(), service.OpenWalletCommand{
		PlayerID:       uuid.New(),
		InitialBalance: money(t, initial),
		CorrelationID:  uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("opening wallet failed: %v", err)
	}
	return wallet
}

func betCommand(wallet *domain.Wallet, externalID, amount string) service.SubmitCommand {
	parsed, _ := domain.ParseMoney(amount, "BRL")
	return service.SubmitCommand{
		ProviderID:            "provider-a",
		ExternalTransactionID: externalID,
		IdempotencyKey:        "provider-a:" + externalID,
		PlayerID:              wallet.PlayerID(),
		WalletID:              wallet.ID(),
		RoundID:               "round-1",
		GameID:                "fortune-chimp",
		Kind:                  domain.KindBet,
		Money:                 parsed,
		CorrelationID:         uuid.NewString(),
	}
}

func walletBalance(t *testing.T, walletID uuid.UUID) domain.Money {
	t.Helper()
	wallet, err := sharedEnv.WalletSvc.GetByID(context.Background(), walletID)
	if err != nil {
		t.Fatalf("reading wallet failed: %v", err)
	}
	return wallet.Balance()
}

func ledgerCount(t *testing.T, walletID uuid.UUID, direction domain.LedgerDirection) int {
	t.Helper()
	var count int
	err := sharedEnv.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = $2`,
		walletID, string(direction)).Scan(&count)
	if err != nil {
		t.Fatalf("counting ledger entries failed: %v", err)
	}
	return count
}

func assertReconciled(t *testing.T, walletID uuid.UUID) {
	t.Helper()
	result, err := sharedEnv.WalletSvc.Reconcile(context.Background(), walletID)
	if err != nil {
		t.Fatalf("reconciliation failed: %v", err)
	}
	if !result.Consistent {
		t.Errorf("wallet %s is inconsistent: stored=%s calculated=%s difference=%s",
			walletID, result.StoredBalance, result.CalculatedBalance, result.Difference)
	}
}

func sendMessage(t *testing.T, body, groupID, dedupID string) {
	t.Helper()
	_, err := sharedEnv.SQSClient.SQS().SendMessage(context.Background(), &sqs.SendMessageInput{
		QueueUrl:               aws.String(sharedEnv.SQSConfig.QueueURL),
		MessageBody:            aws.String(body),
		MessageGroupId:         aws.String(groupID),
		MessageDeduplicationId: aws.String(dedupID),
	})
	if err != nil {
		t.Fatalf("sending message failed: %v", err)
	}
}

func queueDepth(t *testing.T, queueURL string) int {
	t.Helper()
	out, err := sharedEnv.SQSClient.SQS().GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(queueURL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages},
	})
	if err != nil {
		t.Fatalf("reading queue depth failed: %v", err)
	}

	raw := out.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessages)]
	var depth int
	_, _ = fmt.Sscanf(raw, "%d", &depth)
	return depth
}

func eventually(t *testing.T, timeout time.Duration, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for: %s", timeout, description)
}
