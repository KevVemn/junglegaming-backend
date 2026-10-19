package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/KevVemn/junglegaming-backend/internal/domain"
	"github.com/KevVemn/junglegaming-backend/internal/events"
	"github.com/KevVemn/junglegaming-backend/internal/observability"
	"github.com/KevVemn/junglegaming-backend/internal/postgres"
)

var ErrWalletNotFound = postgres.ErrWalletNotFound

type WalletService struct {
	tx      *postgres.TxManager
	wallets *postgres.WalletRepository
	txs     *postgres.TransactionRepository
	ledger  *postgres.LedgerRepository
	outbox  *postgres.OutboxRepository
	metrics *observability.Metrics
	now     Clock
}

func NewWalletService(
	tx *postgres.TxManager,
	wallets *postgres.WalletRepository,
	txs *postgres.TransactionRepository,
	ledger *postgres.LedgerRepository,
	outbox *postgres.OutboxRepository,
	metrics *observability.Metrics,
) *WalletService {
	return &WalletService{
		tx:      tx,
		wallets: wallets,
		txs:     txs,
		ledger:  ledger,
		outbox:  outbox,
		metrics: metrics,
		now:     time.Now,
	}
}

type OpenWalletCommand struct {
	PlayerID       uuid.UUID
	InitialBalance domain.Money
	CorrelationID  string
}

func (s *WalletService) Open(ctx context.Context, cmd OpenWalletCommand) (*domain.Wallet, error) {
	var wallet *domain.Wallet

	err := s.tx.WithinTx(ctx, func(q postgres.Querier) error {
		now := s.now().UTC()

		created, err := domain.NewWallet(uuid.New(), cmd.PlayerID, cmd.InitialBalance, now)
		if err != nil {
			return err
		}

		if err := s.wallets.Insert(ctx, q, created); err != nil {
			return err
		}

		if cmd.InitialBalance.IsZero() {
			wallet = created
			return nil
		}

		opening, err := domain.NewOpeningTransaction(
			uuid.New(), created.ID(), cmd.PlayerID, cmd.InitialBalance, now)
		if err != nil {
			return err
		}
		if err := s.txs.Insert(ctx, q, opening); err != nil {
			return err
		}

		zero, err := domain.ZeroMoney(cmd.InitialBalance.Currency())
		if err != nil {
			return err
		}

		entry, err := domain.NewWalletLedgerEntry(
			uuid.New(), created.ID(), opening.ID(),
			domain.DirectionCredit, cmd.InitialBalance, zero, cmd.InitialBalance, now)
		if err != nil {
			return err
		}
		if err := s.ledger.Insert(ctx, q, entry); err != nil {
			return err
		}

		if err := opening.MarkProcessed(cmd.InitialBalance, now); err != nil {
			return err
		}
		if err := s.txs.Update(ctx, q, opening); err != nil {
			return err
		}

		processed, err := events.NewWagerTransactionProcessed(
			opening, cmd.InitialBalance, cmd.CorrelationID, now)
		if err != nil {
			return err
		}
		if err := s.enqueue(ctx, q, processed, opening.ID(), now); err != nil {
			return err
		}

		changed, err := events.NewWalletBalanceChanged(entry, created.Version(), cmd.CorrelationID, now)
		if err != nil {
			return err
		}
		if err := s.enqueue(ctx, q, changed, created.ID(), now); err != nil {
			return err
		}

		wallet = created
		return nil
	})
	if err != nil {
		return nil, err
	}

	return wallet, nil
}

func (s *WalletService) GetByID(ctx context.Context, id uuid.UUID) (*domain.Wallet, error) {
	return s.wallets.FindByID(ctx, s.tx.Pool(), id)
}

func (s *WalletService) Ledger(
	ctx context.Context, walletID uuid.UUID, cursor string, limit int,
) (*postgres.LedgerPage, error) {
	if _, err := s.wallets.FindByID(ctx, s.tx.Pool(), walletID); err != nil {
		return nil, err
	}
	return s.ledger.ListByWallet(ctx, s.tx.Pool(), walletID, cursor, limit)
}

type ReconciliationResult struct {
	WalletID          uuid.UUID
	StoredBalance     domain.Money
	CalculatedBalance domain.Money
	Difference        domain.Money
	Consistent        bool
	CheckedEntries    int
}

func (s *WalletService) Reconcile(ctx context.Context, walletID uuid.UUID) (*ReconciliationResult, error) {
	var result *ReconciliationResult

	err := s.tx.WithinTx(ctx, func(q postgres.Querier) error {
		wallet, err := s.wallets.FindByID(ctx, q, walletID)
		if err != nil {
			return err
		}

		sum, count, err := s.wallets.SumLedger(ctx, q, walletID)
		if err != nil {
			return err
		}

		calculated, err := domain.FromMinorUnits(sum, wallet.Currency())
		if err != nil {
			return err
		}

		difference, err := wallet.Balance().Sub(calculated)
		if err != nil {
			return err
		}

		result = &ReconciliationResult{
			WalletID:          walletID,
			StoredBalance:     wallet.Balance(),
			CalculatedBalance: calculated,
			Difference:        difference,
			Consistent:        difference.IsZero(),
			CheckedEntries:    count,
		}
		return nil
	})
	if err != nil {
		s.metrics.ReconciliationChecks.WithLabelValues("error").Inc()
		return nil, err
	}

	if result.Consistent {
		s.metrics.ReconciliationChecks.WithLabelValues("consistent").Inc()
	} else {
		s.metrics.ReconciliationChecks.WithLabelValues("divergent").Inc()
		s.metrics.ReconciliationDiffs.Inc()
	}

	return result, nil
}

func (s *WalletService) enqueue(
	ctx context.Context, q postgres.Querier,
	envelope events.Envelope, aggregateID uuid.UUID, now time.Time,
) error {
	payload, err := envelope.Marshal()
	if err != nil {
		return err
	}

	return s.outbox.Insert(ctx, q, postgres.OutboxRecord{
		EventID:       envelope.EventID,
		AggregateID:   aggregateID,
		AggregateType: envelope.AggregateType,
		EventType:     envelope.EventType,
		Payload:       payload,
		OccurredAt:    now,
	})
}

func IsWalletAlreadyExists(err error) bool {
	return errors.Is(err, postgres.ErrWalletAlreadyExists)
}
