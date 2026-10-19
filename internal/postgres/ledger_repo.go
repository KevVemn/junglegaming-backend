package postgres

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/KevVemn/junglegaming-backend/internal/domain"
)

var (
	ErrLedgerEntryExists = errors.New("ledger entry already exists for wallet and transaction")
	ErrInvalidCursor     = errors.New("invalid pagination cursor")
)

type LedgerRepository struct{}

func NewLedgerRepository() *LedgerRepository { return &LedgerRepository{} }

func (r *LedgerRepository) Insert(ctx context.Context, q Querier, e *domain.WalletLedgerEntry) error {
	_, err := q.Exec(ctx, `
		INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction, amount_minor, currency,
			balance_before_minor, balance_after_minor, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		e.ID(), e.WalletID(), e.TransactionID(), string(e.Direction()),
		e.Amount().MinorUnits(), e.Amount().Currency(),
		e.BalanceBefore().MinorUnits(), e.BalanceAfter().MinorUnits(), e.CreatedAt(),
	)
	if err != nil {
		if isUniqueViolation(err, "ledger_wallet_transaction_unique") {
			return ErrLedgerEntryExists
		}
		return fmt.Errorf("inserting ledger entry: %w", err)
	}
	return nil
}

type LedgerPage struct {
	Entries    []*domain.WalletLedgerEntry
	NextCursor string
}

func (r *LedgerRepository) ListByWallet(
	ctx context.Context, q Querier, walletID uuid.UUID, cursor string, limit int,
) (*LedgerPage, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	var (
		rows interface {
			Next() bool
			Scan(dest ...any) error
			Close()
			Err() error
		}
		err error
	)

	if cursor == "" {
		rows, err = q.Query(ctx, `
			SELECT id, wallet_id, transaction_id, direction, amount_minor, currency,
			       balance_before_minor, balance_after_minor, created_at
			  FROM wallet_ledger_entries
			 WHERE wallet_id = $1
			 ORDER BY created_at, id
			 LIMIT $2`, walletID, limit+1)
	} else {
		cursorTime, cursorID, decodeErr := decodeCursor(cursor)
		if decodeErr != nil {
			return nil, decodeErr
		}
		rows, err = q.Query(ctx, `
			SELECT id, wallet_id, transaction_id, direction, amount_minor, currency,
			       balance_before_minor, balance_after_minor, created_at
			  FROM wallet_ledger_entries
			 WHERE wallet_id = $1 AND (created_at, id) > ($2, $3)
			 ORDER BY created_at, id
			 LIMIT $4`, walletID, cursorTime, cursorID, limit+1)
	}
	if err != nil {
		return nil, fmt.Errorf("listing ledger entries: %w", err)
	}
	defer rows.Close()

	entries := make([]*domain.WalletLedgerEntry, 0, limit)
	for rows.Next() {
		var (
			id, wID, txID uuid.UUID
			direction     string
			amountMinor   int64
			currency      string
			beforeMinor   int64
			afterMinor    int64
			createdAt     time.Time
		)
		if err := rows.Scan(&id, &wID, &txID, &direction, &amountMinor, &currency,
			&beforeMinor, &afterMinor, &createdAt); err != nil {
			return nil, fmt.Errorf("scanning ledger entry: %w", err)
		}

		amount, err := domain.FromMinorUnits(amountMinor, currency)
		if err != nil {
			return nil, err
		}
		before, err := domain.FromMinorUnits(beforeMinor, currency)
		if err != nil {
			return nil, err
		}
		after, err := domain.FromMinorUnits(afterMinor, currency)
		if err != nil {
			return nil, err
		}

		entry, err := domain.RehydrateWalletLedgerEntry(
			id, wID, txID, domain.LedgerDirection(direction),
			amount, before, after, createdAt,
		)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	page := &LedgerPage{Entries: entries}
	if len(entries) > limit {
		page.Entries = entries[:limit]
		last := page.Entries[len(page.Entries)-1]
		page.NextCursor = encodeCursor(last.CreatedAt(), last.ID())
	}

	return page, nil
}

func encodeCursor(createdAt time.Time, id uuid.UUID) string {
	raw := createdAt.UTC().Format(time.RFC3339Nano) + "|" + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(cursor string) (time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}

	timePart, idPart, found := strings.Cut(string(raw), "|")
	if !found {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}

	parsedTime, err := time.Parse(time.RFC3339Nano, timePart)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}

	parsedID, err := uuid.Parse(idPart)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}

	return parsedTime, parsedID, nil
}
