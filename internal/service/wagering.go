package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/KevVemn/junglegaming-backend/internal/domain"
	"github.com/KevVemn/junglegaming-backend/internal/events"
	"github.com/KevVemn/junglegaming-backend/internal/idempotency"
	"github.com/KevVemn/junglegaming-backend/internal/observability"
	"github.com/KevVemn/junglegaming-backend/internal/postgres"
)

var (
	ErrIdempotencyConflict = errors.New("idempotency key reused with a different payload")
	ErrWalletMismatch      = errors.New("wallet does not belong to the informed player")
	ErrProviderForbidden   = errors.New("provider is not allowed to access this transaction")
)

const maxConcurrencyRetries = 3

type Clock func() time.Time

type WageringService struct {
	tx       *postgres.TxManager
	wallets  *postgres.WalletRepository
	txs      *postgres.TransactionRepository
	ledger   *postgres.LedgerRepository
	outbox   *postgres.OutboxRepository
	inbox    *postgres.InboxRepository
	metrics  *observability.Metrics
	now      Clock
	maxRetry int
}

func NewWageringService(
	tx *postgres.TxManager,
	wallets *postgres.WalletRepository,
	txs *postgres.TransactionRepository,
	ledger *postgres.LedgerRepository,
	outbox *postgres.OutboxRepository,
	inbox *postgres.InboxRepository,
	metrics *observability.Metrics,
) *WageringService {
	return &WageringService{
		tx:       tx,
		wallets:  wallets,
		txs:      txs,
		ledger:   ledger,
		outbox:   outbox,
		inbox:    inbox,
		metrics:  metrics,
		now:      time.Now,
		maxRetry: maxConcurrencyRetries,
	}
}

type SubmitCommand struct {
	ProviderID            string
	ExternalTransactionID string
	IdempotencyKey        string
	PlayerID              uuid.UUID
	WalletID              uuid.UUID
	RoundID               string
	GameID                string
	Kind                  domain.TransactionKind
	Money                 domain.Money
	ReferenceExternalID   string
	CorrelationID         string
	Inbox                 *InboxClaim
}

func (c SubmitCommand) Source() string {
	if c.Inbox != nil {
		return "sqs"
	}
	return "http"
}

type InboxClaim struct {
	ConsumerName string
	MessageID    string
	MessageHash  string
}

type SubmitResult struct {
	TransactionID    uuid.UUID
	State            domain.TransactionState
	Balance          domain.Money
	FailureCode      domain.FailureCode
	IdempotentReplay bool
}

func (c SubmitCommand) businessHash() (string, error) {
	return idempotency.Hash(idempotency.BusinessFields{
		ProviderID:            c.ProviderID,
		ExternalTransactionID: c.ExternalTransactionID,
		PlayerID:              c.PlayerID.String(),
		WalletID:              c.WalletID.String(),
		RoundID:               c.RoundID,
		GameID:                c.GameID,
		Kind:                  string(c.Kind),
		Money:                 c.Money,
		ReferenceExternalID:   c.ReferenceExternalID,
	})
}

func (s *WageringService) Submit(ctx context.Context, cmd SubmitCommand) (*SubmitResult, error) {
	started := s.now()
	source := cmd.Source()

	hash, err := cmd.businessHash()
	if err != nil {
		return nil, err
	}

	if replay, err := s.findReplay(ctx, cmd, hash); err != nil {
		return nil, err
	} else if replay != nil {
		s.metrics.DuplicatesTotal.WithLabelValues(source, "idempotency_key").Inc()
		return replay, nil
	}

	var result *SubmitResult
	for attempt := 0; attempt < s.maxRetry; attempt++ {
		result, err = s.process(ctx, cmd, hash)
		if err == nil {
			s.metrics.TransactionsTotal.
				WithLabelValues(string(cmd.Kind), string(result.State), source).Inc()
			s.metrics.TransactionDuration.
				WithLabelValues(string(cmd.Kind), source).
				Observe(s.now().Sub(started).Seconds())
			return result, nil
		}
		if errors.Is(err, postgres.ErrConcurrencyConflict) {
			s.metrics.ConcurrencyConflicts.Inc()
			s.metrics.RetriesTotal.WithLabelValues("wallet_update").Inc()
			continue
		}
		if errors.Is(err, postgres.ErrInboxDuplicate) {
			s.metrics.DuplicatesTotal.WithLabelValues(source, "inbox").Inc()
			return nil, err
		}
		if errors.Is(err, postgres.ErrDuplicateIdempotency) || errors.Is(err, postgres.ErrDuplicateExternalID) {
			replay, replayErr := s.findReplay(ctx, cmd, hash)
			if replayErr != nil {
				return nil, replayErr
			}
			if replay != nil {
				return replay, nil
			}
		}
		return nil, err
	}

	return nil, fmt.Errorf("concurrency conflict after %d attempts: %w", s.maxRetry, postgres.ErrConcurrencyConflict)
}

