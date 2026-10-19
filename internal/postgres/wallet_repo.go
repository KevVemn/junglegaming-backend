package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/KevVemn/junglegaming-backend/internal/domain"
)

var (
	ErrWalletNotFound      = errors.New("wallet not found")
	ErrWalletAlreadyExists = errors.New("wallet already exists for player and currency")
	ErrConcurrencyConflict = errors.New("concurrency conflict: wallet version changed")
)

type WalletRepository struct{}

func NewWalletRepository() *WalletRepository { return &WalletRepository{} }

func (r *WalletRepository) Insert(ctx context.Context, q Querier, w *domain.Wallet) error {
	_, err := q.Exec(ctx, `
		INSERT INTO wallets (id, player_id, currency, balance_minor, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		w.ID(), w.PlayerID(), w.Currency(), w.Balance().MinorUnits(),
		w.Version(), w.CreatedAt(), w.UpdatedAt(),
	)
	if err != nil {
		if isUniqueViolation(err, "wallets_player_currency_unique") {
			return ErrWalletAlreadyExists
		}
		return fmt.Errorf("inserting wallet: %w", err)
	}
	return nil
}

func (r *WalletRepository) FindByID(ctx context.Context, q Querier, id uuid.UUID) (*domain.Wallet, error) {
	row := q.QueryRow(ctx, `
		SELECT id, player_id, currency, balance_minor, version, created_at, updated_at
		FROM wallets WHERE id = $1`, id)

	return scanWallet(row)
}

func (r *WalletRepository) FindByPlayerAndCurrency(
	ctx context.Context, q Querier, playerID uuid.UUID, currency string,
) (*domain.Wallet, error) {
	row := q.QueryRow(ctx, `
		SELECT id, player_id, currency, balance_minor, version, created_at, updated_at
		FROM wallets WHERE player_id = $1 AND currency = $2`, playerID, currency)

	return scanWallet(row)
}

func (r *WalletRepository) UpdateBalance(
	ctx context.Context, q Querier, w *domain.Wallet, expectedVersion int64,
) error {
	tag, err := q.Exec(ctx, `
		UPDATE wallets
		   SET balance_minor = $1, version = $2, updated_at = $3
		 WHERE id = $4 AND version = $5`,
		w.Balance().MinorUnits(), w.Version(), w.UpdatedAt(), w.ID(), expectedVersion,
	)
	if err != nil {
		return fmt.Errorf("updating wallet balance: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConcurrencyConflict
	}
	return nil
}

func (r *WalletRepository) SumLedger(ctx context.Context, q Querier, walletID uuid.UUID) (int64, int, error) {
	var total *int64
	var count int

	err := q.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN direction = 'CREDIT' THEN amount_minor ELSE -amount_minor END), 0),
			COUNT(*)
		FROM wallet_ledger_entries
		WHERE wallet_id = $1`, walletID).Scan(&total, &count)
	if err != nil {
		return 0, 0, fmt.Errorf("summing ledger: %w", err)
	}
	if total == nil {
		return 0, count, nil
	}
	return *total, count, nil
}

func scanWallet(row pgx.Row) (*domain.Wallet, error) {
	var (
		id, playerID         uuid.UUID
		currency             string
		balanceMinor         int64
		version              int64
		createdAt, updatedAt time.Time
	)

	if err := row.Scan(&id, &playerID, &currency, &balanceMinor, &version, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrWalletNotFound
		}
		return nil, fmt.Errorf("scanning wallet: %w", err)
	}

	balance, err := domain.FromMinorUnits(balanceMinor, currency)
	if err != nil {
		return nil, fmt.Errorf("rebuilding wallet balance: %w", err)
	}

	return domain.RehydrateWallet(id, playerID, balance, version, createdAt, updatedAt)
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	if pgErr.Code != "23505" {
		return false
	}
	return constraint == "" || pgErr.ConstraintName == constraint
}
