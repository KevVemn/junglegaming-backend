package events

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/KevVemn/junglegaming-backend/internal/domain"
)

const (
	TypeWagerTransactionProcessed        = "WagerTransactionProcessed"
	TypeWagerTransactionRejected         = "WagerTransactionRejected"
	TypeWalletBalanceChanged             = "WalletBalanceChanged"
	TypeWagerTransactionPendingReference = "WagerTransactionPendingReference"
)

const (
	AggregateWallet      = "Wallet"
	AggregateTransaction = "WagerTransaction"
)

const currentVersion = 1

type Envelope struct {
	EventID       uuid.UUID       `json:"eventId"`
	EventType     string          `json:"eventType"`
	AggregateID   uuid.UUID       `json:"aggregateId"`
	AggregateType string          `json:"aggregateType"`
	CorrelationID string          `json:"correlationId"`
	CausationID   string          `json:"causationId,omitempty"`
	OccurredAt    time.Time       `json:"occurredAt"`
	Version       int             `json:"version"`
	Data          json.RawMessage `json:"data"`
}

func (e Envelope) Marshal() ([]byte, error) {
	return json.Marshal(e)
}

type WagerTransactionProcessedData struct {
	TransactionID         uuid.UUID    `json:"transactionId"`
	WalletID              uuid.UUID    `json:"walletId"`
	PlayerID              uuid.UUID    `json:"playerId"`
	ProviderID            string       `json:"providerId,omitempty"`
	ExternalTransactionID string       `json:"externalTransactionId,omitempty"`
	RoundID               string       `json:"roundId,omitempty"`
	GameID                string       `json:"gameId,omitempty"`
	Kind                  string       `json:"kind"`
	Money                 domain.Money `json:"money"`
	Balance               domain.Money `json:"balance"`
	ProcessedAt           time.Time    `json:"processedAt"`
}

type WagerTransactionRejectedData struct {
	TransactionID         uuid.UUID    `json:"transactionId"`
	WalletID              uuid.UUID    `json:"walletId"`
	PlayerID              uuid.UUID    `json:"playerId"`
	ProviderID            string       `json:"providerId,omitempty"`
	ExternalTransactionID string       `json:"externalTransactionId,omitempty"`
	Kind                  string       `json:"kind"`
	Money                 domain.Money `json:"money"`
	FailureCode           string       `json:"failureCode"`
	RejectedAt            time.Time    `json:"rejectedAt"`
}

type WalletBalanceChangedData struct {
	WalletID      uuid.UUID    `json:"walletId"`
	TransactionID uuid.UUID    `json:"transactionId"`
	Direction     string       `json:"direction"`
	Money         domain.Money `json:"money"`
	BalanceBefore domain.Money `json:"balanceBefore"`
	BalanceAfter  domain.Money `json:"balanceAfter"`
	WalletVersion int64        `json:"walletVersion"`
	ChangedAt     time.Time    `json:"changedAt"`
}

type WagerTransactionPendingReferenceData struct {
	TransactionID         uuid.UUID `json:"transactionId"`
	WalletID              uuid.UUID `json:"walletId"`
	ProviderID            string    `json:"providerId"`
	ExternalTransactionID string    `json:"externalTransactionId"`
	Kind                  string    `json:"kind"`
	ReferenceExternalID   string    `json:"referenceExternalTransactionId"`
	Attempts              int       `json:"attempts"`
	PendingSince          time.Time `json:"pendingSince"`
}

func newEnvelope(
	eventType, aggregateType string,
	aggregateID uuid.UUID,
	correlationID, causationID string,
	occurredAt time.Time,
	data any,
) (Envelope, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Envelope{}, err
	}

	return Envelope{
		EventID:       uuid.New(),
		EventType:     eventType,
		AggregateID:   aggregateID,
		AggregateType: aggregateType,
		CorrelationID: correlationID,
		CausationID:   causationID,
		OccurredAt:    occurredAt.UTC(),
		Version:       currentVersion,
		Data:          raw,
	}, nil
}

func NewWagerTransactionProcessed(
	t *domain.WagerTransaction, balance domain.Money,
	correlationID string, occurredAt time.Time,
) (Envelope, error) {
	return newEnvelope(
		TypeWagerTransactionProcessed, AggregateTransaction, t.ID(),
		correlationID, t.ID().String(), occurredAt,
		WagerTransactionProcessedData{
			TransactionID:         t.ID(),
			WalletID:              t.WalletID(),
			PlayerID:              t.PlayerID(),
			ProviderID:            t.ProviderID(),
			ExternalTransactionID: t.ExternalTransactionID(),
			RoundID:               t.RoundID(),
			GameID:                t.GameID(),
			Kind:                  string(t.Kind()),
			Money:                 t.Money(),
			Balance:               balance,
			ProcessedAt:           occurredAt.UTC(),
		},
	)
}

func NewWagerTransactionRejected(
	t *domain.WagerTransaction, code domain.FailureCode,
	correlationID string, occurredAt time.Time,
) (Envelope, error) {
	return newEnvelope(
		TypeWagerTransactionRejected, AggregateTransaction, t.ID(),
		correlationID, t.ID().String(), occurredAt,
		WagerTransactionRejectedData{
			TransactionID:         t.ID(),
			WalletID:              t.WalletID(),
			PlayerID:              t.PlayerID(),
			ProviderID:            t.ProviderID(),
			ExternalTransactionID: t.ExternalTransactionID(),
			Kind:                  string(t.Kind()),
			Money:                 t.Money(),
			FailureCode:           string(code),
			RejectedAt:            occurredAt.UTC(),
		},
	)
}

func NewWalletBalanceChanged(
	entry *domain.WalletLedgerEntry, walletVersion int64,
	correlationID string, occurredAt time.Time,
) (Envelope, error) {
	return newEnvelope(
		TypeWalletBalanceChanged, AggregateWallet, entry.WalletID(),
		correlationID, entry.TransactionID().String(), occurredAt,
		WalletBalanceChangedData{
			WalletID:      entry.WalletID(),
			TransactionID: entry.TransactionID(),
			Direction:     string(entry.Direction()),
			Money:         entry.Amount(),
			BalanceBefore: entry.BalanceBefore(),
			BalanceAfter:  entry.BalanceAfter(),
			WalletVersion: walletVersion,
			ChangedAt:     occurredAt.UTC(),
		},
	)
}

func NewWagerTransactionPendingReference(
	t *domain.WagerTransaction, attempts int,
	correlationID string, occurredAt time.Time,
) (Envelope, error) {
	return newEnvelope(
		TypeWagerTransactionPendingReference, AggregateTransaction, t.ID(),
		correlationID, t.ID().String(), occurredAt,
		WagerTransactionPendingReferenceData{
			TransactionID:         t.ID(),
			WalletID:              t.WalletID(),
			ProviderID:            t.ProviderID(),
			ExternalTransactionID: t.ExternalTransactionID(),
			Kind:                  string(t.Kind()),
			ReferenceExternalID:   t.ReferenceExternalID(),
			Attempts:              attempts,
			PendingSince:          occurredAt.UTC(),
		},
	)
}
