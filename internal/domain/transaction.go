package domain

import (
	"time"

	"github.com/google/uuid"
)

type TransactionKind string

const (
	KindOpening  TransactionKind = "OPENING"
	KindBet      TransactionKind = "BET"
	KindWin      TransactionKind = "WIN"
	KindLoss     TransactionKind = "LOSS"
	KindRefund   TransactionKind = "REFUND"
	KindRollback TransactionKind = "ROLLBACK"
)

func (k TransactionKind) IsExternal() bool {
	switch k {
	case KindBet, KindWin, KindLoss, KindRefund, KindRollback:
		return true
	default:
		return false
	}
}

func (k TransactionKind) Valid() bool {
	return k == KindOpening || k.IsExternal()
}

func (k TransactionKind) RequiresReference() bool {
	return k == KindRefund || k == KindRollback
}

func (k TransactionKind) String() string { return string(k) }

type TransactionState string

const (
	StatePending          TransactionState = "PENDING"
	StatePendingReference TransactionState = "PENDING_REFERENCE"
	StateProcessed        TransactionState = "PROCESSED"
	StateRejected         TransactionState = "REJECTED"
	StateFailed           TransactionState = "FAILED"
)

func (s TransactionState) IsTerminal() bool {
	return s == StateProcessed || s == StateRejected || s == StateFailed
}

func (s TransactionState) Valid() bool {
	switch s {
	case StatePending, StatePendingReference, StateProcessed, StateRejected, StateFailed:
		return true
	default:
		return false
	}
}

func (s TransactionState) String() string { return string(s) }

type WagerTransaction struct {
	id       uuid.UUID
	walletID uuid.UUID
	playerID uuid.UUID
	kind     TransactionKind
	money    Money
	state    TransactionState

	providerID            string
	externalTransactionID string
	idempotencyKey        string
	payloadHash           string
	roundID               string
	gameID                string

	referenceExternalID string
	referenceInternalID *uuid.UUID

	failureCode  FailureCode
	resultBalanc *Money

	createdAt time.Time
	updatedAt time.Time
}

type ExternalTransactionInput struct {
	ID                    uuid.UUID
	WalletID              uuid.UUID
	PlayerID              uuid.UUID
	ProviderID            string
	ExternalTransactionID string
	IdempotencyKey        string
	PayloadHash           string
	RoundID               string
	GameID                string
	Kind                  TransactionKind
	Money                 Money
	ReferenceExternalID   string
}

func NewExternalTransaction(in ExternalTransactionInput, now time.Time) (*WagerTransaction, error) {
	if in.ID == uuid.Nil {
		return nil, ErrInvalidTransactionID
	}
	if in.WalletID == uuid.Nil {
		return nil, ErrInvalidWalletID
	}
	if in.PlayerID == uuid.Nil {
		return nil, ErrInvalidPlayerID
	}
	if !in.Kind.Valid() {
		return nil, ErrInvalidTransactionKind
	}
	if !in.Kind.IsExternal() {
		return nil, ErrInternalKindNotAllowed
	}
	if in.ProviderID == "" {
		return nil, ErrInvalidProviderID
	}
	if in.ExternalTransactionID == "" {
		return nil, ErrInvalidExternalID
	}
	if in.IdempotencyKey == "" || in.PayloadHash == "" {
		return nil, ErrInvalidIdempotencyKey
	}
	if !in.Money.IsInitialized() {
		return nil, ErrUninitializedMoney
	}
	if in.Money.IsNegative() {
		return nil, ErrNegativeAmount
	}

	if in.Kind == KindLoss {
		if !in.Money.IsZero() {
			return nil, ErrInvalidTransactionKind
		}
	} else if !in.Money.IsPositive() {
		return nil, ErrNonPositiveAmount
	}

	if in.Kind.RequiresReference() && in.ReferenceExternalID == "" {
		return nil, ErrReferenceRequired
	}
	if in.Kind == KindBet && in.ReferenceExternalID != "" {
		return nil, ErrReferenceNotAllowed
	}

	return &WagerTransaction{
		id:                    in.ID,
		walletID:              in.WalletID,
		playerID:              in.PlayerID,
		kind:                  in.Kind,
		money:                 in.Money,
		state:                 StatePending,
		providerID:            in.ProviderID,
		externalTransactionID: in.ExternalTransactionID,
		idempotencyKey:        in.IdempotencyKey,
		payloadHash:           in.PayloadHash,
		roundID:               in.RoundID,
		gameID:                in.GameID,
		referenceExternalID:   in.ReferenceExternalID,
		createdAt:             now.UTC(),
		updatedAt:             now.UTC(),
	}, nil
}

func NewOpeningTransaction(id, walletID, playerID uuid.UUID, amount Money, now time.Time) (*WagerTransaction, error) {
	if id == uuid.Nil {
		return nil, ErrInvalidTransactionID
	}
	if walletID == uuid.Nil {
		return nil, ErrInvalidWalletID
	}
	if playerID == uuid.Nil {
		return nil, ErrInvalidPlayerID
	}
	if !amount.IsInitialized() {
		return nil, ErrUninitializedMoney
	}
	if !amount.IsPositive() {
		return nil, ErrNonPositiveAmount
	}

	return &WagerTransaction{
		id:        id,
		walletID:  walletID,
		playerID:  playerID,
		kind:      KindOpening,
		money:     amount,
		state:     StatePending,
		createdAt: now.UTC(),
		updatedAt: now.UTC(),
	}, nil
}

