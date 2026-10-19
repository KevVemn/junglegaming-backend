package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/KevVemn/junglegaming-backend/internal/domain"
	"github.com/KevVemn/junglegaming-backend/internal/events"
	"github.com/KevVemn/junglegaming-backend/internal/postgres"
)

type ReferencePolicy struct {
	MaxAttempts int
	TTL         time.Duration
	BaseBackoff time.Duration
}

type ResumeOutcome string

const (
	ResumeProcessed ResumeOutcome = "PROCESSED"
	ResumeRejected  ResumeOutcome = "REJECTED"
	ResumeDeferred  ResumeOutcome = "DEFERRED"
)

func (s *WageringService) ResumePendingReference(
	ctx context.Context, transactionID string, policy ReferencePolicy,
) (ResumeOutcome, error) {
	var outcome ResumeOutcome

	err := s.tx.WithinTx(ctx, func(q postgres.Querier) error {
		now := s.now().UTC()

		transaction, err := s.txs.FindByIdempotencyKey(ctx, q, transactionID)
		if err != nil {
			return err
		}
		if transaction.State() != domain.StatePendingReference {
			outcome = ResumeDeferred
			return nil
		}

		reference, refErr := s.txs.FindByProviderAndExternalID(
			ctx, q, transaction.ProviderID(), transaction.ReferenceExternalID())

		if refErr != nil {
			if !errors.Is(refErr, postgres.ErrTransactionNotFound) {
				return refErr
			}
			return s.deferOrExpire(ctx, q, transaction, policy, now, &outcome)
		}

		if reference.State() == domain.StatePending || reference.State() == domain.StatePendingReference {
			return s.deferOrExpire(ctx, q, transaction, policy, now, &outcome)
		}

		if reference.State() != domain.StateProcessed {
			outcome = ResumeRejected
			return s.rejectPending(ctx, q, transaction, domain.FailureReferenceNotUsable, now)
		}

		wallet, err := s.wallets.FindByID(ctx, q, transaction.WalletID())
		if err != nil {
			return err
		}
		expectedVersion := wallet.Version()

		if !s.referenceMatchesTransaction(reference, transaction) {
			outcome = ResumeRejected
			return s.rejectPending(ctx, q, transaction, domain.FailureReferenceMismatch, now)
		}

		if err := transaction.ResolveReference(reference.ID(), now); err != nil {
			return err
		}

		already, err := s.txs.HasSuccessfulReversal(ctx, q, reference.ID(), reversalKindsFor(transaction.Kind()))
		if err != nil {
			return err
		}
		if already {
			outcome = ResumeRejected
			return s.rejectPending(ctx, q, transaction, domain.FailureAlreadyReversed, now)
		}

		var result operationOutcome
		switch reference.Kind() {
		case domain.KindBet:
			result, err = s.applyCredit(wallet, transaction, transaction.Money(), now)
		case domain.KindWin, domain.KindRefund:
			result, err = s.applyDebit(wallet, transaction, transaction.Money(), now, domain.FailureReversalExceedsFund)
		default:
			outcome = ResumeRejected
			return s.rejectPending(ctx, q, transaction, domain.FailureReferenceNotUsable, now)
		}
		if err != nil {
			return err
		}

		if result.ledgerEntry != nil {
			if err := s.ledger.Insert(ctx, q, result.ledgerEntry); err != nil {
				return err
			}
			if err := s.wallets.UpdateBalance(ctx, q, wallet, expectedVersion); err != nil {
				return err
			}
		}

		if err := s.txs.Update(ctx, q, transaction); err != nil {
			return err
		}

		if err := s.publishOutcome(ctx, q, transaction, wallet, result, transaction.ID().String(), now); err != nil {
			return err
		}

		if result.rejected {
			outcome = ResumeRejected
		} else {
			outcome = ResumeProcessed
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	if outcome != "" {
		s.metrics.PendingReferenceTotal.WithLabelValues(string(outcome)).Inc()
	}

	return outcome, nil
}

func (s *WageringService) deferOrExpire(
	ctx context.Context, q postgres.Querier,
	transaction *domain.WagerTransaction, policy ReferencePolicy,
	now time.Time, outcome *ResumeOutcome,
) error {
	age := now.Sub(transaction.CreatedAt())
	attempts, err := s.txs.ReferenceAttempts(ctx, q, transaction.ID())
	if err != nil {
		return err
	}

	if attempts+1 >= policy.MaxAttempts || age >= policy.TTL {
		*outcome = ResumeRejected
		return s.rejectPending(ctx, q, transaction, domain.FailureReferenceNotFound, now)
	}

	backoff := time.Duration(math.Pow(2, float64(attempts))) * policy.BaseBackoff
	if _, err := s.txs.RecordReferenceAttempt(ctx, q, transaction.ID(), now.Add(backoff)); err != nil {
		return err
	}

	*outcome = ResumeDeferred
	return nil
}

func (s *WageringService) rejectPending(
	ctx context.Context, q postgres.Querier,
	transaction *domain.WagerTransaction, code domain.FailureCode, now time.Time,
) error {
	if err := transaction.MarkRejected(code, now); err != nil {
		return err
	}
	if err := s.txs.Update(ctx, q, transaction); err != nil {
		return err
	}

	envelope, err := events.NewWagerTransactionRejected(
		transaction, code, transaction.ID().String(), now)
	if err != nil {
		return err
	}
	return s.enqueue(ctx, q, envelope, transaction.ID(), now)
}

func (s *WageringService) referenceMatchesTransaction(reference, transaction *domain.WagerTransaction) bool {
	if reference.ProviderID() != transaction.ProviderID() {
		return false
	}
	if reference.PlayerID() != transaction.PlayerID() || reference.WalletID() != transaction.WalletID() {
		return false
	}
	if reference.Money().Currency() != transaction.Money().Currency() {
		return false
	}
	if reference.RoundID() != transaction.RoundID() {
		return false
	}
	return reference.Money().Equal(transaction.Money())
}

func (s *WageringService) ClaimPendingReferences(
	ctx context.Context, limit int,
) ([]string, error) {
	var keys []string

	err := s.tx.WithinTx(ctx, func(q postgres.Querier) error {
		pending, err := s.txs.ClaimPendingReferences(ctx, q, limit, s.now().UTC())
		if err != nil {
			return err
		}
		for _, t := range pending {
			keys = append(keys, t.IdempotencyKey())
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("claiming pending references: %w", err)
	}

	return keys, nil
}
