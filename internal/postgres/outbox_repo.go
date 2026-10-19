package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type OutboxRecord struct {
	EventID       uuid.UUID
	AggregateID   uuid.UUID
	AggregateType string
	EventType     string
	Payload       []byte
	OccurredAt    time.Time
	Attempts      int
	NextRetryAt   time.Time
	CreatedAt     time.Time
}

type OutboxRepository struct{}

func NewOutboxRepository() *OutboxRepository { return &OutboxRepository{} }

func (r *OutboxRepository) Insert(ctx context.Context, q Querier, rec OutboxRecord) error {
	_, err := q.Exec(ctx, `
		INSERT INTO outbox_events (
			event_id, aggregate_id, aggregate_type, event_type, payload,
			occurred_at, attempts, next_retry_at, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, 0, $7, $8)
		ON CONFLICT (event_id) DO NOTHING`,
		rec.EventID, rec.AggregateID, rec.AggregateType, rec.EventType, rec.Payload,
		rec.OccurredAt, rec.OccurredAt, rec.OccurredAt,
	)
	if err != nil {
		return fmt.Errorf("inserting outbox event: %w", err)
	}
	return nil
}

func (r *OutboxRepository) ClaimBatch(
	ctx context.Context, q Querier, limit int, now time.Time, lockUntil time.Time,
) ([]OutboxRecord, error) {
	rows, err := q.Query(ctx, `
		WITH claimed AS (
			SELECT event_id
			  FROM outbox_events
			 WHERE published_at IS NULL
			   AND next_retry_at <= $1
			   AND (locked_until IS NULL OR locked_until <= $1)
			 ORDER BY occurred_at
			 LIMIT $2
			 FOR UPDATE SKIP LOCKED
		)
		UPDATE outbox_events o
		   SET locked_until = $3
		  FROM claimed c
		 WHERE o.event_id = c.event_id
		RETURNING o.event_id, o.aggregate_id, o.aggregate_type, o.event_type,
		          o.payload, o.occurred_at, o.attempts, o.next_retry_at, o.created_at`,
		now, limit, lockUntil,
	)
	if err != nil {
		return nil, fmt.Errorf("claiming outbox batch: %w", err)
	}
	defer rows.Close()

	records := make([]OutboxRecord, 0, limit)
	for rows.Next() {
		var rec OutboxRecord
		if err := rows.Scan(
			&rec.EventID, &rec.AggregateID, &rec.AggregateType, &rec.EventType,
			&rec.Payload, &rec.OccurredAt, &rec.Attempts, &rec.NextRetryAt, &rec.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning outbox record: %w", err)
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}

func (r *OutboxRepository) MarkPublished(
	ctx context.Context, q Querier, eventID uuid.UUID, now time.Time,
) error {
	_, err := q.Exec(ctx, `
		UPDATE outbox_events
		   SET published_at = $1, locked_until = NULL, last_error = NULL
		 WHERE event_id = $2 AND published_at IS NULL`, now, eventID)
	if err != nil {
		return fmt.Errorf("marking outbox event as published: %w", err)
	}
	return nil
}

func (r *OutboxRepository) MarkFailed(
	ctx context.Context, q Querier, eventID uuid.UUID, nextRetryAt time.Time, reason string,
) error {
	_, err := q.Exec(ctx, `
		UPDATE outbox_events
		   SET attempts = attempts + 1,
		       next_retry_at = $1,
		       locked_until = NULL,
		       last_error = $2
		 WHERE event_id = $3`, nextRetryAt, reason, eventID)
	if err != nil {
		return fmt.Errorf("marking outbox event as failed: %w", err)
	}
	return nil
}

func (r *OutboxRepository) PendingCount(ctx context.Context, q Querier) (int64, error) {
	var count int64
	if err := q.QueryRow(ctx,
		`SELECT COUNT(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&count); err != nil {
		return 0, fmt.Errorf("counting pending outbox events: %w", err)
	}
	return count, nil
}

func (r *OutboxRepository) OldestPendingAge(ctx context.Context, q Querier, now time.Time) (time.Duration, error) {
	var oldest *time.Time
	err := q.QueryRow(ctx,
		`SELECT MIN(occurred_at) FROM outbox_events WHERE published_at IS NULL`).Scan(&oldest)
	if err != nil {
		return 0, fmt.Errorf("reading outbox lag: %w", err)
	}
	if oldest == nil {
		return 0, nil
	}
	return now.Sub(*oldest), nil
}