type RehydrateTransactionInput struct {
	ExternalTransactionInput
	State               TransactionState
	ReferenceInternalID *uuid.UUID
	FailureCode         FailureCode
	ResultBalance       *Money
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func RehydrateTransaction(in RehydrateTransactionInput) (*WagerTransaction, error) {
	if in.ID == uuid.Nil {
		return nil, ErrInvalidTransactionID
	}
	if !in.Kind.Valid() {
		return nil, ErrInvalidTransactionKind
	}
	if !in.State.Valid() {
		return nil, ErrInvalidTransactionState
	}

	return &WagerTransaction{
		id:                    in.ID,
		walletID:              in.WalletID,
		playerID:              in.PlayerID,
		kind:                  in.Kind,
		money:                 in.Money,
		state:                 in.State,
		providerID:            in.ProviderID,
		externalTransactionID: in.ExternalTransactionID,
		idempotencyKey:        in.IdempotencyKey,
		payloadHash:           in.PayloadHash,
		roundID:               in.RoundID,
		gameID:                in.GameID,
		referenceExternalID:   in.ReferenceExternalID,
		referenceInternalID:   in.ReferenceInternalID,
		failureCode:           in.FailureCode,
		resultBalanc:          in.ResultBalance,
		createdAt:             in.CreatedAt.UTC(),
		updatedAt:             in.UpdatedAt.UTC(),
	}, nil
}

func (t *WagerTransaction) ID() uuid.UUID                   { return t.id }
func (t *WagerTransaction) WalletID() uuid.UUID             { return t.walletID }
func (t *WagerTransaction) PlayerID() uuid.UUID             { return t.playerID }
func (t *WagerTransaction) Kind() TransactionKind           { return t.kind }
func (t *WagerTransaction) Money() Money                    { return t.money }
func (t *WagerTransaction) State() TransactionState         { return t.state }
func (t *WagerTransaction) ProviderID() string              { return t.providerID }
func (t *WagerTransaction) ExternalTransactionID() string   { return t.externalTransactionID }
func (t *WagerTransaction) IdempotencyKey() string          { return t.idempotencyKey }
func (t *WagerTransaction) PayloadHash() string             { return t.payloadHash }
func (t *WagerTransaction) RoundID() string                 { return t.roundID }
func (t *WagerTransaction) GameID() string                  { return t.gameID }
func (t *WagerTransaction) ReferenceExternalID() string     { return t.referenceExternalID }
func (t *WagerTransaction) ReferenceInternalID() *uuid.UUID { return t.referenceInternalID }
func (t *WagerTransaction) FailureCode() FailureCode        { return t.failureCode }
func (t *WagerTransaction) ResultBalance() *Money           { return t.resultBalanc }
func (t *WagerTransaction) CreatedAt() time.Time            { return t.createdAt }
func (t *WagerTransaction) UpdatedAt() time.Time            { return t.updatedAt }

func (t *WagerTransaction) MarkProcessed(resultBalance Money, now time.Time) error {
	if err := t.assertNotTerminal(); err != nil {
		return err
	}
	if !resultBalance.IsInitialized() {
		return ErrUninitializedMoney
	}

	balance := resultBalance
	t.resultBalanc = &balance
	t.state = StateProcessed
	t.updatedAt = now.UTC()
	return nil
}

func (t *WagerTransaction) MarkPendingReference(now time.Time) error {
	if err := t.assertNotTerminal(); err != nil {
		return err
	}
	if !t.kind.RequiresReference() {
		return ErrInvalidStateTransition
	}

	t.state = StatePendingReference
	t.updatedAt = now.UTC()
	return nil
}

func (t *WagerTransaction) MarkRejected(code FailureCode, now time.Time) error {
	if err := t.assertNotTerminal(); err != nil {
		return err
	}
	if code == "" {
		return ErrInvalidTransactionState
	}

	t.failureCode = code
	t.state = StateRejected
	t.updatedAt = now.UTC()
	return nil
}

func (t *WagerTransaction) MarkFailed(code FailureCode, now time.Time) error {
	if err := t.assertNotTerminal(); err != nil {
		return err
	}
	if code == "" {
		code = FailurePermanentError
	}

	t.failureCode = code
	t.state = StateFailed
	t.updatedAt = now.UTC()
	return nil
}

func (t *WagerTransaction) ResolveReference(referenceID uuid.UUID, now time.Time) error {
	if err := t.assertNotTerminal(); err != nil {
		return err
	}
	if referenceID == uuid.Nil {
		return ErrInvalidTransactionID
	}

	ref := referenceID
	t.referenceInternalID = &ref
	t.updatedAt = now.UTC()
	return nil
}

func (t *WagerTransaction) MovesMoney() bool {
	return t.kind != KindLoss
}

func (t *WagerTransaction) assertNotTerminal() error {
	if t.state.IsTerminal() {
		return ErrTerminalState
	}
	return nil
}
