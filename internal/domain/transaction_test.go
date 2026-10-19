package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func validInput(t *testing.T, kind TransactionKind, amount string) ExternalTransactionInput {
	t.Helper()
	return ExternalTransactionInput{
		ID:                    uuid.New(),
		WalletID:              uuid.New(),
		PlayerID:              uuid.New(),
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-123",
		IdempotencyKey:        "provider-a:transaction-123",
		PayloadHash:           "abc123",
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  kind,
		Money:                 mustParse(t, amount, "BRL"),
	}
}

func TestTransactionKindClassification(t *testing.T) {
	external := []TransactionKind{KindBet, KindWin, KindLoss, KindRefund, KindRollback}
	for _, k := range external {
		if !k.IsExternal() || !k.Valid() {
			t.Errorf("%s must be a valid external kind", k)
		}
	}

	if KindOpening.IsExternal() {
		t.Error("OPENING must not be accepted as external")
	}
	if !KindOpening.Valid() {
		t.Error("OPENING must be a valid kind")
	}
	if TransactionKind("TRANSFER").Valid() {
		t.Error("unknown kinds must be invalid")
	}

	if !KindRefund.RequiresReference() || !KindRollback.RequiresReference() {
		t.Error("REFUND and ROLLBACK must require a reference")
	}
	if KindBet.RequiresReference() || KindWin.RequiresReference() || KindLoss.RequiresReference() {
		t.Error("BET, WIN and LOSS must not require a reference")
	}
}

func TestTransactionStateTerminality(t *testing.T) {
	terminal := []TransactionState{StateProcessed, StateRejected, StateFailed}
	for _, s := range terminal {
		if !s.IsTerminal() {
			t.Errorf("%s must be terminal", s)
		}
	}

	for _, s := range []TransactionState{StatePending, StatePendingReference} {
		if s.IsTerminal() {
			t.Errorf("%s must not be terminal", s)
		}
	}
}

