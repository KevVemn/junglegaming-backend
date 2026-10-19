package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/KevVemn/junglegaming-backend/internal/domain"
)

var (
	ErrTransactionNotFound  = errors.New("transaction not found")
	ErrDuplicateExternalID  = errors.New("transaction already exists for provider and external id")
	ErrDuplicateIdempotency = errors.New("idempotency key already used")
	ErrAlreadyReversed      = errors.New("reference already has a successful reversal of this kind")
)

const transactionColumns = `
	id, wallet_id, player_id, kind, amount_minor, currency, state,
	provider_id, external_transaction_id, idempotency_key, payload_hash,
	round_id, game_id, reference_external_id, reference_internal_id,
	failure_code, result_balance_minor, created_at, updated_at`

type TransactionRepository struct{}

func NewTransactionRepository() *TransactionRepository { return &TransactionRepository{} }

func (r *TransactionRepository) Insert(ctx context.Context, q Querier, t *domain.WagerTransaction) error {
	_, err := q.Exec(ctx, `
		INSERT INTO wager_transactions (
			id, wallet_id, player_id, kind, amount_minor, currency, state,
			provider_id, external_transaction_id, idempotency_key, payload_hash,
			round_id, game_id, reference_external_id, reference_internal_id,
			failure_code, result_balance_minor, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11,
			$12, $13, $14, $15,
			$16, $17, $18, $19
		)`,
		t.ID(), t.WalletID(), t.PlayerID(), string(t.Kind()),
		t.Money().MinorUnits(), t.Money().Currency(), string(t.State()),
		nullableString(t.ProviderID()), nullableString(t.ExternalTransactionID()),
		nullableString(t.IdempotencyKey()), nullableString(t.PayloadHash()),
		nullableString(t.RoundID()), nullableString(t.GameID()),
		nullableString(t.ReferenceExternalID()), t.ReferenceInternalID(),
		nullableFailureCode(t.FailureCode()), nullableBalance(t.ResultBalance()),
		t.CreatedAt(), t.UpdatedAt(),
	)
	if err != nil {
		switch {
		case isUniqueViolation(err, "wager_transactions_provider_external_unique"):
			return ErrDuplicateExternalID
		case isUniqueViolation(err, "wager_transactions_idempotency_key_unique"):
			return ErrDuplicateIdempotency
		case isUniqueViolation(err, "wager_transactions_single_successful_reversal"):
			return ErrAlreadyReversed
		case isUniqueViolation(err, ""):
			return ErrDuplicateExternalID
		}
		return fmt.Errorf("inserting transaction: %w", err)
	}
	return nil
}

