# Architecture

This document records the decisions behind the implementation, why each one was taken,
and what was deliberately left out. It is meant to be read next to the code, not instead
of it.

- [Guiding principles](#guiding-principles)
- [Money](#money)
- [Transaction boundaries](#transaction-boundaries)
- [Idempotency](#idempotency)
- [Concurrency](#concurrency)
- [Reversals](#reversals)
- [Pending references](#pending-references)
- [Inbox](#inbox)
- [Outbox](#outbox)
- [Database invariants](#database-invariants)
- [Authentication and authorization](#authentication-and-authorization)
- [Composition with Fx and shutdown](#composition-with-fx-and-shutdown)
- [Observability](#observability)
- [Interpretations](#interpretations)
- [Limitations and unfinished work](#limitations-and-unfinished-work)

---

## Guiding principles

Three ideas drove most of the decisions.

**The database is the last line of defence.** Application code can be bypassed, replaced
or simply wrong. A `CHECK` constraint cannot. Every financial invariant that can be
expressed in the schema is expressed there, so that even a direct `psql` session cannot
leave the data inconsistent.

**The domain owes nothing to infrastructure.** `internal/domain` imports the standard
library and a UUID package, nothing else. It does not know that Fx, HTTP, SQS or pgx
exist. This is what makes the domain testable without any container and what keeps the
financial rules readable.

**Correctness must not depend on a single process.** Every guarantee — idempotency,
ordering per wallet, event publication, pending reference resolution — is anchored in the
database, so a second instance can take over at any point.

---

## Money

### Representation

`Money` is an immutable value object holding an `int64` of **minor units** and an ISO 4217
currency code. `2500` with `BRL` means R$ 25.00.

```go
type Money struct {
	minorUnits int64
	currency   string
}
```

Floating point does not appear anywhere: not in parsing, not in arithmetic, not in
serialization, not in persistence. The reason is concrete rather than theoretical:

```
strconv.ParseFloat("0.29") * 100  →  28.999999999999996
int64(28.999999999999996)         →  28
```

One cent disappears. Repeat that across millions of operations and the ledger stops
matching the balance.

### Parsing

`ParseMoney` splits the decimal string at the dot and converts each side with
`strconv.ParseInt`. `strconv.ParseFloat` is never called, which also means `"1e2"`,
`"NaN"` and `"Infinity"` are rejected instead of silently accepted.

Accepted: `"0"`, `"25"`, `"25.5"`, `"25.50"`, `"025.00"`.
Rejected: `""`, `" 25.00"`, `"25."`, `".50"`, `"25.001"`, `"1e2"`, `"NaN"`, `"25,00"`,
`"+25.00"`, `"-5.00"`.

Scale is fixed at two decimal places and **never rounded**. `"25.001"` is an error, not
`25.00`, because silently rounding an invalid input is how money quietly goes missing.

`"25.5"` is normalised to 2550 minor units: the fractional part is padded on the right,
so a single decimal digit means tens of cents, not units.

### Limits and overflow

`int64` covers roughly ±92 quadrillion minor units, which is far beyond any realistic
balance. Still, overflow is handled explicitly at every point, because in Go an integer
overflow does not raise — it silently wraps and flips the sign.

The scaling check is the subtle one:

```go
if units > (math.MaxInt64-frac)/minorUnitsPerUnit {
	return 0, ErrMoneyOverflow
}
```

It is written as a division on purpose. Testing `units*100 + frac > MaxInt64` would
already have overflowed while computing the condition — the check would be inspecting a
corrupted value. Dividing cannot overflow, so the test is safe.

`Add` and `Sub` detect wrap-around by sign: if two operands of the same sign produce a
result of a different sign, it wrapped. `Neg` guards `math.MinInt64`, which has no
positive counterpart in two's complement.

### Two constructors, on purpose

| Constructor | Accepts negatives | Used by |
|---|---|---|
| `ParseMoney` | no | external input (HTTP, SQS) |
| `FromMinorUnits` | yes | rehydration from the database, internal differences |

The external contract never sends negative amounts, so `ParseMoney` rejects them. But a
reconciliation difference can legitimately be negative, and a value read back from
`BIGINT` must round-trip exactly — hence the second constructor.

### Immutability

Every method uses a value receiver, so the language itself guarantees that no operation
can mutate an existing `Money`. `a.Add(b)` returns a new value and leaves both operands
untouched. The only exception is `UnmarshalJSON`, which needs a pointer receiver because
that is how `encoding/json` fills a value.

### Persistence and transport

Stored as `BIGINT` plus a `CHAR(3)` currency column — exact, with no rounding on the way
in or out. Transported as `{"amount":"25.00","currency":"BRL"}`: a **string**, so that no
consumer is forced to parse it into a float.

---

## Transaction boundaries

Repositories do not open transactions. They receive a `Querier`:

```go
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
```

Both `*pgxpool.Pool` and `pgx.Tx` satisfy it. The caller decides the boundary, and the
boundary is visible in the code:

```go
err := s.tx.WithinTx(ctx, func(q postgres.Querier) error {
	// everything in here commits or rolls back together
})
```

This keeps the services honest about what is atomic. Reads that do not need a transaction
pass the pool directly; writes that must be atomic go through `WithinTx`.

The isolation level is **Read Committed**, PostgreSQL's default. Serializable was not
necessary: the only contended write is the wallet balance, and that is protected by the
version check described below, which is effective at Read Committed and does not force
the caller to handle serialization failures.

### What commits together

A wager operation is a single transaction containing:

1. the inbox record, when the operation arrived over SQS
2. the `wager_transactions` row
3. the ledger entry, when money moves
4. the wallet balance and version update
5. the outbox events
6. the inbox completion mark

If any step fails, none of it happened. This is what makes *"events published only after
the transaction that originated them commits"* true by construction rather than by
convention.

---

## Idempotency

### The key and the hash

The client sends `Idempotency-Key` (HTTP) or `data.idempotencyKey` (SQS). The server
stores it verbatim — it never substitutes a key it computed itself, even though the
suggested format is `{providerId}:{externalTransactionId}`.

Alongside the key, a SHA-256 hash of the **business fields** is persisted:

```
providerId · externalTransactionId · playerId · walletId
roundId · gameId · kind · amount · currency · referenceExternalTransactionId
```

The hash deliberately excludes the idempotency key itself and every transport detail
(headers, `messageId`, `occurredAt`), so that the same operation produces the same hash
whether it arrived over HTTP or SQS.

Canonical form is a `map[string]string` serialised with `encoding/json`, which sorts map
keys — so field ordering cannot change the hash. The amount enters the hash through
`Money.String()`, its canonical two-decimal form, which means `"25"`, `"25.0"` and
`"25.00"` all hash identically.

### Behaviour

| Situation | Result |
|---|---|
| Same key, same hash | the stored result is returned with `idempotentReplay: true` |
| Same key, different hash | `409 IDEMPOTENCY_CONFLICT` |
| Same `(providerId, externalTransactionId)` under a different key | `409` |
| Replay of a completed operation | returns the balance observed **at the original processing** |

That last row matters. A replay does not recompute anything and does not report the
current balance — it reports what the caller would have seen the first time, which is why
`result_balance_minor` is persisted on the transaction row.

### Where it is enforced

Two layers, both in the database:

- `UNIQUE (idempotency_key)` — the key cannot be reused for a different transaction
- `UNIQUE (provider_id, external_transaction_id)` — the operation cannot be re-applied
  under a different key

The application checks for a replay before processing, and also after a unique violation,
because two concurrent requests with the same key will race: one inserts, the other hits
the constraint and then finds the committed result.

---

## Concurrency

### The strategy

Optimistic control with a bounded retry.

```sql
UPDATE wallets
   SET balance_minor = $1, version = $2, updated_at = $3
 WHERE id = $4 AND version = $5
```

If the version moved, zero rows are affected and the service retries — up to three times,
reloading the wallet each time. Beyond that, the request returns `409` and the caller may
retry.

### Why not `SELECT FOR UPDATE`

Pessimistic locking would also be correct, and simpler to reason about. Optimistic was
chosen for three reasons:

1. **No waiting under contention.** A conflict is detected and retried immediately
   instead of blocking a connection while another transaction finishes.
2. **No lock ordering to get wrong.** Deadlocks between transactions touching more than
   one row stop being a concern.
3. **Conflicts become observable.** Every conflict increments
   `wagering_concurrency_conflicts_total`, which turns contention into a number on a
   dashboard rather than into latency nobody can explain.

The trade-off is accepted knowingly: under heavy contention on a single wallet, optimistic
control wastes work by redoing it. The retry is bounded so that waste cannot grow without
limit, and the mandatory scenario — two 80.00 bets against a 100.00 balance — resolves on
the first retry.

### Why there is no global lock

Coordination is per wallet, and only per wallet. Different wallets never touch the same
row, so they proceed in parallel at the database level. On the SQS side, `MessageGroupId`
is the wallet id, so FIFO ordering is preserved **within** a wallet while different wallets
are consumed concurrently.

### What stops a negative balance

Three independent layers:

1. `Wallet.Debit` refuses to produce a negative balance in the domain
2. `CHECK (balance_minor >= 0)` on the wallets table
3. `CHECK (balance_before ± amount = balance_after)` plus
   `CHECK (balance_after_minor >= 0)` on the ledger

Any one of them is enough. All three are present because this is the invariant whose
violation is both the most expensive and the hardest to notice after the fact.

---

## Reversals

`REFUND` and `ROLLBACK` both require `referenceExternalTransactionId`, resolved by
`(providerId, referenceExternalTransactionId)`.

### Validation

The reversal and its reference must agree on provider, player, wallet, currency and round,
and the amounts must be **exactly equal**. Partial reversals are out of scope, so any
other amount is rejected with `REFERENCE_MISMATCH` rather than partially applied.

### Direction

| Reference kind | Reversal effect |
|---|---|
| `BET` (debit) | credit |
| `WIN` (credit) | debit |
| `REFUND` (credit) | debit |

A reversal that would need to debit more than the current balance is rejected with
`REVERSAL_EXCEEDS_BALANCE` — a **different** code from `INSUFFICIENT_FUNDS`, as the
challenge requires, because the two situations call for different reactions from the
provider.

### Preventing double reversal

A partial unique index:

```sql
CREATE UNIQUE INDEX wager_transactions_single_successful_reversal
    ON wager_transactions (reference_internal_id, kind)
    WHERE state = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK');
```

The same reference cannot have two successful reversals **of the same kind**. The
application also checks this before applying, so the normal path returns a clean
`ALREADY_REVERSED` rejection rather than a constraint violation; the index exists for the
case where two reversals commit concurrently.

### Combining REFUND and ROLLBACK on the same bet

The index is scoped by kind, so a bet could in principle receive both a `REFUND` and a
`ROLLBACK`. That is prevented by the amount and state checks rather than by the index:
once the `REFUND` is processed, a `ROLLBACK` of the original bet is still allowed, because
a rollback of a refund is a legitimate operation — but it reverses the refund, not the
bet, and it is the refund that becomes its reference. A rollback pointing at a bet that
was already fully refunded finds the bet in `PROCESSED` and the balance already restored;
debiting it again would be a second reversal of the same movement, so it is rejected with
`ALREADY_REVERSED` when the reference already carries a successful reversal.

---

## Pending references

A reversal can arrive before the operation it reverses. SQS does not order messages across
groups, and providers retry out of order.

Rejecting immediately would be wrong — the reference may arrive a second later. Blocking
would hold a transaction open on something that may never come. So the operation is
persisted as `PENDING_REFERENCE` and the response is `202 Accepted`.

### The worker

A background worker sweeps pending reversals:

```
claim a batch (FOR UPDATE SKIP LOCKED)
for each one:
  reference found and PROCESSED  →  apply the reversal
  reference found but pending    →  defer
  reference found but failed     →  reject (REFERENCE_NOT_USABLE)
  reference not found            →  defer, or reject if exhausted
```

Deferring records an attempt and schedules the next one with exponential backoff
(`2^attempts × base`). When `REFERENCE_MAX_ATTEMPTS` or `REFERENCE_TTL` is reached, the
transaction is rejected with `REFERENCE_NOT_FOUND` and the rejection event is published.

### Why the state lives in the database

`reference_attempts` and `reference_next_retry_at` are columns, not in-memory counters.
That is what makes the requirement *"inclusive após reinicialização da aplicação"* hold:
the worker can die mid-sweep and another instance picks up exactly where it left off.
`SKIP LOCKED` ensures two instances never work on the same pending reversal.

---

## Inbox

SQS delivers **at least once**. The same message will be delivered twice — it is a
question of when, not if: a consumer that commits and dies before deleting, a network
timeout on the delete, a visibility timeout that expires mid-processing.

The inbox turns at-least-once delivery into exactly-once *effect*:

```
1. INSERT (consumerName, messageId) ─┐
2. process the operation             │
3. move the balance                  ├─ one transaction
4. write the ledger                  │
5. write the outbox events           │
6. mark the inbox record complete   ─┘
7. COMMIT
8. only now, delete from the queue
```

If the process dies between 7 and 8, the message returns — and step 1 recognises it as a
duplicate, so nothing is applied twice. The message is simply deleted.

`PRIMARY KEY (consumer_name, message_id)` is what makes step 1 reliable: the uniqueness is
enforced by the database, so two consumers racing on the same message cannot both proceed.

The hash of the body is stored as well. A redelivery carrying a **different** body under
the same `messageId` is not a legitimate retry — it is a corrupted or forged message, and
it goes to the DLQ.

### Message handling policy

| Outcome | Action |
|---|---|
| Processed successfully | delete from the queue |
| Business rejection (terminal) | delete — retrying would not change the result |
| Duplicate caught by the inbox | delete |
| Malformed envelope | send to the DLQ, then delete |
| Permanent error (unknown wallet, invalid kind) | send to the DLQ, then delete |
| Transient failure (database unreachable) | **do not delete** — let SQS redeliver |

Transient and permanent are distinguished by the error itself, through `errors.Is` against
the domain sentinels. Anything not recognised as permanent is treated as transient, which
is the safe default: a message redelivered unnecessarily is deduplicated by the inbox,
while a message discarded wrongly is lost.

Exhausted deliveries reach the DLQ through the queue's redrive policy
(`maxReceiveCount: 5`).

---

## Outbox

Events are written to `outbox_events` inside the same transaction as the domain change, so
an event cannot describe something that was rolled back, and a committed change cannot
fail to produce its event.

A separate worker publishes them:

```sql
WITH claimed AS (
    SELECT event_id FROM outbox_events
     WHERE published_at IS NULL
       AND next_retry_at <= now()
       AND (locked_until IS NULL OR locked_until <= now())
     ORDER BY occurred_at
     LIMIT $1
     FOR UPDATE SKIP LOCKED
)
UPDATE outbox_events SET locked_until = $2 ...
```

`SKIP LOCKED` lets several publishers run without fighting over the same rows.
`locked_until` is the recovery mechanism: if a publisher dies holding a claim, the lock
expires and another instance picks the event up.

### The window that cannot be closed

Between "message sent to SQS" and "marked as published in the outbox" there is an
unavoidable gap. If the process dies there, the event is published but still marked
pending, and it will be published again.

This is accepted, not hidden. The mitigation is that **`eventId` is preserved across
republications**, so a consumer can deduplicate. The alternative — marking as published
before sending — would risk losing events entirely, which is strictly worse. At-least-once
with a stable identity is the right trade.

Failures increment `attempts` and reschedule with exponential backoff. `outbox_lag_seconds`
exposes the age of the oldest unpublished event, which is the metric that tells you the
publisher is stuck before anyone notices downstream.

### Events

| Event | Trigger |
|---|---|
| `WagerTransactionProcessed` | successful completion, including `LOSS` |
| `WagerTransactionRejected` | definitive business rejection |
| `WalletBalanceChanged` | effective balance change |
| `WagerTransactionPendingReference` | waiting for a reference |

The envelope carries `eventId`, `eventType`, `aggregateId`, `aggregateType`,
`correlationId`, `causationId`, `occurredAt` (RFC 3339 UTC), `version` and a typed `data`
payload. Type and version come from the event constructor, never from the caller. The
payload is an immutable snapshot: it records what was true at that moment, not a reference
to be resolved later.

---

## Database invariants

What the schema enforces, independently of the application:

| Invariant | Mechanism |
|---|---|
| Balance never negative | `CHECK (balance_minor >= 0)` |
| One wallet per player and currency | `UNIQUE (player_id, currency)` |
| Ledger is append-only | `BEFORE UPDATE OR DELETE` trigger raising an exception |
| Ledger arithmetic is consistent | `CHECK (balance_after = balance_before ± amount)` |
| One ledger entry per wallet and transaction | `UNIQUE (wallet_id, transaction_id)` |
| One opening per wallet | partial unique index `WHERE kind = 'OPENING'` |
| Idempotency key is unique | partial unique index |
| Operation unique per provider | `UNIQUE (provider_id, external_transaction_id)` |
| One successful reversal per kind | partial unique index on processed reversals |
| `LOSS` carries zero | `CHECK (kind <> 'LOSS' OR amount_minor = 0)` |
| `OPENING` carries no external metadata | `CHECK` on the metadata columns |
| Failure code only on failure | `CHECK (state IN ('REJECTED','FAILED') OR failure_code IS NULL)` |

The append-only trigger deserves a note: it raises rather than silently ignoring, so an
accidental `UPDATE` surfaces as an error instead of appearing to succeed.

---

## Authentication and authorization

### The choice

Keycloak, with `client_credentials`. The challenge recommended it and the recommendation
is sound: it is a real OIDC provider with discovery, JWKS rotation and standard token
introspection, and it can be provisioned declaratively — which matters, because the
evaluator has to get a working identity provider with one command.

`client_credentials` is the right grant because there is no human here. A game provider is
a service, and the token represents the service, not a user.

### How identity is resolved

The realm injects a hardcoded claim into each client's token:

| Client | Claim |
|---|---|
| `provider-a` | `provider_id: provider-a` |
| `provider-b` | `provider_id: provider-b` |
| `wallet-admin` | `scope_internal: wallet:open` |

The verifier reads `provider_id` first; if absent, it looks for the internal claim; if
neither is present the token is rejected. There is deliberately **no fallback** to `azp` or
`client_id` — an earlier iteration had one, and it quietly turned the internal service into
a "provider", which broke the internal-only rule. Identity has to be explicit.

The crucial property: **the request body never decides who the caller is**. If
`provider-b` sends `"providerId": "provider-a"`, the request is rejected with `403`,
because the body is compared against the identity in the signed token.

### Authorization rules

| Rule | Enforcement |
|---|---|
| A provider only sees its own transactions | `provider_id` filter on every query and replay |
| `providerId` in the body must match the token | comparison in the handler |
| Opening a wallet is internal only | requires the internal identity |
| Reconciliation is internal only | same |
| `OPENING` is rejected from outside | domain-level, in `NewExternalTransaction` |

Health and metrics are public, so that probes and Prometheus can reach them without
credentials.

### Issuer and discovery are separate

Tokens are issued with the public hostname (`localhost:8081`), but the API reaches Keycloak
over the Docker network (`keycloak:8080`). The issuer in the token therefore does not match
the URL the API would discover from.

The tempting fix is to skip issuer validation. That would be a real security hole: issuer
validation is what prevents a token minted by a different identity provider from being
accepted.

The correct fix is to separate the two concerns:

```
OIDC_DISCOVERY_URL  →  where the keys are fetched from   (keycloak:8080)
OIDC_ISSUER_URL     →  which issuer is accepted          (localhost:8081)
```

`go-oidc` supports exactly this through `InsecureIssuerURLContext`. Despite the name, the
result is stricter than skipping the check: the issuer is still validated, just against a
value that is configured rather than inferred.

### Out of scope

Password storage, user registration and token issuance are not implemented — the challenge
places them outside the scope, and they belong to the identity provider anyway.

---

## Composition with Fx and shutdown

### Modules

```go
fx.New(
    configModule,          // configuration, validated at startup
    observabilityModule,   // logger and metrics
    databaseModule,        // pool and repositories
    authModule,            // OIDC verifier
    serviceModule,         // use cases
    messagingModule,       // SQS client, consumer, publisher, reference worker
    httpModule,            // handler and server
)
```

Dependencies are declared by constructor signature and resolved by Fx. Nothing reaches for
a global, and there is no initialisation order written by hand — Fx derives it from the
graph.

The practical benefit shows up in the integration tests: the same constructors are called
directly with test doubles for configuration, without Fx being involved at all.

### Lifecycle

| Component | `OnStart` | `OnStop` |
|---|---|---|
| Database pool | connect and ping | close after everything that uses it |
| OIDC verifier | discover the provider, retrying | — |
| SQS consumer | start workers | stop fetching, finish in-flight work |
| Outbox publisher | start the loop | stop and drain |
| Reference worker | start the sweep | stop |
| HTTP server | bind and serve | graceful shutdown |

The OIDC discovery retries for up to 30 seconds. Keycloak is slow to become ready and the
API would otherwise fail to start on a cold `docker compose up` — a startup that depends
on a race is not a startup.

### Shutdown

On `SIGTERM`, Fx runs the stop hooks in reverse order of start:

1. the HTTP server stops accepting new connections and finishes what is in flight
2. the workers stop fetching and complete what they are holding, within the grace period
3. the database pool closes last, after everything that uses it is done

For the consumer specifically: it stops polling immediately, then waits for in-flight
messages. Anything that does not finish within `SQS_SHUTDOWN_GRACE` is simply not deleted,
so SQS redelivers it and the inbox prevents double processing. Not finishing is safe by
design, rather than by hope.

Note that the pool closes **after** the workers. Closing it first would make in-flight work
fail during a clean shutdown — which is the kind of detail that only shows up under load in
production.

---

## Observability

**Logs** are structured JSON carrying whichever identifiers are available —
`correlationId`, `messageId`, `transactionId`, `walletId`, `providerId`. Credentials and
full financial payloads are never written. An incoming `X-Correlation-Id` is honoured and
echoed, so a request can be followed across services.

**Metrics** are exposed at `/metrics` under the `wagering` namespace, in a dedicated
registry rather than the global default — which keeps tests isolated from each other.

All labelled series are **pre-initialised at zero on startup**. Without that, a counter
like `dlq_messages_total` would not exist until the first failure, so an alert on "DLQ rate
above zero" would never fire and a dashboard would render empty. A metric that only appears
when things are already broken is not much use.

**Health checks** separate liveness (the process is up) from readiness (PostgreSQL and SQS
are reachable), so an orchestrator can tell "restart me" apart from "stop sending me
traffic".

---

## Interpretations

Where the statement allowed more than one reading, this is what was chosen.

**Zero amounts.** `LOSS` requires exactly `"0.00"` and produces no ledger entry and no
version bump, but it does emit `WagerTransactionProcessed` — it is a real business event,
just not a financial movement. `BET`, `WIN`, `REFUND` and `ROLLBACK` require an amount
greater than zero. An opening balance of zero creates the wallet but no `OPENING`
transaction, no ledger entry and no events.

**Partial reversals.** Out of scope, so the reversal amount must equal the referenced
amount exactly. Anything else is `REFERENCE_MISMATCH`, never a partial application.

**`-0.00`.** Accepted as zero by `ParseMoney`. It is syntactically negative but numerically
zero, and rejecting it would add a special case without protecting anything.

**Equivalent decimal forms.** `"25"`, `"25.0"` and `"25.00"` are the same value and hash
identically, because the hash uses the canonical form rather than the raw string. This is
the normalisation the challenge asks to be documented.

**Synchronous completion.** Operations with no pending dependency complete within the
request, with no intermediate "accepted" commit. The challenge allows this explicitly. Only
reversals waiting on a reference are persisted in a non-terminal state.

**Currency.** Only BRL is exercised in the main scenarios, but `Money` always carries its
currency, arithmetic between different currencies is rejected, and there are tests for the
mismatch. Supporting a second currency needs no change to the domain.

---

## Limitations and unfinished work

Stated plainly, because knowing where the edges are is part of the work.

### Not implemented

**Double-entry bookkeeping.** The ledger is single-sided: each entry records a debit or a
credit against one wallet. The challenge lists double-entry as optional. A full
implementation would add a counterpart account per movement and would be the natural next
step for accounting-grade auditability.

**Distributed tracing.** No OpenTelemetry. `correlationId` propagates through logs and
events, which covers the diagnosis the challenge requires, but there are no spans.

**Load tests.** None were run, so there are no throughput or percentile numbers to report.
Rather than publish a figure produced in five minutes on a laptop, the omission is recorded
here. The latency histogram is in place, so the instrumentation side is ready.

### Tested, but not automated

Two scenarios from the statement were reasoned about and are supported by the design, but
are not covered by an automated test:

**Killing the consumer between the commit and the message deletion.** The inbox makes this
safe — a redelivered message is recognised and discarded — and the duplicate path *is*
covered by a test that sends the same message twice. What is missing is a test that kills
the process at precisely that point.

**Restarting the application with work in flight.** All the relevant state is in the
database (`PENDING_REFERENCE`, `reference_attempts`, unpublished outbox rows), and the
workers claim with `SKIP LOCKED`, so another instance resumes. Again: supported by design,
not proven by a test that stops and starts the process.

Both would be worth adding. They were left out because they need orchestration of process
lifecycle inside the test suite, and the time was better spent on the scenarios that could
be proven end to end.

### Known trade-offs

**Optimistic locking wastes work under heavy single-wallet contention.** Accepted: retries
are bounded, conflicts are measured, and independent wallets are unaffected.

**The outbox can republish an event.** Unavoidable without distributed transactions.
Mitigated by preserving `eventId`, so consumers can deduplicate.

**Reconciliation reads the whole ledger of a wallet.** Fine at the volumes here; a wallet
with millions of entries would want incremental checkpoints.

**The pending reference worker polls.** A notification channel would react faster, but
polling is simpler, survives restarts without extra machinery, and the latency is bounded
by `REFERENCE_POLL_INTERVAL`.
