package messaging

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/KevVemn/junglegaming-backend/internal/domain"
)

var (
	ErrMalformedMessage = errors.New("message body is not a valid wager transaction envelope")
	ErrUnsupportedType  = errors.New("unsupported message type")
)

const TypeWagerTransactionRequested = "WagerTransactionRequested"

type InboundEnvelope struct {
	MessageID  string                 `json:"messageId"`
	Type       string                 `json:"type"`
	OccurredAt time.Time              `json:"occurredAt"`
	Data       WagerTransactionReques `json:"data"`
}

type WagerTransactionReques struct {
	ProviderID            string       `json:"providerId"`
	ExternalTransactionID string       `json:"externalTransactionId"`
	IdempotencyKey        string       `json:"idempotencyKey"`
	PlayerID              string       `json:"playerId"`
	WalletID              string       `json:"walletId"`
	RoundID               string       `json:"roundId"`
	GameID                string       `json:"gameId"`
	Kind                  string       `json:"kind"`
	Money                 domain.Money `json:"money"`
	ReferenceExternalID   string       `json:"referenceExternalTransactionId"`
}

type ParsedMessage struct {
	Envelope InboundEnvelope
	PlayerID uuid.UUID
	WalletID uuid.UUID
	Kind     domain.TransactionKind
	BodyHash string
}

func ParseInbound(body string) (*ParsedMessage, error) {
	var envelope InboundEnvelope
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedMessage, err)
	}

	if envelope.Type != "" && envelope.Type != TypeWagerTransactionRequested {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedType, envelope.Type)
	}
	if envelope.MessageID == "" {
		return nil, fmt.Errorf("%w: messageId is required", ErrMalformedMessage)
	}
	if envelope.Data.IdempotencyKey == "" {
		return nil, fmt.Errorf("%w: data.idempotencyKey is required", ErrMalformedMessage)
	}

	playerID, err := uuid.Parse(envelope.Data.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("%w: playerId is not a uuid", ErrMalformedMessage)
	}
	walletID, err := uuid.Parse(envelope.Data.WalletID)
	if err != nil {
		return nil, fmt.Errorf("%w: walletId is not a uuid", ErrMalformedMessage)
	}

	kind := domain.TransactionKind(envelope.Data.Kind)
	if !kind.IsExternal() {
		return nil, fmt.Errorf("%w: %s", ErrMalformedMessage, domain.ErrInternalKindNotAllowed)
	}

	return &ParsedMessage{
		Envelope: envelope,
		PlayerID: playerID,
		WalletID: walletID,
		Kind:     kind,
		BodyHash: HashBody(body),
	}, nil
}

func HashBody(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}