func (s *WageringService) findReplay(ctx context.Context, cmd SubmitCommand, hash string) (*SubmitResult, error) {
	existing, err := s.txs.FindByIdempotencyKey(ctx, s.tx.Pool(), cmd.IdempotencyKey)
	if err != nil {
		if errors.Is(err, postgres.ErrTransactionNotFound) {
			byExternal, extErr := s.txs.FindByProviderAndExternalID(
				ctx, s.tx.Pool(), cmd.ProviderID, cmd.ExternalTransactionID)
			if extErr != nil {
				if errors.Is(extErr, postgres.ErrTransactionNotFound) {
					return nil, nil
				}
				return nil, extErr
			}
			if byExternal.IdempotencyKey() != cmd.IdempotencyKey {
				return nil, ErrIdempotencyConflict
			}
			existing = byExternal
		} else {
			return nil, err
		}
	}

	if existing.PayloadHash() != hash {
		return nil, ErrIdempotencyConflict
	}
	if existing.ProviderID() != cmd.ProviderID {
		return nil, ErrProviderForbidden
	}

	return s.resultFrom(existing), nil
}

func (s *WageringService) resultFrom(t *domain.WagerTransaction) *SubmitResult {
	result := &SubmitResult{
		TransactionID:    t.ID(),
		State:            t.State(),
		FailureCode:      t.FailureCode(),
		IdempotentReplay: true,
	}
	if t.ResultBalance() != nil {
		result.Balance = *t.ResultBalance()
	}
	return result
}