func TestNewExternalTransactionStartsPending(t *testing.T) {
	tx, err := NewExternalTransaction(validInput(t, KindBet, "25.00"), time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tx.State() != StatePending {
		t.Errorf("state = %s, want PENDING", tx.State())
	}
	if tx.Kind() != KindBet {
		t.Errorf("kind = %s, want BET", tx.Kind())
	}
	if tx.Money().String() != "25.00" {
		t.Errorf("money = %s, want 25.00", tx.Money())
	}
}

func TestNewExternalTransactionRejectsOpening(t *testing.T) {
	in := validInput(t, KindOpening, "25.00")

	_, err := NewExternalTransaction(in, time.Now())
	if !errors.Is(err, ErrInternalKindNotAllowed) {
		t.Errorf("error = %v, want ErrInternalKindNotAllowed", err)
	}
}

func TestLossRequiresZeroAmount(t *testing.T) {
	t.Run("zero is accepted", func(t *testing.T) {
		if _, err := NewExternalTransaction(validInput(t, KindLoss, "0.00"), time.Now()); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("non zero is rejected", func(t *testing.T) {
		if _, err := NewExternalTransaction(validInput(t, KindLoss, "10.00"), time.Now()); err == nil {
			t.Error("LOSS with non zero amount should be rejected")
		}
	})
}

func TestMonetaryKindsRequirePositiveAmount(t *testing.T) {
	for _, kind := range []TransactionKind{KindBet, KindWin} {
		t.Run(string(kind), func(t *testing.T) {
			in := validInput(t, kind, "0.00")
			if _, err := NewExternalTransaction(in, time.Now()); !errors.Is(err, ErrNonPositiveAmount) {
				t.Errorf("error = %v, want ErrNonPositiveAmount", err)
			}
		})
	}
}

func TestReversalKindsRequireReference(t *testing.T) {
	for _, kind := range []TransactionKind{KindRefund, KindRollback} {
		t.Run(string(kind)+" without reference", func(t *testing.T) {
			in := validInput(t, kind, "25.00")
			if _, err := NewExternalTransaction(in, time.Now()); !errors.Is(err, ErrReferenceRequired) {
				t.Errorf("error = %v, want ErrReferenceRequired", err)
			}
		})

		t.Run(string(kind)+" with reference", func(t *testing.T) {
			in := validInput(t, kind, "25.00")
			in.ReferenceExternalID = "transaction-000"
			if _, err := NewExternalTransaction(in, time.Now()); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestBetRejectsReference(t *testing.T) {
	in := validInput(t, KindBet, "25.00")
	in.ReferenceExternalID = "transaction-000"

	if _, err := NewExternalTransaction(in, time.Now()); !errors.Is(err, ErrReferenceNotAllowed) {
		t.Errorf("error = %v, want ErrReferenceNotAllowed", err)
	}
}

func TestNewExternalTransactionRequiresIdentifiers(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*ExternalTransactionInput)
		wantErr error
	}{
		{"nil id", func(in *ExternalTransactionInput) { in.ID = uuid.Nil }, ErrInvalidTransactionID},
		{"nil wallet", func(in *ExternalTransactionInput) { in.WalletID = uuid.Nil }, ErrInvalidWalletID},
		{"nil player", func(in *ExternalTransactionInput) { in.PlayerID = uuid.Nil }, ErrInvalidPlayerID},
		{"empty provider", func(in *ExternalTransactionInput) { in.ProviderID = "" }, ErrInvalidProviderID},
		{"empty external id", func(in *ExternalTransactionInput) { in.ExternalTransactionID = "" }, ErrInvalidExternalID},
		{"empty idempotency key", func(in *ExternalTransactionInput) { in.IdempotencyKey = "" }, ErrInvalidIdempotencyKey},
		{"empty payload hash", func(in *ExternalTransactionInput) { in.PayloadHash = "" }, ErrInvalidIdempotencyKey},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := validInput(t, KindBet, "25.00")
			c.mutate(&in)
			if _, err := NewExternalTransaction(in, time.Now()); !errors.Is(err, c.wantErr) {
				t.Errorf("error = %v, want %v", err, c.wantErr)
			}
		})
	}
}

func TestNewOpeningTransaction(t *testing.T) {
	amount := mustParse(t, "1000.00", "BRL")

	tx, err := NewOpeningTransaction(uuid.New(), uuid.New(), uuid.New(), amount, time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tx.Kind() != KindOpening {
		t.Errorf("kind = %s, want OPENING", tx.Kind())
	}
	if tx.ProviderID() != "" || tx.ExternalTransactionID() != "" || tx.IdempotencyKey() != "" {
		t.Error("OPENING must not carry external metadata")
	}
	if tx.RoundID() != "" || tx.GameID() != "" || tx.ReferenceExternalID() != "" {
		t.Error("OPENING must not carry round, game or reference")
	}
}

func TestNewOpeningTransactionRejectsZeroAmount(t *testing.T) {
	zero := mustParse(t, "0.00", "BRL")

	if _, err := NewOpeningTransaction(uuid.New(), uuid.New(), uuid.New(), zero, time.Now()); !errors.Is(err, ErrNonPositiveAmount) {
		t.Errorf("error = %v, want ErrNonPositiveAmount", err)
	}
}

func TestTransactionMarkProcessed(t *testing.T) {
	tx, err := NewExternalTransaction(validInput(t, KindBet, "25.00"), time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	balance := mustParse(t, "975.00", "BRL")
	if err := tx.MarkProcessed(balance, time.Now()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tx.State() != StateProcessed {
		t.Errorf("state = %s, want PROCESSED", tx.State())
	}
	if tx.ResultBalance() == nil || tx.ResultBalance().String() != "975.00" {
		t.Error("result balance not recorded")
	}
}

func TestTransactionMarkRejectedStoresFailureCode(t *testing.T) {
	tx, err := NewExternalTransaction(validInput(t, KindBet, "25.00"), time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := tx.MarkRejected(FailureInsufficientFunds, time.Now()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tx.State() != StateRejected {
		t.Errorf("state = %s, want REJECTED", tx.State())
	}
	if tx.FailureCode() != FailureInsufficientFunds {
		t.Errorf("failure code = %s, want INSUFFICIENT_FUNDS", tx.FailureCode())
	}
}

func TestTerminalStatesRejectFurtherTransitions(t *testing.T) {
	now := time.Now()
	balance := mustParse(t, "100.00", "BRL")

	transitions := map[string]func(*WagerTransaction) error{
		"MarkProcessed":        func(tx *WagerTransaction) error { return tx.MarkProcessed(balance, now) },
		"MarkRejected":         func(tx *WagerTransaction) error { return tx.MarkRejected(FailureInvalidAmount, now) },
		"MarkFailed":           func(tx *WagerTransaction) error { return tx.MarkFailed(FailurePermanentError, now) },
		"MarkPendingReference": func(tx *WagerTransaction) error { return tx.MarkPendingReference(now) },
		"ResolveReference":     func(tx *WagerTransaction) error { return tx.ResolveReference(uuid.New(), now) },
	}

	terminals := map[string]func(*WagerTransaction) error{
		"PROCESSED": func(tx *WagerTransaction) error { return tx.MarkProcessed(balance, now) },
		"REJECTED":  func(tx *WagerTransaction) error { return tx.MarkRejected(FailureInvalidAmount, now) },
		"FAILED":    func(tx *WagerTransaction) error { return tx.MarkFailed(FailurePermanentError, now) },
	}

	for terminalName, reachTerminal := range terminals {
		for transitionName, transition := range transitions {
			t.Run(terminalName+"_then_"+transitionName, func(t *testing.T) {
				tx, err := NewExternalTransaction(validInput(t, KindBet, "25.00"), now)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if err := reachTerminal(tx); err != nil {
					t.Fatalf("unexpected error reaching terminal: %v", err)
				}

				if err := transition(tx); !errors.Is(err, ErrTerminalState) {
					t.Errorf("error = %v, want ErrTerminalState", err)
				}
			})
		}
	}
}

func TestMarkPendingReferenceOnlyForReversals(t *testing.T) {
	now := time.Now()

	t.Run("refund is allowed", func(t *testing.T) {
		in := validInput(t, KindRefund, "25.00")
		in.ReferenceExternalID = "transaction-000"
		tx, err := NewExternalTransaction(in, now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := tx.MarkPendingReference(now); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if tx.State() != StatePendingReference {
			t.Errorf("state = %s, want PENDING_REFERENCE", tx.State())
		}
	})

	t.Run("bet is rejected", func(t *testing.T) {
		tx, err := NewExternalTransaction(validInput(t, KindBet, "25.00"), now)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := tx.MarkPendingReference(now); !errors.Is(err, ErrInvalidStateTransition) {
			t.Errorf("error = %v, want ErrInvalidStateTransition", err)
		}
	})
}

func TestPendingReferenceCanStillBeResolved(t *testing.T) {
	now := time.Now()
	in := validInput(t, KindRollback, "25.00")
	in.ReferenceExternalID = "transaction-000"

	tx, err := NewExternalTransaction(in, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := tx.MarkPendingReference(now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	referenceID := uuid.New()
	if err := tx.ResolveReference(referenceID, now); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tx.ReferenceInternalID() == nil || *tx.ReferenceInternalID() != referenceID {
		t.Error("reference not resolved")
	}

	balance := mustParse(t, "125.00", "BRL")
	if err := tx.MarkProcessed(balance, now); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if tx.State() != StateProcessed {
		t.Errorf("state = %s, want PROCESSED", tx.State())
	}
}

func TestMovesMoney(t *testing.T) {
	now := time.Now()

	loss, err := NewExternalTransaction(validInput(t, KindLoss, "0.00"), now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if loss.MovesMoney() {
		t.Error("LOSS must not move money")
	}

	bet, err := NewExternalTransaction(validInput(t, KindBet, "25.00"), now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bet.MovesMoney() {
		t.Error("BET must move money")
	}
}
