CREATE TABLE wallets (
    id              UUID        PRIMARY KEY,
    player_id       UUID        NOT NULL,
    currency        CHAR(3)     NOT NULL,
    balance_minor   BIGINT      NOT NULL,
    version         BIGINT      NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL,

    CONSTRAINT wallets_player_currency_unique UNIQUE (player_id, currency),
    CONSTRAINT wallets_balance_non_negative   CHECK (balance_minor >= 0),
    CONSTRAINT wallets_version_positive       CHECK (version >= 1),
    CONSTRAINT wallets_currency_format        CHECK (currency ~ '^[A-Z]{3}$')
);

CREATE TABLE wager_transactions (
    id                       UUID        PRIMARY KEY,
    wallet_id                UUID        NOT NULL REFERENCES wallets (id),
    player_id                UUID        NOT NULL,
    kind                     TEXT        NOT NULL,
    amount_minor             BIGINT      NOT NULL,
    currency                 CHAR(3)     NOT NULL,
    state                    TEXT        NOT NULL,

    provider_id              TEXT,
    external_transaction_id  TEXT,
    idempotency_key          TEXT,
    payload_hash             TEXT,
    round_id                 TEXT,
    game_id                  TEXT,

    reference_external_id    TEXT,
    reference_internal_id    UUID REFERENCES wager_transactions (id),

    failure_code             TEXT,
    result_balance_minor     BIGINT,

    reference_attempts       INT         NOT NULL DEFAULT 0,
    reference_next_retry_at  TIMESTAMPTZ,

    created_at               TIMESTAMPTZ NOT NULL,
    updated_at               TIMESTAMPTZ NOT NULL,

    CONSTRAINT wager_transactions_kind_valid
        CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),

    CONSTRAINT wager_transactions_state_valid
        CHECK (state IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),

    CONSTRAINT wager_transactions_amount_non_negative
        CHECK (amount_minor >= 0),

    CONSTRAINT wager_transactions_currency_format
        CHECK (currency ~ '^[A-Z]{3}$'),

    CONSTRAINT wager_transactions_loss_is_zero
        CHECK (kind <> 'LOSS' OR amount_minor = 0),

    CONSTRAINT wager_transactions_monetary_is_positive
        CHECK (kind NOT IN ('OPENING', 'BET', 'WIN', 'REFUND', 'ROLLBACK') OR amount_minor > 0),

    CONSTRAINT wager_transactions_external_metadata
        CHECK (
            (kind = 'OPENING'
                AND provider_id IS NULL
                AND external_transaction_id IS NULL
                AND idempotency_key IS NULL
                AND payload_hash IS NULL
                AND round_id IS NULL
                AND game_id IS NULL
                AND reference_external_id IS NULL)
            OR
            (kind <> 'OPENING'
                AND provider_id IS NOT NULL
                AND external_transaction_id IS NOT NULL
                AND idempotency_key IS NOT NULL
                AND payload_hash IS NOT NULL)
        ),

    CONSTRAINT wager_transactions_reversal_needs_reference
        CHECK (kind NOT IN ('REFUND', 'ROLLBACK') OR reference_external_id IS NOT NULL),

    CONSTRAINT wager_transactions_failure_code_only_on_failure
        CHECK (state IN ('REJECTED', 'FAILED') OR failure_code IS NULL)
);

CREATE UNIQUE INDEX wager_transactions_provider_external_unique
    ON wager_transactions (provider_id, external_transaction_id)
    WHERE provider_id IS NOT NULL;

CREATE UNIQUE INDEX wager_transactions_idempotency_key_unique
    ON wager_transactions (idempotency_key)
    WHERE idempotency_key IS NOT NULL;

CREATE UNIQUE INDEX wager_transactions_single_opening_per_wallet
    ON wager_transactions (wallet_id)
    WHERE kind = 'OPENING';

CREATE UNIQUE INDEX wager_transactions_single_successful_reversal
    ON wager_transactions (reference_internal_id, kind)
    WHERE state = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK');

CREATE INDEX wager_transactions_wallet_created
    ON wager_transactions (wallet_id, created_at DESC);

CREATE INDEX wager_transactions_pending_reference_retry
    ON wager_transactions (reference_next_retry_at)
    WHERE state = 'PENDING_REFERENCE';

CREATE INDEX wager_transactions_pending_resume
    ON wager_transactions (created_at)
    WHERE state = 'PENDING';

CREATE TABLE wallet_ledger_entries (
    id                   UUID        PRIMARY KEY,
    wallet_id            UUID        NOT NULL REFERENCES wallets (id),
    transaction_id       UUID        NOT NULL REFERENCES wager_transactions (id),
    direction            TEXT        NOT NULL,
    amount_minor         BIGINT      NOT NULL,
    currency             CHAR(3)     NOT NULL,
    balance_before_minor BIGINT      NOT NULL,
    balance_after_minor  BIGINT      NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL,

    CONSTRAINT ledger_wallet_transaction_unique UNIQUE (wallet_id, transaction_id),

    CONSTRAINT ledger_direction_valid
        CHECK (direction IN ('DEBIT', 'CREDIT')),

    CONSTRAINT ledger_amount_positive
        CHECK (amount_minor > 0),

    CONSTRAINT ledger_balances_non_negative
        CHECK (balance_before_minor >= 0 AND balance_after_minor >= 0),

    CONSTRAINT ledger_balance_math
        CHECK (
            (direction = 'CREDIT' AND balance_after_minor = balance_before_minor + amount_minor)
            OR
            (direction = 'DEBIT'  AND balance_after_minor = balance_before_minor - amount_minor)
        )
);

CREATE INDEX ledger_wallet_cursor
    ON wallet_ledger_entries (wallet_id, created_at, id);

CREATE OR REPLACE FUNCTION reject_ledger_mutation()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'wallet_ledger_entries is append-only: % is not allowed', TG_OP
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER ledger_entries_immutable
    BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION reject_ledger_mutation();

CREATE TABLE inbox_messages (
    consumer_name TEXT        NOT NULL,
    message_id    TEXT        NOT NULL,
    message_hash  TEXT        NOT NULL,
    received_at   TIMESTAMPTZ NOT NULL,
    completed_at  TIMESTAMPTZ,

    CONSTRAINT inbox_messages_pk PRIMARY KEY (consumer_name, message_id)
);

CREATE INDEX inbox_messages_incomplete
    ON inbox_messages (received_at)
    WHERE completed_at IS NULL;

CREATE TABLE outbox_events (
    event_id       UUID        PRIMARY KEY,
    aggregate_id   UUID        NOT NULL,
    aggregate_type TEXT        NOT NULL,
    event_type     TEXT        NOT NULL,
    payload        JSONB       NOT NULL,
    occurred_at    TIMESTAMPTZ NOT NULL,
    attempts       INT         NOT NULL DEFAULT 0,
    next_retry_at  TIMESTAMPTZ NOT NULL,
    locked_until   TIMESTAMPTZ,
    published_at   TIMESTAMPTZ,
    last_error     TEXT,
    created_at     TIMESTAMPTZ NOT NULL,

    CONSTRAINT outbox_attempts_non_negative CHECK (attempts >= 0)
);

CREATE INDEX outbox_events_pending
    ON outbox_events (next_retry_at, occurred_at)
    WHERE published_at IS NULL;

CREATE INDEX outbox_events_aggregate
    ON outbox_events (aggregate_id, occurred_at);
