package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrInboxDuplicate    = errors.New("message already received by this consumer")
	ErrInboxHashMismatch = errors.New("message id reused with a different payload")
)

type InboxRecord struct {
	ConsumerName string
	MessageID    string
	MessageHash  string
	ReceivedAt   time.Time
	CompletedAt  *time.Time
}

type InboxRepository struct{}

func NewInboxRepository() *InboxRepository { return &InboxRepository{} }

func (r *InboxRepository) Claim(
	ctx context.Context, q Querier, consumerName, messageID, messageHash string, now time.Time,
) error {
	tag, err := q.Exec(ctx, `
		INSERT INTO inbox_messages (consumer_name, message_id, message_hash, received_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (consumer_name, message_id) DO NOTHING`,
		consumerName, messageID, messageHash, now,
	)
	if err != nil {
		return fmt.Errorf("claiming inbox message: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}

	existing, err := r.Find(ctx, q, consumerName, messageID)
	if err != nil {
		return err
	}
	if existing.MessageHash != messageHash {
		return ErrInboxHashMismatch
	}
	return ErrInboxDuplicate
}

func (r *InboxRepository) Find(
	ctx context.Context, q Querier, consumerName, messageID string,
) (*InboxRecord, error) {
	var record InboxRecord
	err := q.QueryRow(ctx, `
		SELECT consumer_name, message_id, message_hash, received_at, completed_at
		  FROM inbox_messages
		 WHERE consumer_name = $1 AND message_id = $2`, consumerName, messageID).
		Scan(&record.ConsumerName, &record.MessageID, &record.MessageHash,
			&record.ReceivedAt, &record.CompletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("inbox record not found")
		}
		return nil, fmt.Errorf("reading inbox record: %w", err)
	}
	return &record, nil
}

func (r *InboxRepository) MarkCompleted(
	ctx context.Context, q Querier, consumerName, messageID string, now time.Time,
) error {
	_, err := q.Exec(ctx, `
		UPDATE inbox_messages
		   SET completed_at = $1
		 WHERE consumer_name = $2 AND message_id = $3`, now, consumerName, messageID)
	if err != nil {
		return fmt.Errorf("completing inbox message: %w", err)
	}
	return nil
}

func (r *InboxRepository) IsCompleted(
	ctx context.Context, q Querier, consumerName, messageID string,
) (bool, error) {
	var completed *time.Time
	err := q.QueryRow(ctx, `
		SELECT completed_at FROM inbox_messages
		 WHERE consumer_name = $1 AND message_id = $2`, consumerName, messageID).Scan(&completed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("checking inbox completion: %w", err)
	}
	return completed != nil, nil
}
