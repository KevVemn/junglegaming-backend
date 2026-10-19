package httpapi

import (
	"time"

	"github.com/google/uuid"

	"github.com/KevVemn/junglegaming-backend/internal/domain"
	"github.com/KevVemn/junglegaming-backend/internal/postgres"
	"github.com/KevVemn/junglegaming-backend/internal/service"
)

type OpenWalletRequest struct {
	PlayerID       string        `json:"playerId"`
	InitialBalance *domain.Money `json:"initialBalance"`
}

type WalletResponse struct {
	ID       uuid.UUID    `json:"id"`
	PlayerID uuid.UUID    `json:"playerId"`
	Balance  domain.Money `json:"balance"`
	Version  int64        `json:"version"`
}

func walletResponse(w *domain.Wallet) WalletResponse {
	return WalletResponse{
		ID:       w.ID(),
		PlayerID: w.PlayerID(),
		Balance:  w.Balance(),
		Version:  w.Version(),
	}
}

type SubmitTransactionRequest struct {
	ProviderID            string        `json:"providerId"`
	ExternalTransactionID string        `json:"externalTransactionId"`
	PlayerID              string        `json:"playerId"`
	WalletID              string        `json:"walletId"`
	RoundID               string        `json:"roundId"`
	GameID                string        `json:"gameId"`
	Kind                  string        `json:"kind"`
	Money                 *domain.Money `json:"money"`
	ReferenceExternalID   string        `json:"referenceExternalTransactionId"`
}

type SubmitTransactionResponse struct {
	TransactionID    uuid.UUID     `json:"transactionId"`
	Status           string        `json:"status"`
	Balance          *domain.Money `json:"balance,omitempty"`
	FailureCode      string        `json:"failureCode,omitempty"`
	IdempotentReplay bool          `json:"idempotentReplay"`
}

func submitResponse(r *service.SubmitResult) SubmitTransactionResponse {
	resp := SubmitTransactionResponse{
		TransactionID:    r.TransactionID,
		Status:           string(r.State),
		IdempotentReplay: r.IdempotentReplay,
	}
	if r.Balance.IsInitialized() {
		balance := r.Balance
		resp.Balance = &balance
	}
	if r.FailureCode != "" {
		resp.FailureCode = string(r.FailureCode)
	}
	return resp
}

type TransactionResponse struct {
	TransactionID         uuid.UUID     `json:"transactionId"`
	WalletID              uuid.UUID     `json:"walletId"`
	PlayerID              uuid.UUID     `json:"playerId"`
	ProviderID            string        `json:"providerId,omitempty"`
	ExternalTransactionID string        `json:"externalTransactionId,omitempty"`
	RoundID               string        `json:"roundId,omitempty"`
	GameID                string        `json:"gameId,omitempty"`
	Kind                  string        `json:"kind"`
	Money                 domain.Money  `json:"money"`
	Status                string        `json:"status"`
	Balance               *domain.Money `json:"balance,omitempty"`
	FailureCode           string        `json:"failureCode,omitempty"`
	ReferenceExternalID   string        `json:"referenceExternalTransactionId,omitempty"`
	CreatedAt             time.Time     `json:"createdAt"`
	UpdatedAt             time.Time     `json:"updatedAt"`
}

func transactionResponse(t *domain.WagerTransaction) TransactionResponse {
	resp := TransactionResponse{
		TransactionID:         t.ID(),
		WalletID:              t.WalletID(),
		PlayerID:              t.PlayerID(),
		ProviderID:            t.ProviderID(),
		ExternalTransactionID: t.ExternalTransactionID(),
		RoundID:               t.RoundID(),
		GameID:                t.GameID(),
		Kind:                  string(t.Kind()),
		Money:                 t.Money(),
		Status:                string(t.State()),
		FailureCode:           string(t.FailureCode()),
		ReferenceExternalID:   t.ReferenceExternalID(),
		CreatedAt:             t.CreatedAt(),
		UpdatedAt:             t.UpdatedAt(),
	}
	if t.ResultBalance() != nil {
		balance := *t.ResultBalance()
		resp.Balance = &balance
	}
	return resp
}

type LedgerEntryResponse struct {
	ID            uuid.UUID    `json:"id"`
	TransactionID uuid.UUID    `json:"transactionId"`
	Direction     string       `json:"direction"`
	Money         domain.Money `json:"money"`
	BalanceBefore domain.Money `json:"balanceBefore"`
	BalanceAfter  domain.Money `json:"balanceAfter"`
	CreatedAt     time.Time    `json:"createdAt"`
}

type LedgerPageResponse struct {
	Entries    []LedgerEntryResponse `json:"entries"`
	NextCursor string                `json:"nextCursor,omitempty"`
}

func ledgerPageResponse(page *postgres.LedgerPage) LedgerPageResponse {
	entries := make([]LedgerEntryResponse, 0, len(page.Entries))
	for _, e := range page.Entries {
		entries = append(entries, LedgerEntryResponse{
			ID:            e.ID(),
			TransactionID: e.TransactionID(),
			Direction:     string(e.Direction()),
			Money:         e.Amount(),
			BalanceBefore: e.BalanceBefore(),
			BalanceAfter:  e.BalanceAfter(),
			CreatedAt:     e.CreatedAt(),
		})
	}
	return LedgerPageResponse{Entries: entries, NextCursor: page.NextCursor}
}

type ReconciliationResponse struct {
	WalletID          uuid.UUID    `json:"walletId"`
	StoredBalance     domain.Money `json:"storedBalance"`
	CalculatedBalance domain.Money `json:"calculatedBalance"`
	Difference        domain.Money `json:"difference"`
	Consistent        bool         `json:"consistent"`
	CheckedEntries    int          `json:"checkedEntries"`
}

type ErrorResponse struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	CorrelationID string `json:"correlationId,omitempty"`
}

type HealthResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}
