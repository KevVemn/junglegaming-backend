# JungleGaming — Distributed Wager Processing

A Go service that processes financial operations from game providers over HTTP and SQS,
keeping balances correct across multiple instances and failures between processing steps.

- Money never touches a floating point type, at any stage.
- Idempotency is persistent and survives a restart of every process.
- Financial invariants are enforced by the database, not only by the application.
- Events reach the outside world only after the transaction that produced them commits.
- The ledger is append-only: corrections require new entries.

---

## Table of contents

- [Stack](#stack)
- [Requirements](#requirements)
- [Quick start](#quick-start)
- [Environment variables](#environment-variables)
- [Authentication](#authentication)
- [HTTP API](#http-api)
- [SQS](#sqs)
- [Migrations](#migrations)
- [Tests](#tests)
- [Observability](#observability)
- [Project layout](#project-layout)
- [Troubleshooting](#troubleshooting)

---

## Stack

| Concern | Choice |
|---|---|
| Language | Go 1.27 (declared in `go.mod` and the `Dockerfile`) |
| Composition | Uber Fx, with `fx.Module` / `fx.Provide` / `fx.Invoke` and `fx.Lifecycle` |
| HTTP | `net/http` with `ServeMux` path patterns (no external router) |
| Database | PostgreSQL 16, accessed with `pgx/v5` and explicit SQL |
| Messaging | AWS SQS FIFO, running locally on LocalStack |
| Identity | Keycloak 26 (OAuth 2.0 / OIDC, `client_credentials`) |
| Metrics | Prometheus (`prometheus/client_golang`) |
| Local environment | Docker Compose |

`ARCHITECTURE.md` explains why each of these was chosen and how the money,
concurrency and idempotency guarantees are implemented.

---

## Requirements

- **Docker** with Compose v2 (everything runs in containers)
- **Go 1.27+** — only needed to run the test suites outside Docker
- **make** — optional

Nothing else has to be installed: PostgreSQL, Keycloak and LocalStack all come up
through Compose.

### Without make

Every target is a thin wrapper. If `make` is not available, use the command directly:

| Target | Equivalent command |
|---|---|
| `make up` | `docker compose up --build -d` |
| `make down` | `docker compose down` |
| `make clean` | `docker compose down -v` |
| `make logs` | `docker compose logs -f api` |
| `make ps` | `docker compose ps` |
| `make migrate-up` | `docker compose run --rm migrate /app/bin/migrate up` |
| `make migrate-down` | `docker compose run --rm migrate /app/bin/migrate down` |
| `make migrate-status` | `docker compose run --rm migrate /app/bin/migrate status` |
| `make test` | `go test ./...` |
| `make test-race` | `docker run --rm -v "$PWD:/app" -w /app golang:1.27 go test -race ./...` |
| `make test-integration` | `TESTCONTAINERS_RYUK_DISABLED=true go test -tags integration ./tests/integration/ -v -timeout 600s` |
| `make vet` | `go vet ./...` and `go vet -tags integration ./tests/...` |
| `make fmt` | `go fmt ./...` |
| `make check` | `go vet ./... && go test ./... && gofmt -l .` |
| `make build` | `go build -o bin/api ./cmd/api` and `go build -o bin/migrate ./cmd/migrate` |

The token helper, spelled out:

```bash
curl -s -X POST http://localhost:8081/realms/junglegaming/protocol/openid-connect/token \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  -d 'grant_type=client_credentials&client_id=provider-a&client_secret=provider-a-secret' \
  | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p'
```

---

## Quick start

```bash
cp .env.example .env
make up
```

Or without make:

```bash
cp .env.example .env
docker compose up --build -d
```

Compose starts the services in order, waits for each health check, applies the
migrations and only then starts the API:

| Service | Address | Notes |
|---|---|---|
| API | http://localhost:8080 | |
| Keycloak | http://localhost:8081 | admin console: `admin` / `admin` |
| LocalStack | http://localhost:4566 | SQS |
| PostgreSQL | localhost:5432 | `junglegaming` / `junglegaming` |

Confirm it is alive:

```bash
curl http://localhost:8080/health/ready
```

```json
{"status":"ok","checks":{"postgres":"up","sqs":"up"}}
```

Tear everything down:

```bash
make down          # stops the services, keeps the data
make clean         # stops and drops the volumes
```

---

## Environment variables

All of them have defaults that work with the bundled Compose file.
`.env.example` carries local values only — no real secrets.

### Server

| Variable | Default | Description |
|---|---|---|
| `SERVER_PORT` | `8080` | HTTP listen port |
| `SERVER_SHUTDOWN_TIMEOUT` | `30s` | Grace period for in-flight work on shutdown |
| `SERVER_READ_TIMEOUT` | `15s` | |
| `SERVER_WRITE_TIMEOUT` | `15s` | |

### Database

| Variable | Default | Description |
|---|---|---|
| `DB_HOST` | `localhost` | |
| `DB_PORT` | `5432` | |
| `DB_USER` | `junglegaming` | |
| `DB_PASSWORD` | `junglegaming` | |
| `DB_NAME` | `junglegaming` | |
| `DB_SSLMODE` | `disable` | |
| `DB_MAX_CONNS` | `20` | Pool ceiling |
| `DB_MIN_CONNS` | `2` | Pool floor |

### Authentication

| Variable | Default | Description |
|---|---|---|
| `AUTH_ENABLED` | `true` | Set to `false` only for local experiments |
| `OIDC_ISSUER_URL` | `http://localhost:8081/realms/junglegaming` | Issuer **accepted in the token** |
| `OIDC_DISCOVERY_URL` | `http://keycloak:8080/realms/junglegaming` | Where the keys are **fetched from** |
| `OIDC_AUDIENCE` | `wagering-api` | Required `aud` claim |
| `OIDC_PROVIDER_CLAIM` | `provider_id` | Claim that identifies an external provider |
| `OIDC_INTERNAL_CLAIM` | `scope_internal` | Claim that identifies the internal service |

> `OIDC_ISSUER_URL` and `OIDC_DISCOVERY_URL` are separate on purpose. Tokens are
> issued with the public hostname (`localhost:8081`) while the API reaches Keycloak
> over the Docker network (`keycloak:8080`). Keeping them apart lets the issuer be
> validated strictly instead of being skipped. See `ARCHITECTURE.md`.

### SQS

| Variable | Default | Description |
|---|---|---|
| `AWS_REGION` | `us-east-1` | |
| `AWS_ENDPOINT_URL` | `http://localhost:4566` | LocalStack endpoint |
| `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` | `test` | LocalStack credentials |
| `SQS_QUEUE_URL` | — | Inbound transactions queue |
| `SQS_DLQ_URL` | — | Dead letter queue |
| `SQS_EVENTS_QUEUE_URL` | — | Outbound events queue |
| `SQS_CONSUMER_NAME` | `wager-transactions-consumer` | Inbox consumer identity |
| `SQS_VISIBILITY_TIMEOUT` | `30` | Seconds a message stays invisible while being handled |
| `SQS_MAX_RECEIVE_COUNT` | `5` | Deliveries before the redrive policy moves it to the DLQ |
| `SQS_WORKER_COUNT` | `4` | Concurrent consumer workers |
| `SQS_SHUTDOWN_GRACE` | `20s` | Time to finish in-flight messages on SIGTERM |

### Outbox and pending references

| Variable | Default | Description |
|---|---|---|
| `OUTBOX_BATCH_SIZE` | `50` | Events claimed per cycle |
| `OUTBOX_POLL_INTERVAL` | `1s` | |
| `OUTBOX_LOCK_DURATION` | `30s` | After this, an abandoned event is picked up again |
| `OUTBOX_MAX_ATTEMPTS` | `10` | |
| `OUTBOX_BASE_BACKOFF` | `1s` | Base of the exponential backoff |
| `REFERENCE_TTL` | `24h` | How long a reversal waits for its reference |
| `REFERENCE_MAX_ATTEMPTS` | `10` | |
| `REFERENCE_POLL_INTERVAL` | `2s` | |

### Logging

| Variable | Default | Description |
|---|---|---|
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `LOG_FORMAT` | `json` | `json` or `text` |

---

## Authentication

Every business endpoint requires a bearer token. `/health/*` and `/metrics` are public
so that probes and Prometheus can reach them.

The realm is imported automatically from `deploy/keycloak/realm-export.json` and ships
three identities:

| Client | Secret | Identity in the token | May do |
|---|---|---|---|
| `provider-a` | `provider-a-secret` | `provider_id=provider-a` | Submit and read **its own** transactions |
| `provider-b` | `provider-b-secret` | `provider_id=provider-b` | Same, isolated from `provider-a` |
| `wallet-admin` | `wallet-admin-secret` | `scope_internal` | Open wallets, run reconciliation |

### Getting a token

```bash
curl -s -X POST \
  http://localhost:8081/realms/junglegaming/protocol/openid-connect/token \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  -d 'grant_type=client_credentials&client_id=provider-a&client_secret=provider-a-secret'
```

Or, for a bare token ready to paste into a variable:

```bash
make token
```

```bash
TOKEN=$(make token)
```

### Authorization rules

- The `providerId` in the request body must match the authenticated identity, otherwise
  the request is rejected with `403`. The body never decides who the caller is.
- A provider can only read its own transactions, including on idempotent replays.
- Opening a wallet and running reconciliation are restricted to the internal identity.

---

## HTTP API

### Open a wallet

```bash
curl -X POST http://localhost:8080/wallets \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "initialBalance": { "amount": "1000.00", "currency": "BRL" }
  }'
```

```json
{
  "id": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "balance": { "amount": "1000.00", "currency": "BRL" },
  "version": 1
}
```

A positive opening balance creates an `OPENING` transaction in `PROCESSED`, its credit
ledger entry and the `WagerTransactionProcessed` and `WalletBalanceChanged` outbox
records, all in the same commit. A zero balance creates none of them.

### Submit a wager operation

```bash
curl -X POST http://localhost:8080/wagering/transactions \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:transaction-123' \
  -d '{
    "providerId": "provider-a",
    "externalTransactionId": "transaction-123",
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
    "roundId": "round-987",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": { "amount": "25.00", "currency": "BRL" }
  }'
```

```json
{
  "transactionId": "0192f298-345e-7e38-af88-e43f851a819d",
  "status": "PROCESSED",
  "balance": { "amount": "975.00", "currency": "BRL" },
  "idempotentReplay": false
}
```

`Idempotency-Key` is required. Replaying the same key with the same business payload
returns the stored result with `idempotentReplay: true` — including the balance observed
at the original processing, even if the wallet moved since. Reusing the key with a
different payload returns `409`.

Reversals add `referenceExternalTransactionId` to the body:

```bash
curl -X POST http://localhost:8080/wagering/transactions \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: provider-a:refund-1' \
  -d '{
    "providerId": "provider-a",
    "externalTransactionId": "refund-1",
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
    "roundId": "round-987",
    "gameId": "fortune-chimp",
    "kind": "REFUND",
    "money": { "amount": "25.00", "currency": "BRL" },
    "referenceExternalTransactionId": "transaction-123"
  }'
```

### Operation types

| Kind | Movement | Rule |
|---|---|---|
| `BET` | debit | Positive amount, sufficient balance |
| `WIN` | credit | Positive amount |
| `LOSS` | none | Requires `"0.00"`; creates no ledger entry and does not bump the wallet version |
| `REFUND` | credit | Returns the full amount of a processed `BET` |
| `ROLLBACK` | opposite of the original | Undoes a processed `BET`, `WIN` or `REFUND` |
| `OPENING` | credit | Internal only — rejected if it arrives over HTTP or SQS |

### Reads

```bash
curl http://localhost:8080/wallets/{walletId}                -H "Authorization: Bearer $TOKEN"
curl http://localhost:8080/wallets/{walletId}/ledger?limit=50 -H "Authorization: Bearer $TOKEN"
curl http://localhost:8080/wagering/transactions/{transactionId} -H "Authorization: Bearer $TOKEN"
curl http://localhost:8080/providers/{providerId}/wagering/transactions/{externalTransactionId} \
  -H "Authorization: Bearer $TOKEN"
```

The ledger is paginated with an opaque cursor and a stable ordering:

```json
{
  "entries": [ ... ],
  "nextCursor": "MjAyNi0xMC0xOFQw..."
}
```

Pass it back as `?cursor=...` to get the next page.

### Reconciliation

```bash
curl -X POST http://localhost:8080/wallets/{walletId}/reconciliation \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

```json
{
  "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
  "storedBalance": { "amount": "975.00", "currency": "BRL" },
  "calculatedBalance": { "amount": "975.00", "currency": "BRL" },
  "difference": { "amount": "0.00", "currency": "BRL" },
  "consistent": true,
  "checkedEntries": 2
}
```

Rebuilds the balance from the ledger and compares it with the stored one.
It never writes to the wallet. Divergences are reported in the response, in the logs
and through the `wagering_reconciliation_divergences_total` metric.

### Health

```bash
curl http://localhost:8080/health/live     # process liveness
curl http://localhost:8080/health/ready    # PostgreSQL and SQS readiness
```

### Response codes

| Status | Meaning |
|---|---|
| `200` | Read succeeded, or idempotent replay of a completed operation |
| `201` | Wallet opened, or operation processed |
| `202` | Accepted and waiting — currently `PENDING_REFERENCE` |
| `400` | Malformed body, invalid money format, missing `Idempotency-Key` |
| `401` | Missing, invalid or expired token |
| `403` | Identity may not perform or read this operation |
| `404` | Wallet or transaction does not exist |
| `409` | Wallet already exists, idempotency key reused with a different payload, or a concurrency conflict that was not resolved by retrying |
| `422` | Business rejection — the response carries a `failureCode` |
| `503` | A dependency is unavailable (readiness only) |

Error bodies are uniform:

```json
{
  "code": "IDEMPOTENCY_CONFLICT",
  "message": "idempotency key was reused with a different payload",
  "correlationId": "41ffc426-1a54-49ac-817e-45ee6c9014b0"
}
```

### Failure codes

| Code | Meaning |
|---|---|
| `INSUFFICIENT_FUNDS` | The wallet does not cover the bet |
| `REVERSAL_EXCEEDS_BALANCE` | The reversal would need more than the current balance |
| `REFERENCE_NOT_FOUND` | The referenced transaction never arrived within the TTL |
| `REFERENCE_NOT_USABLE` | The reference exists but did not finish successfully |
| `REFERENCE_MISMATCH` | Provider, player, wallet, currency, round or amount disagree |
| `ALREADY_REVERSED` | The reference already has a successful reversal of this kind |
| `CURRENCY_MISMATCH` | Operation currency differs from the wallet currency |
| `INVALID_AMOUNT` | Amount is not valid for this operation kind |

---

## SQS

Two FIFO queues are provisioned by `deploy/localstack/init-sqs.sh` when LocalStack
starts, plus a third one for outbound events:

| Queue | Purpose |
|---|---|
| `wager-transactions.fifo` | Inbound operations |
| `wager-transactions-dlq.fifo` | Dead letter target, `maxReceiveCount: 5` |
| `wager-events.fifo` | Outbound events published from the outbox |

### Message shape

```json
{
  "messageId": "msg-123",
  "type": "WagerTransactionRequested",
  "occurredAt": "2026-10-18T12:00:00.000Z",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": "transaction-123",
    "idempotencyKey": "provider-a:transaction-123",
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
    "roundId": "round-987",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": { "amount": "25.00", "currency": "BRL" }
  }
}
```

`data.idempotencyKey` carries the same meaning as the HTTP header, and both paths share
the same use case and the same payload hash. `messageId` is the durable identity used by
the inbox.

### Sending a message by hand

```bash
docker exec jg-localstack awslocal sqs send-message \
  --region us-east-1 \
  --queue-url http://localhost:4566/000000000000/wager-transactions.fifo \
  --message-body "$(cat message.json)" \
  --message-group-id "<walletId>" \
  --message-deduplication-id "<unique-id>"
```

`MessageGroupId` is the wallet id, so operations on the same wallet keep their order while
different wallets are processed in parallel. `MessageDeduplicationId` is the producer's
responsibility; the service does not rely on it for correctness, since the inbox already
deduplicates by `messageId`.

### Delivery semantics

- A message is deleted from the queue **only after** its handling commits.
- Business rejections are terminal, so the message is deleted.
- Transient failures are not deleted — SQS redelivers them, and the inbox prevents
  double processing.
- Malformed messages and permanent errors are sent to the DLQ immediately.
- Exhausted deliveries reach the DLQ through the redrive policy.
- On `SIGTERM` the consumer stops fetching and finishes in-flight work within
  `SQS_SHUTDOWN_GRACE`; whatever does not finish is released for safe redelivery.

---

## Migrations

Migrations live in `migrations/` as versioned `*.up.sql` / `*.down.sql` pairs and are
embedded into the binary, so the migrator needs no files at runtime.

```bash
make migrate-up        # apply pending migrations
make migrate-down      # revert the last one
make migrate-status    # show what is applied
```

Without make:

```bash
docker compose run --rm migrate /app/bin/migrate up
docker compose run --rm migrate /app/bin/migrate down
docker compose run --rm migrate /app/bin/migrate status
```

Applied versions are tracked in `schema_migrations`. Each migration runs inside its own
transaction, so a failure leaves nothing half applied.

---

## Tests

```bash
make test              # unit tests
make test-race         # unit tests with the race detector
make test-integration  # integration tests against real containers
make test-all          # all of the above
make check             # gofmt check + go vet + unit tests
```

Plain commands:

```bash
go test ./...
go vet ./...
gofmt -l .
```

### Race detector

`-race` requires cgo and a C toolchain, which is not available on a plain Windows
install. The Make target runs it inside a Linux container so it works everywhere:

```bash
docker run --rm -v "$PWD:/app" -w /app golang:1.27 go test -race ./...
```

### Integration tests

They are behind the `integration` build tag and start **real** containers through
testcontainers — PostgreSQL, LocalStack and Keycloak. No infrastructure is mocked.

```bash
TESTCONTAINERS_RYUK_DISABLED=true go test -tags integration ./tests/integration/ -v -timeout 600s
```

Docker must be running. The whole suite takes roughly 80 seconds; the Keycloak container
starts lazily, so running only the concurrency tests does not pay for it.

What they cover:

| Area | Scenarios |
|---|---|
| Concurrency | The same bet sent 50 times in parallel debits once; two 80.00 bets against a 100.00 balance leave 20.00 with a single debit; replays after the contest do not change the outcome; independent wallets proceed in parallel |
| Messaging | Consumer processes a message; a duplicate is ignored by the inbox; malformed messages reach the DLQ; the same operation over HTTP and SQS debits once; the outbox is drained; two publishers compete without duplicating |
| Persistence | The ledger rejects `UPDATE` and `DELETE`; negative balances are rejected; inconsistent ledger arithmetic is rejected; only one `OPENING` per wallet; duplicate wallets are rejected |
| Reversals | `REFUND` credits the bet back; a second refund is rejected with `ALREADY_REVERSED`; a reversal that arrives before its reference stays pending and resolves later; it expires after the configured attempts; `LOSS` moves no money |
| Authentication | Keycloak issues tokens with the expected claims; invalid, malformed and tampered tokens are rejected; a wrong secret gets no token; providers are isolated from each other |
| Metrics | Counters move for processed, rejected, duplicated and reconciled operations; every expected series is exposed |

### Running multiple instances

Independent processes, each with its own connections, can be started by scaling the API
service:

```bash
docker compose up -d --scale api=3
```

Correctness does not depend on a single instance: the wallet version check, the
`(wallet_id, transaction_id)` ledger constraint, the inbox primary key and the outbox
`SKIP LOCKED` claims all live in the database.

---

## Observability

### Logs

Structured JSON on stdout, carrying whichever identifiers are available for the
operation: `correlationId`, `messageId`, `transactionId`, `walletId`, `providerId`.
Credentials and full financial payloads are never logged.

```json
{"time":"2026-10-18T13:25:02Z","level":"INFO","msg":"message processed","messageId":"msg-123","providerId":"provider-a","walletId":"7c4d...","transactionId":"7ea2...","status":"PROCESSED"}
```

A `X-Correlation-Id` request header is honoured and echoed back; one is generated when
absent.

### Metrics

```bash
curl http://localhost:8080/metrics
```

| Metric | Type | What it answers |
|---|---|---|
| `wagering_transactions_total` | counter | Outcomes by kind, status and source |
| `wagering_transaction_duration_seconds` | histogram | Processing latency (p50/p95/p99) |
| `wagering_duplicates_total` | counter | Duplicates, by what caught them |
| `wagering_retries_total` | counter | Retries by component |
| `wagering_concurrency_conflicts_total` | counter | Lost update attempts prevented |
| `wagering_dlq_messages_total` | counter | Messages routed to the DLQ, by reason |
| `wagering_messages_in_flight` | gauge | Messages being handled right now |
| `wagering_outbox_pending_events` | gauge | Events committed but not published |
| `wagering_outbox_lag_seconds` | gauge | Age of the oldest unpublished event |
| `wagering_outbox_published_total` | counter | Publish attempts by result |
| `wagering_reconciliation_checks_total` | counter | Reconciliation runs by result |
| `wagering_reconciliation_divergences_total` | counter | Stored balance differing from the ledger |
| `wagering_pending_reference_total` | counter | Pending reference outcomes |

All labelled series are pre-initialised at zero on startup, so dashboards and alerts
see them before the first event ever happens.

### Health checks

| Endpoint | Checks |
|---|---|
| `GET /health/live` | The process is up |
| `GET /health/ready` | PostgreSQL and SQS are reachable |

---

## Project layout

```
cmd/
  api/              HTTP server, workers and the Fx composition root
  migrate/          migration CLI
internal/
  domain/           Money, Wallet, WagerTransaction, WalletLedgerEntry
  postgres/         pgx repositories, transaction manager, migrator
  service/          use cases: wagering, wallet, pending references
  httpapi/          handlers, DTOs, middlewares, health
  messaging/        SQS client, consumer, outbox publisher, reference worker
  auth/             OIDC verification and identity resolution
  events/           event envelope and the four published event types
  idempotency/      canonical payload hashing
  config/           environment configuration and validation
  observability/    structured logging and Prometheus metrics
migrations/         versioned SQL, embedded into the binary
deploy/             Keycloak realm and LocalStack queue provisioning
tests/integration/  integration suites, behind the `integration` build tag
```

`internal/domain` imports nothing but the standard library and a UUID package. It knows
nothing about Fx, HTTP, SQS or pgx.

---

## Troubleshooting

**Keycloak takes a while on the first start.**
It creates its schema before serving. `make up` waits for the health check; give it up to
a minute.

**`401` with a token that looks valid.**
The issuer in the token must match `OIDC_ISSUER_URL` exactly. If you request tokens from
`localhost:8081`, that is the issuer the API has to accept, while `OIDC_DISCOVERY_URL`
points at the address the API can actually reach.

**`403` when opening a wallet.**
Wallet operations require the internal identity. Use `wallet-admin`, not `provider-a`.

**`go test -race` fails on Windows.**
It needs cgo. Use `make test-race`, which runs it inside a Linux container.

**Integration tests cannot reach Docker.**
Make sure Docker is running and try `export TESTCONTAINERS_RYUK_DISABLED=true`.

**Ports already in use.**
Override `API_PORT`, `KEYCLOAK_PORT`, `DB_PORT` or `LOCALSTACK_PORT` in `.env`.

---

## Further reading

`ARCHITECTURE.md` documents the decisions behind this implementation: the money
representation and its limits, the transaction boundaries, how the idempotency hash is
computed, the concurrency strategy, how pending references and reversals behave, the
inbox and outbox design, the authentication and authorization model, how Fx composes and
shuts everything down, and what was intentionally left out.
