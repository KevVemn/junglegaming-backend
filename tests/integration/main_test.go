//go:build integration

package integration

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/localstack"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/KevVemn/junglegaming-backend/internal/config"
	"github.com/KevVemn/junglegaming-backend/internal/messaging"
	"github.com/KevVemn/junglegaming-backend/internal/observability"
	"github.com/KevVemn/junglegaming-backend/internal/postgres"
	"github.com/KevVemn/junglegaming-backend/internal/service"
	"github.com/KevVemn/junglegaming-backend/migrations"
)

type Env struct {
	Pool          *pgxpool.Pool
	TxManager     *postgres.TxManager
	Wallets       *postgres.WalletRepository
	Transactions  *postgres.TransactionRepository
	Ledger        *postgres.LedgerRepository
	Inbox         *postgres.InboxRepository
	Outbox        *postgres.OutboxRepository
	WalletSvc     *service.WalletService
	WageringSvc   *service.WageringService
	SQSClient     *messaging.Client
	Metrics       *observability.Metrics
	SQSConfig     config.SQSConfig
	DatabaseURL   string
	Logger        *slog.Logger
	localstackURL string
}

var sharedEnv *Env

func TestMain(m *testing.M) {
	ctx := context.Background()

	env, cleanup, err := setupEnv(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration setup failed: %v\n", err)
		os.Exit(1)
	}

	sharedEnv = env
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func setupEnv(ctx context.Context) (*Env, func(), error) {
	var terminators []func()
	cleanup := func() {
		for i := len(terminators) - 1; i >= 0; i-- {
			terminators[i]()
		}
	}

	pgContainer, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("junglegaming"),
		tcpostgres.WithUsername("junglegaming"),
		tcpostgres.WithPassword("junglegaming"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(90*time.Second),
		),
	)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("starting postgres: %w", err)
	}
	terminators = append(terminators, func() {
		_ = testcontainers.TerminateContainer(pgContainer)
	})

	dsn, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("building dsn: %w", err)
	}

	lsContainer, err := localstack.Run(ctx, "localstack/localstack:3.8")
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("starting localstack: %w", err)
	}
	terminators = append(terminators, func() {
		_ = testcontainers.TerminateContainer(lsContainer)
	})

	lsHost, err := lsContainer.Host(ctx)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	lsPort, err := lsContainer.MappedPort(ctx, "4566/tcp")
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	lsURL := fmt.Sprintf("http://%s:%s", lsHost, lsPort.Port())

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("creating pool: %w", err)
	}
	terminators = append(terminators, pool.Close)

	migrator, err := postgres.NewMigrator(pool, migrations.FS)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	if _, err := migrator.Up(ctx); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("applying migrations: %w", err)
	}

	sqsCfg := config.SQSConfig{
		Region:            "us-east-1",
		EndpointURL:       lsURL,
		ConsumerName:      "integration-consumer",
		MaxMessages:       10,
		WaitTimeSeconds:   1,
		VisibilityTimeout: 5,
		WorkerCount:       2,
	}

	client, err := messaging.NewClient(ctx, sqsCfg)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("creating sqs client: %w", err)
	}

	queues, err := createQueues(ctx, client)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("creating queues: %w", err)
	}
	sqsCfg.QueueURL = queues["wager-transactions.fifo"]
	sqsCfg.DLQURL = queues["wager-transactions-dlq.fifo"]
	sqsCfg.EventsQueueURL = queues["wager-events.fifo"]

	client, err = messaging.NewClient(ctx, sqsCfg)
	if err != nil {
		cleanup()
		return nil, nil, err
	}

	txManager := postgres.NewTxManager(pool)
	wallets := postgres.NewWalletRepository()
	transactions := postgres.NewTransactionRepository()
	ledger := postgres.NewLedgerRepository()
	inbox := postgres.NewInboxRepository()
	outbox := postgres.NewOutboxRepository()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	metrics := observability.NewMetrics()

	return &Env{
		Pool:          pool,
		TxManager:     txManager,
		Wallets:       wallets,
		Transactions:  transactions,
		Ledger:        ledger,
		Inbox:         inbox,
		Outbox:        outbox,
		WalletSvc:     service.NewWalletService(txManager, wallets, transactions, ledger, outbox, metrics),
		WageringSvc:   service.NewWageringService(txManager, wallets, transactions, ledger, outbox, inbox, metrics),
		Metrics:       metrics,
		SQSClient:     client,
		SQSConfig:     sqsCfg,
		DatabaseURL:   dsn,
		Logger:        logger,
		localstackURL: lsURL,
	}, cleanup, nil
}
