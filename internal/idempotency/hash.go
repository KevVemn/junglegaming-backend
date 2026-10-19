package idempotency

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/KevVemn/junglegaming-backend/internal/domain"
)

type BusinessFields struct {
	ProviderID            string
	ExternalTransactionID string
	PlayerID              string
	WalletID              string
	RoundID               string
	GameID                string
	Kind                  string
	Money                 domain.Money
	ReferenceExternalID   string
}

func Hash(f BusinessFields) (string, error) {
	if !f.Money.IsInitialized() {
		return "", domain.ErrUninitializedMoney
	}

	canonical := map[string]string{
		"providerId":                     f.ProviderID,
		"externalTransactionId":          f.ExternalTransactionID,
		"playerId":                       f.PlayerID,
		"walletId":                       f.WalletID,
		"roundId":                        f.RoundID,
		"gameId":                         f.GameID,
		"kind":                           f.Kind,
		"amount":                         f.Money.String(),
		"currency":                       f.Money.Currency(),
		"referenceExternalTransactionId": f.ReferenceExternalID,
	}

	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("serializing canonical payload: %w", err)
	}

	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