func (r *TransactionRepository) Update(ctx context.Context, q Querier, t *domain.WagerTransaction) error {
	tag, err := q.Exec(ctx, `
		UPDATE wager_transactions
		   SET state = $1, failure_code = $2, result_balance_minor = $3,
		       reference_internal_id = $4, updated_at = $5
		 WHERE id = $6`,
		string(t.State()), nullableFailureCode(t.FailureCode()),
		nullableBalance(t.ResultBalance()), t.ReferenceInternalID(),
		t.UpdatedAt(), t.ID(),
	)
	if err != nil {
		if isUniqueViolation(err, "wager_transactions_single_successful_reversal") {
			return ErrAlreadyReversed
		}
		return fmt.Errorf("updating transaction: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrTransactionNotFound
	}
	return nil
}

func (r *TransactionRepository) FindByID(ctx context.Context, q Querier, id uuid.UUID) (*domain.WagerTransaction, error) {
	row := q.QueryRow(ctx, `SELECT`+transactionColumns+` FROM wager_transactions WHERE id = $1`, id)
	return scanTransaction(row)
}

func (r *TransactionRepository) FindByIdempotencyKey(
	ctx context.Context, q Querier, key string,
) (*domain.WagerTransaction, error) {
	row := q.QueryRow(ctx, `SELECT`+transactionColumns+`
		FROM wager_transactions WHERE idempotency_key = $1`, key)
	return scanTransaction(row)
}

func (r *TransactionRepository) FindByProviderAndExternalID(
	ctx context.Context, q Querier, providerID, externalID string,
) (*domain.WagerTransaction, error) {
	row := q.QueryRow(ctx, `SELECT`+transactionColumns+`
		FROM wager_transactions
		WHERE provider_id = $1 AND external_transaction_id = $2`, providerID, externalID)
	return scanTransaction(row)
}

func (r *TransactionRepository) HasSuccessfulReversal(
	ctx context.Context, q Querier, referenceID uuid.UUID, kinds []string,
) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM wager_transactions
			 WHERE reference_internal_id = $1
			   AND state = 'PROCESSED'
			   AND kind = ANY($2)
		)`, referenceID, kinds).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("checking reversals: %w", err)
	}
	return exists, nil
}

func (r *TransactionRepository) ClaimPendingReferences(
	ctx context.Context, q Querier, limit int, now time.Time,
) ([]*domain.WagerTransaction, error) {
	rows, err := q.Query(ctx, `SELECT`+transactionColumns+`
		FROM wager_transactions
		WHERE state = 'PENDING_REFERENCE'
		  AND (reference_next_retry_at IS NULL OR reference_next_retry_at <= $1)
		ORDER BY created_at
		LIMIT $2
		FOR UPDATE SKIP LOCKED`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("claiming pending references: %w", err)
	}
	defer rows.Close()

	return collectTransactions(rows)
}

func (r *TransactionRepository) ClaimStalePending(
	ctx context.Context, q Querier, limit int, olderThan time.Time,
) ([]*domain.WagerTransaction, error) {
	rows, err := q.Query(ctx, `SELECT`+transactionColumns+`
		FROM wager_transactions
		WHERE state = 'PENDING' AND created_at <= $1
		ORDER BY created_at
		LIMIT $2
		FOR UPDATE SKIP LOCKED`, olderThan, limit)
	if err != nil {
		return nil, fmt.Errorf("claiming stale pending transactions: %w", err)
	}
	defer rows.Close()

	return collectTransactions(rows)
}

func (r *TransactionRepository) RecordReferenceAttempt(
	ctx context.Context, q Querier, id uuid.UUID, nextRetryAt time.Time,
) (int, error) {
	var attempts int
	err := q.QueryRow(ctx, `
		UPDATE wager_transactions
		   SET reference_attempts = reference_attempts + 1,
		       reference_next_retry_at = $1,
		       updated_at = now()
		 WHERE id = $2
		RETURNING reference_attempts`, nextRetryAt, id).Scan(&attempts)
	if err != nil {
		return 0, fmt.Errorf("recording reference attempt: %w", err)
	}
	return attempts, nil
}

func (r *TransactionRepository) ReferenceAttempts(ctx context.Context, q Querier, id uuid.UUID) (int, error) {
	var attempts int
	if err := q.QueryRow(ctx,
		`SELECT reference_attempts FROM wager_transactions WHERE id = $1`, id).Scan(&attempts); err != nil {
		return 0, fmt.Errorf("reading reference attempts: %w", err)
	}
	return attempts, nil
}

func collectTransactions(rows pgx.Rows) ([]*domain.WagerTransaction, error) {
	result := make([]*domain.WagerTransaction, 0)
	for rows.Next() {
		tx, err := scanTransaction(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, tx)
	}
	return result, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTransaction(row rowScanner) (*domain.WagerTransaction, error) {
	var (
		id, walletID, playerID uuid.UUID
		kind, currency, state  string
		amountMinor            int64
		providerID             *string
		externalID             *string
		idempotencyKey         *string
		payloadHash            *string
		roundID                *string
		gameID                 *string
		referenceExternalID    *string
		referenceInternalID    *uuid.UUID
		failureCode            *string
		resultBalanceMinor     *int64
		createdAt, updatedAt   time.Time
	)

	err := row.Scan(
		&id, &walletID, &playerID, &kind, &amountMinor, &currency, &state,
		&providerID, &externalID, &idempotencyKey, &payloadHash,
		&roundID, &gameID, &referenceExternalID, &referenceInternalID,
		&failureCode, &resultBalanceMinor, &createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTransactionNotFound
		}
		return nil, fmt.Errorf("scanning transaction: %w", err)
	}

	money, err := domain.FromMinorUnits(amountMinor, currency)
	if err != nil {
		return nil, fmt.Errorf("rebuilding transaction money: %w", err)
	}

	var resultBalance *domain.Money
	if resultBalanceMinor != nil {
		balance, err := domain.FromMinorUnits(*resultBalanceMinor, currency)
		if err != nil {
			return nil, fmt.Errorf("rebuilding result balance: %w", err)
		}
		resultBalance = &balance
	}

	return domain.RehydrateTransaction(domain.RehydrateTransactionInput{
		ExternalTransactionInput: domain.ExternalTransactionInput{
			ID:                    id,
			WalletID:              walletID,
			PlayerID:              playerID,
			ProviderID:            derefString(providerID),
			ExternalTransactionID: derefString(externalID),
			IdempotencyKey:        derefString(idempotencyKey),
			PayloadHash:           derefString(payloadHash),
			RoundID:               derefString(roundID),
			GameID:                derefString(gameID),
			Kind:                  domain.TransactionKind(kind),
			Money:                 money,
			ReferenceExternalID:   derefString(referenceExternalID),
		},
		State:               domain.TransactionState(state),
		ReferenceInternalID: referenceInternalID,
		FailureCode:         domain.FailureCode(derefString(failureCode)),
		ResultBalance:       resultBalance,
		CreatedAt:           createdAt,
		UpdatedAt:           updatedAt,
	})
}

func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullableFailureCode(c domain.FailureCode) *string {
	if c == "" {
		return nil
	}
	s := string(c)
	return &s
}

func nullableBalance(m *domain.Money) *int64 {
	if m == nil {
		return nil
	}
	v := m.MinorUnits()
	return &v
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