func (s *WageringService) process(ctx context.Context, cmd SubmitCommand, hash string) (*SubmitResult, error) {
	var result *SubmitResult

	err := s.tx.WithinTx(ctx, func(q postgres.Querier) error {
		now := s.now().UTC()

		if cmd.Inbox != nil {
			if err := s.inbox.Claim(ctx, q,
				cmd.Inbox.ConsumerName, cmd.Inbox.MessageID, cmd.Inbox.MessageHash, now); err != nil {
				return err
			}
		}

		wallet, err := s.wallets.FindByID(ctx, q, cmd.WalletID)
		if err != nil {
			return err
		}
		if wallet.PlayerID() != cmd.PlayerID {
			return ErrWalletMismatch
		}

		expectedVersion := wallet.Version()

		transaction, err := domain.NewExternalTransaction(domain.ExternalTransactionInput{
			ID:                    uuid.New(),
			WalletID:              cmd.WalletID,
			PlayerID:              cmd.PlayerID,
			ProviderID:            cmd.ProviderID,
			ExternalTransactionID: cmd.ExternalTransactionID,
			IdempotencyKey:        cmd.IdempotencyKey,
			PayloadHash:           hash,
			RoundID:               cmd.RoundID,
			GameID:                cmd.GameID,
			Kind:                  cmd.Kind,
			Money:                 cmd.Money,
			ReferenceExternalID:   cmd.ReferenceExternalID,
		}, now)
		if err != nil {
			return err
		}

		if err := s.txs.Insert(ctx, q, transaction); err != nil {
			return err
		}

		outcome, err := s.applyOperation(ctx, q, wallet, transaction, cmd, now)
		if err != nil {
			return err
		}

		if outcome.ledgerEntry != nil {
			if err := s.ledger.Insert(ctx, q, outcome.ledgerEntry); err != nil {
				return err
			}
			if err := s.wallets.UpdateBalance(ctx, q, wallet, expectedVersion); err != nil {
				return err
			}
		}

		if err := s.txs.Update(ctx, q, transaction); err != nil {
			return err
		}

		if err := s.publishOutcome(ctx, q, transaction, wallet, outcome, cmd.CorrelationID, now); err != nil {
			return err
		}

		if cmd.Inbox != nil {
			if err := s.inbox.MarkCompleted(ctx, q,
				cmd.Inbox.ConsumerName, cmd.Inbox.MessageID, now); err != nil {
				return err
			}
		}

		result = &SubmitResult{
			TransactionID:    transaction.ID(),
			State:            transaction.State(),
			Balance:          wallet.Balance(),
			FailureCode:      transaction.FailureCode(),
			IdempotentReplay: false,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

type operationOutcome struct {
	ledgerEntry *domain.WalletLedgerEntry
	rejected    bool
	pending     bool
}

func (s *WageringService) applyOperation(
	ctx context.Context, q postgres.Querier,
	wallet *domain.Wallet, transaction *domain.WagerTransaction,
	cmd SubmitCommand, now time.Time,
) (operationOutcome, error) {
	switch cmd.Kind {
	case domain.KindBet:
		return s.applyDebit(wallet, transaction, cmd.Money, now, domain.FailureInsufficientFunds)

	case domain.KindWin:
		return s.applyCredit(wallet, transaction, cmd.Money, now)

	case domain.KindLoss:
		if err := transaction.MarkProcessed(wallet.Balance(), now); err != nil {
			return operationOutcome{}, err
		}
		return operationOutcome{}, nil

	case domain.KindRefund, domain.KindRollback:
		return s.applyReversal(ctx, q, wallet, transaction, cmd, now)

	default:
		return operationOutcome{}, domain.ErrInvalidTransactionKind
	}
}

func (s *WageringService) applyDebit(
	wallet *domain.Wallet, transaction *domain.WagerTransaction,
	amount domain.Money, now time.Time, code domain.FailureCode,
) (operationOutcome, error) {
	entry, err := wallet.Debit(amount, transaction.ID(), now)
	if err != nil {
		if errors.Is(err, domain.ErrInsufficientFunds) {
			if markErr := transaction.MarkRejected(code, now); markErr != nil {
				return operationOutcome{}, markErr
			}
			return operationOutcome{rejected: true}, nil
		}
		if errors.Is(err, domain.ErrWalletCurrencyKind) {
			if markErr := transaction.MarkRejected(domain.FailureCurrencyMismatch, now); markErr != nil {
				return operationOutcome{}, markErr
			}
			return operationOutcome{rejected: true}, nil
		}
		return operationOutcome{}, err
	}

	if err := transaction.MarkProcessed(wallet.Balance(), now); err != nil {
		return operationOutcome{}, err
	}
	return operationOutcome{ledgerEntry: entry}, nil
}

func (s *WageringService) applyCredit(
	wallet *domain.Wallet, transaction *domain.WagerTransaction,
	amount domain.Money, now time.Time,
) (operationOutcome, error) {
	entry, err := wallet.Credit(amount, transaction.ID(), now)
	if err != nil {
		if errors.Is(err, domain.ErrWalletCurrencyKind) {
			if markErr := transaction.MarkRejected(domain.FailureCurrencyMismatch, now); markErr != nil {
				return operationOutcome{}, markErr
			}
			return operationOutcome{rejected: true}, nil
		}
		return operationOutcome{}, err
	}

	if err := transaction.MarkProcessed(wallet.Balance(), now); err != nil {
		return operationOutcome{}, err
	}
	return operationOutcome{ledgerEntry: entry}, nil
}

func (s *WageringService) applyReversal(
	ctx context.Context, q postgres.Querier,
	wallet *domain.Wallet, transaction *domain.WagerTransaction,
	cmd SubmitCommand, now time.Time,
) (operationOutcome, error) {
	reference, err := s.txs.FindByProviderAndExternalID(ctx, q, cmd.ProviderID, cmd.ReferenceExternalID)
	if err != nil {
		if errors.Is(err, postgres.ErrTransactionNotFound) {
			if markErr := transaction.MarkPendingReference(now); markErr != nil {
				return operationOutcome{}, markErr
			}
			return operationOutcome{pending: true}, nil
		}
		return operationOutcome{}, err
	}

	if reference.State() != domain.StateProcessed {
		if markErr := transaction.MarkRejected(domain.FailureReferenceNotUsable, now); markErr != nil {
			return operationOutcome{}, markErr
		}
		return operationOutcome{rejected: true}, nil
	}

	if !s.referenceMatches(reference, transaction, cmd) {
		if markErr := transaction.MarkRejected(domain.FailureReferenceMismatch, now); markErr != nil {
			return operationOutcome{}, markErr
		}
		return operationOutcome{rejected: true}, nil
	}

	if err := transaction.ResolveReference(reference.ID(), now); err != nil {
		return operationOutcome{}, err
	}

	already, err := s.txs.HasSuccessfulReversal(ctx, q, reference.ID(), reversalKindsFor(cmd.Kind))
	if err != nil {
		return operationOutcome{}, err
	}
	if already {
		if markErr := transaction.MarkRejected(domain.FailureAlreadyReversed, now); markErr != nil {
			return operationOutcome{}, markErr
		}
		return operationOutcome{rejected: true}, nil
	}

	switch reference.Kind() {
	case domain.KindBet:
		return s.applyCredit(wallet, transaction, cmd.Money, now)
	case domain.KindWin, domain.KindRefund:
		return s.applyDebit(wallet, transaction, cmd.Money, now, domain.FailureReversalExceedsFund)
	default:
		if markErr := transaction.MarkRejected(domain.FailureReferenceNotUsable, now); markErr != nil {
			return operationOutcome{}, markErr
		}
		return operationOutcome{rejected: true}, nil
	}
}

func (s *WageringService) referenceMatches(
	reference, transaction *domain.WagerTransaction, cmd SubmitCommand,
) bool {
	if reference.ProviderID() != cmd.ProviderID {
		return false
	}
	if reference.PlayerID() != cmd.PlayerID || reference.WalletID() != cmd.WalletID {
		return false
	}
	if reference.Money().Currency() != cmd.Money.Currency() {
		return false
	}
	if reference.RoundID() != cmd.RoundID {
		return false
	}
	if !reference.Money().Equal(cmd.Money) {
		return false
	}
	return true
}

func reversalKindsFor(kind domain.TransactionKind) []string {
	if kind == domain.KindRefund {
		return []string{string(domain.KindRefund)}
	}
	return []string{string(domain.KindRollback)}
}

func (s *WageringService) publishOutcome(
	ctx context.Context, q postgres.Querier,
	transaction *domain.WagerTransaction, wallet *domain.Wallet,
	outcome operationOutcome, correlationID string, now time.Time,
) error {
	switch {
	case outcome.pending:
		envelope, err := events.NewWagerTransactionPendingReference(transaction, 0, correlationID, now)
		if err != nil {
			return err
		}
		return s.enqueue(ctx, q, envelope, transaction.ID(), now)

	case outcome.rejected:
		envelope, err := events.NewWagerTransactionRejected(transaction, transaction.FailureCode(), correlationID, now)
		if err != nil {
			return err
		}
		return s.enqueue(ctx, q, envelope, transaction.ID(), now)

	default:
		processed, err := events.NewWagerTransactionProcessed(transaction, wallet.Balance(), correlationID, now)
		if err != nil {
			return err
		}
		if err := s.enqueue(ctx, q, processed, transaction.ID(), now); err != nil {
			return err
		}

		if outcome.ledgerEntry != nil {
			changed, err := events.NewWalletBalanceChanged(outcome.ledgerEntry, wallet.Version(), correlationID, now)
			if err != nil {
				return err
			}
			if err := s.enqueue(ctx, q, changed, wallet.ID(), now); err != nil {
				return err
			}
		}
		return nil
	}
}

func (s *WageringService) enqueue(
	ctx context.Context, q postgres.Querier,
	envelope events.Envelope, aggregateID uuid.UUID, now time.Time,
) error {
	payload, err := envelope.Marshal()
	if err != nil {
		return err
	}

	return s.outbox.Insert(ctx, q, postgres.OutboxRecord{
		EventID:       envelope.EventID,
		AggregateID:   aggregateID,
		AggregateType: envelope.AggregateType,
		EventType:     envelope.EventType,
		Payload:       payload,
		OccurredAt:    now,
	})
}

func (s *WageringService) GetByID(
	ctx context.Context, id uuid.UUID, authenticatedProvider string,
) (*domain.WagerTransaction, error) {
	transaction, err := s.txs.FindByID(ctx, s.tx.Pool(), id)
	if err != nil {
		return nil, err
	}
	if authenticatedProvider != "" && transaction.ProviderID() != authenticatedProvider {
		return nil, ErrProviderForbidden
	}
	return transaction, nil
}

func (s *WageringService) GetByExternalID(
	ctx context.Context, providerID, externalID string,
) (*domain.WagerTransaction, error) {
	return s.txs.FindByProviderAndExternalID(ctx, s.tx.Pool(), providerID, externalID)
}
