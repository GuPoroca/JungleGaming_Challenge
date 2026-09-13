# Architecture

This document is written incrementally, alongside the implementation. It
records decisions as they're made, not a plan for the whole system made in
advance. Sections not yet reached are listed as pending at the bottom
rather than speculated about.

## Money (`internal/domain/money`)

- **Representation**: `int64` minor units (e.g. cents for BRL) at a fixed
  scale of two decimal digits. No `float32`/`float64` anywhere in parsing,
  arithmetic, or formatting. Range is the full `int64` domain (~±92.23
  quadrillion minor units); arithmetic that would exceed it returns
  `ErrOverflow` instead of wrapping.
- **Why `int64` over a decimal library**: no extra dependency, full control
  over overflow detection, and the scale is fixed at 2 anyway per the
  external contract, so a general-purpose decimal type buys little here.
- **Parsing is strict, not lenient**: `Parse` requires exactly two
  fractional digits (`"25.00"`); `"25"` and `"25.0"` are rejected rather
  than padded. This avoids ever needing to document an "equivalent form"
  normalization ahead of the idempotency hash.
- **Negative amounts**: `Parse`/`UnmarshalJSON` (the external-input path)
  reject negative amounts outright. `Sub`/`Negate` (internal arithmetic)
  can still produce a negative `Money` — needed for things like a
  reconciliation `difference` that legitimately can be negative. That
  value is only ever produced internally and serialized out via
  `MarshalJSON`; nothing parses a negative amount back in.
- **Currency validation** checks only the ISO 4217 alphabetic shape (three
  uppercase letters), not membership in the real ISO 4217 list. Documented
  limitation, not an oversight.

## Wallet (`internal/domain/wallet`)

- Aggregate root: `id`, `playerId`, `balance` (a `Money`, so currency comes
  along with it), `version`, `createdAt`, `updatedAt`.
- **`New` vs `Rehydrate`** are separate constructors. `New` always starts
  at version 1; `Rehydrate` only validates persisted state, it never
  re-derives it.
- **`Debit`/`Credit` return `(Wallet, Movement, error)`**, where `Movement`
  is `{Direction, Amount, BalanceBefore, BalanceAfter}`. `Wallet` does
  **not** import the `ledger` package — it hands back the data a ledger
  entry needs and leaves entry construction to the caller. This keeps
  `Wallet`'s only dependency `Money`, and keeps the
  `balanceAfter = balanceBefore ± amount` check in exactly one place
  (`ledger`), rather than duplicating it. The tradeoff: nothing at the
  type level forces every debit/credit to actually become a ledger entry —
  that pairing has to be enforced by the use-case layer (and tested for,
  per §13's requirement to check ledger sums against the stored balance).
- **Non-negative balance** is enforced in `Debit`: it computes
  `balance.Sub(amount)` (which is allowed to go negative — see Money
  above), then rejects the operation with `ErrInsufficientBalance` if the
  result is negative, before ever returning a mutated `Wallet`. `Money`
  itself has no opinion on this; the invariant belongs to `Wallet`.
- **Version** starts at 1 on creation and increments by exactly 1 on every
  *successful* `Debit`/`Credit`. A failed call returns zero values and
  leaves the receiver untouched (Go value semantics, not extra code).
- **Concurrency strategy: pessimistic locking, decided and verified.**
  `WalletRepository.FindByIDForUpdate` takes `SELECT ... FOR UPDATE`
  inside a transaction before any read the operation depends on; a
  concurrent operation against the *same* wallet blocks on that `SELECT`
  until the first transaction commits or rolls back, so the two can never
  race on the later `UPDATE`. Different wallets are different rows, so
  they never contend — locking is per-row, not global. This was chosen
  over optimistic compare-and-swap (`UPDATE ... WHERE version = $1`
  with a retry loop) because a debit's SQL transaction also has to write
  a ledger entry and update the triggering `WagerTransaction`'s state in
  the same commit; retrying a version conflict would mean re-doing all
  three, whereas locking the wallet row up front means there's nothing to
  retry at all. See "Persistence: pgx layer" below for how this is
  actually exercised against a real database, including the §8-mandated
  concurrent-debit scenario.

## Ledger (`internal/domain/ledger`)

- `Entry` is immutable: `id`, `walletId`, `transactionId`, `direction`,
  `amount`, `balanceBefore`, `balanceAfter`, `createdAt`. No mutating
  methods — a correction is a new `Entry`, never an edit to an existing
  one.
- **Dependency direction**: `ledger` depends on `wallet` (for `wallet.ID`
  and `wallet.Direction`) and on `money`. It deliberately does **not**
  depend on a `wagertransaction` package — `transactionId` is a plain
  local string type, so `ledger` doesn't force a dependency on a domain
  type that doesn't exist yet (and, more importantly, so the application
  layer decides how the two relate rather than `ledger` assuming it).
- **`New` takes a `wallet.Movement` directly**: the numbers on the entry
  are exactly the numbers `Wallet` computed for the same operation, with
  no separate transcription step where they could drift apart.
- **Validation**: both `New` and `Rehydrate` check
  `balanceAfter = balanceBefore ± amount` per `direction` before allowing
  an `Entry` to exist. `Rehydrate` re-checks this on read too, as a
  defensive check against corrupted persisted data.
- **Not yet enforced**: uniqueness of `(walletId, transactionId)` and
  protection against edit/delete are database-level responsibilities
  (constraints, revoked grants, or a trigger), to be added with the
  migrations — this package can't enforce them on its own.

## WagerTransaction (`internal/domain/wagertransaction`)

- Carries every field §6.3 asks for: internal/external ids, provider,
  idempotency key, payload hash, wallet/player/round/game, kind, amount,
  optional reference, resolved reference, state, failure code, and a
  `resultingBalance` snapshot. That snapshot exists specifically so an
  idempotent replay can return the balance observed at original
  processing time, even if the wallet has moved since (§9).
- **Two constructors for two origins**: `NewExternal` (HTTP/SQS) rejects
  `OPENING` outright and requires every external field to be present;
  `NewOpening` only takes the internal fields (wallet, player, amount) —
  provider, external id, idempotency key, payload hash, round, game, and
  reference genuinely don't exist for this origin, so they aren't even
  parameters, not just left empty. `Rehydrate` reconstructs any state
  without replaying business logic, but still defensively checks that
  persisted data is structurally consistent (e.g. a `PROCESSED` row must
  carry a `resultingBalance`, a `REJECTED`/`FAILED` row must carry a
  `failureCode`).
- **Amount rule (§7)**: `LOSS` must be exactly zero; every other kind,
  including `OPENING`, must be strictly positive. A zero-balance wallet
  therefore never gets an `OPENING` row at all — that decision belongs to
  the wallet-creation use case (not yet built), not to this constructor,
  which simply refuses to construct a zero-amount `OPENING`.
- **State machine**: `PENDING` is the only non-terminal starting state.
  `MarkPendingReference`, `MarkProcessed`, `MarkRejected`, and
  `MarkFailed` are the only transitions, and all of them refuse to fire
  once the transaction is already terminal (`PROCESSED`, `REJECTED`, or
  `FAILED`) — enforced with one guard (`State.IsTerminal()`) rather than
  per-transition special-casing, so adding a transition later can't
  accidentally skip it.
- **Distinguishing transient vs. permanent failure**: `MarkRejected` is
  for a definitive business-rule outcome (bad input, insufficient
  balance, expired reference) and always carries a `FailureCode` meant to
  be shown back to the provider. `MarkFailed` is for a permanent
  infrastructure failure kept for audit rather than a transient error
  worth retrying — a transient failure (e.g. a momentary DB outage)
  should not call either method at all; the caller just retries the
  operation, since the transaction is still sitting in `PENDING`.
- **`FailureCode` is a stable string enum**, not a free-form message,
  specifically so `INSUFFICIENT_BALANCE_FOR_BET` and
  `INSUFFICIENT_BALANCE_FOR_REVERSAL` stay two different codes, per the
  challenge's explicit requirement that a bet rejected for low balance
  and a reversal rejected for low balance be distinguishable to the
  provider.
- **`ValidateReversalAgreement(reversal, referenced)`** is the one
  reference check this package can do without I/O, given two
  already-loaded transactions: `REFUND` may only reference a `BET`;
  `ROLLBACK` may reference a `BET`, `WIN`, or `REFUND`; the reference must
  be `PROCESSED` (not merely existing); and both must agree on provider,
  player, wallet, currency, round, and amount.
- **Interpretation adopted for REFUND/ROLLBACK combinations**: the
  spec asks explicitly for this to be documented. A given referenced
  transaction may receive **at most one successful reversal in total**,
  regardless of whether it's a `REFUND` or a `ROLLBACK` — not "at most one
  of each type." The spec's literal wording ("uma referência não receba
  duas reversões bem-sucedidas do mesmo tipo") only forbids two of the
  *same* type, but its own justification — preserving financial coherence
  and preventing duplicate return of the same debit — is violated just as
  much by one `REFUND` followed by one `ROLLBACK` on the same bet (that
  would credit the player twice for one debit). We're treating the
  broader financial-coherence rule as the binding one. `FailureCodeDuplicateReversal`
  exists for this outcome, but detecting it requires querying prior
  reversals against the same reference — a repository concern, not
  something this package can check on a single already-loaded value.

## Persistence schema (`migrations/`)

- **Tool**: [golang-migrate](https://github.com/golang-migrate/migrate),
  run through a `migrate` service in `docker-compose.yml` (the official
  `migrate/migrate` image, `tools` profile) — no local install required.
  `up`/`down` are both scripted and were both actually run against a real
  Postgres container to confirm they work, not just written and assumed
  correct.
- **One migration so far** (`000001_init_financial_schema`) covering the
  three aggregates that exist in Go: `wallets`, `wager_transactions`,
  `wallet_ledger_entries`. No inbox/outbox tables yet — those domain
  types don't exist, and a table with no corresponding Go type is just
  dead schema.
- **IDs are always application-supplied** (`UUID PRIMARY KEY`, no
  `DEFAULT gen_random_uuid()`), matching the Go constructors, which take
  an id as a parameter rather than generating one internally.
- **Every domain invariant already enforced in Go is mirrored as a
  database constraint**, per §5.8's requirement that non-negativity,
  uniqueness, and ledger immutability be enforced by the schema itself,
  not only by application code that could have a bug or simply not be
  the only writer:
  - `wallets`: `balance_minor_units >= 0`, `version >= 1`,
    `UNIQUE (player_id, currency)`.
  - `wager_transactions`: a `CHECK` per §7's amount rule (`LOSS` = 0,
    everything else > 0); a `CHECK` mirroring `validateFieldsForKind`
    (OPENING carries none of the external metadata, every other kind
    carries all of it); a `CHECK` requiring a reference exactly for
    `REFUND`/`ROLLBACK`; a `CHECK` requiring `PROCESSED` to carry a
    `resulting_balance` and `REJECTED`/`FAILED` to carry a
    `failure_code`; `UNIQUE (provider_id, external_transaction_id)`;
    `UNIQUE (provider_id, idempotency_key)`; and a partial unique index
    (`WHERE kind = 'OPENING'`) capping a wallet at one `OPENING` row.
  - `wallet_ledger_entries`: a `CHECK` enforcing
    `balanceAfter = balanceBefore ± amount` per `direction`, and
    `UNIQUE (wallet_id, transaction_id)`.
- **Two triggers add protection Go alone can't provide**, because they
  guard against *any* writer, not just the application's own code path:
  - `trg_wagertx_terminal_immutable` rejects any `UPDATE` on a
    `wager_transactions` row already in a terminal state
    (`PROCESSED`/`REJECTED`/`FAILED`) — the same rule
    `WagerTransaction.MarkX` already enforces in Go, now also true even
    if a bug or a direct SQL statement bypasses the domain layer.
  - `trg_ledger_immutable` rejects any `UPDATE` or `DELETE` at all on
    `wallet_ledger_entries` — the ledger is append-only, enforced as a
    hard database rule rather than by convention.
  - Both were verified with real `UPDATE`/`DELETE`/`INSERT` statements
    against a running Postgres, not just written and trusted: a negative
    balance, a duplicate wallet, a `BET` missing external metadata, a
    ledger row with the wrong `balanceAfter` for its direction, editing
    or deleting a ledger row, and re-transitioning a terminal transaction
    were all attempted and all rejected.
## Persistence: pgx layer (`internal/platform/postgres`)

- **Library**: `pgx/v5` with explicit SQL (no `sqlc`, no ORM) — every
  query in this package is hand-written and visible, so the transaction
  boundary and the exact columns touched are never hidden behind
  generated code.
- **`Querier` interface** (`Exec`/`Query`/`QueryRow`) is satisfied by
  both `*pgxpool.Pool` and `pgx.Tx`. Every repository method takes a
  `Querier` rather than a concrete type, so *the caller* decides the
  transaction boundary by choosing what to pass — the pool for one
  read, an active `pgx.Tx` (via the `WithTx` helper) when several
  repository calls must commit or roll back together. `WalletRepository`
  and `WagerTransactionRepository` both follow this shape; a ledger
  repository (still pending) will too.
- **No repository interface (port) is defined yet.** `internal/app`,
  which will own that interface, doesn't exist yet either. Defining an
  interface before its only consumer exists would mean guessing its
  shape; `WalletRepository`/`WagerTransactionRepository` are concrete
  structs today, and Go's structural typing means either can satisfy
  whatever interface `internal/app` ends up declaring once it exists,
  with no changes needed here.
- **`WagerTransactionRepository`** covers `Create`, `FindByID`,
  `FindByIDForUpdate`, `FindByProviderAndExternalID` (the §7 reference
  lookup and the idempotent-replay-by-business-key path),
  `FindByProviderAndIdempotencyKey` (the `Idempotency-Key` header lookup
  path), and `Update` (state transitions). `Create` classifies Postgres
  errors into `ErrTransactionAlreadyExists` (any of the table's three
  uniqueness rules — see the schema section above) and `ErrWalletNotFound`
  (the FK to `wallets`), so the use-case layer never has to inspect a raw
  `pgconn.PgError` itself.
- **The terminal-transition trigger was deliberately left unmapped.**
  `Update`'s doc comment is explicit that a rejection from
  `trg_wagertx_terminal_immutable` propagates as a raw Postgres error
  rather than being translated into `wagertransaction.ErrTerminalTransaction`
  — correct application code is expected to check `State().IsTerminal()`
  in Go before ever calling `Update`, so hitting the trigger at all means
  something already went wrong upstream; it's a safety net, not a
  documented code path the use-case layer is meant to branch on.
  `TestWagerTransactionRepository_Update_TerminalRowRejectedByTrigger`
  confirms the trigger actually fires, not that the Go layer alone is
  trusted to prevent it.
- **`LedgerRepository` exposes no `Update` or `Delete` at all** — not
  just "the schema rejects it," but there is no method here that could
  even attempt one. `Create` classifies the same way the other two
  repositories do: a uniqueness violation on `(walletId, transactionId)`
  becomes `ErrLedgerEntryAlreadyExists` (the schema's last line of
  defense against ever recording two movements for one transaction), and
  a foreign key violation is disambiguated by constraint name into
  `ErrWalletNotFound` or `ErrTransactionNotFound` — the first repository
  where two different FKs on one table needed telling apart, so `Create`
  inspects `pgErr.ConstraintName` rather than just checking the
  SQLSTATE.
- **`FindByWalletID` implements the ledger's cursor pagination as keyset
  pagination** on `(created_at, id)`, using the index built for exactly
  this (`idx_ledger_wallet_pagination`). It fetches `limit+1` rows to
  determine whether another page exists, then trims to `limit`. The
  returned `LedgerCursor` is a typed Go value `{CreatedAt, ID}`, not yet
  an opaque wire string — turning it into and out of the HTTP query
  param's opaque cursor is deliberately left to the HTTP layer (not
  built yet), since encoding is a transport concern, not a query
  concern. Order is oldest-first, matching how a bank statement reads.
- **`SumBalanceByWallet` is what the reconciliation endpoint will call**:
  credits minus debits over every entry for a wallet, plus the count for
  the response's `checkedEntries`. `currency` is a parameter rather than
  inferred from the rows because a wallet with a zero initial balance
  has no entries at all to infer it from.
- **IDs bind as plain Go `string`** against `uuid` columns — pgx's
  built-in `uuid` codec accepts and returns canonical text form, so no
  `pgtype.UUID` wrapper is needed to match the domain's string-based
  `wallet.ID`/`wallet.PlayerID` types.
- **Money round-trips through `MinorUnits()`/`FromMinorUnits`**, added to
  the `money` package specifically for this: `Money` had a constructor
  *from* raw minor units but no accessor to get them back out for a
  `BIGINT` column, which persistence obviously needs.
- **Integration tests are real, not mocked**, gated behind a
  `//go:build integration` tag (documented in
  `internal/platform/postgres/wallet_repository_test.go`) so
  `go test ./...` stays fast and infra-free, while
  `go test -tags=integration -race ./internal/platform/postgres/...`
  exercises an actual Postgres started via `docker compose up -d postgres`
  with the migration already applied.
- **The §8-mandated concurrency test is already passing for real**: a
  wallet with 100.00 BRL, two goroutines each opening their own
  transaction and racing to debit 80.00 BRL via `FindByIDForUpdate` +
  `Update`. Verified outcome: exactly one succeeds, the other fails with
  `wallet.ErrInsufficientBalance`, final balance is 20.00, and the
  version advances by exactly 1 (proving only one debit actually landed,
  not that both landed and something else masked it). This is a genuine
  row-lock-driven serialization, not a mock standing in for one.

## Go module

- Module path: `github.com/gustavoporoca/jungle-gaming-challenge`.
- `go.mod`'s `go` directive has been bumped by tooling as dependencies
  were added (`1.23` → `1.25.0`, when `pgx/v5` was introduced) rather
  than pinned by hand — `go get`/`go mod tidy` raise it to the minimum
  the resolved dependency graph actually requires. The Docker build will
  need to use a matching `golang` base image tag once a Dockerfile
  exists.

## Pending (not yet implemented)

- All three repositories the domain needs now exist (`WalletRepository`,
  `WagerTransactionRepository`, `LedgerRepository`). What's still missing
  is the use-case layer that opens one transaction and calls all three
  together — nothing wires them into a single unit of work yet.
- Inbox / outbox (§6.5) — no tables, no domain types, no repository.
- Idempotency (key handling, payload hashing, conflict detection). The
  lookups it needs (`FindByProviderAndIdempotencyKey`,
  `FindByProviderAndExternalID`) exist; the comparison-and-replay logic
  that uses them does not.
- Resolving a reversal's reference by querying storage — the domain layer
  only validates agreement once both sides are already loaded (see
  `ValidateReversalAgreement` above); looking the reference up by
  `(providerId, referenceExternalTransactionId)` is a repository concern.
- The pending-reference worker: retry/backoff timing, max attempts or
  TTL, and finalizing an exhausted `PENDING_REFERENCE` as `REJECTED` with
  `FailureCodeReferenceNotFound`.
- Detecting a duplicate successful reversal against the same reference
  (`FailureCodeDuplicateReversal`) — needs a query over prior reversals,
  which the use-case layer / a database constraint will provide.
- Authentication / authorization (OIDC via Keycloak).
- Uber Fx composition beyond the minimal lifecycle bootstrap in `main.go`.
- Graceful shutdown behavior for HTTP, the SQS consumer, and the outbox
  publisher. Wiring `NewPool` and the three repositories into the Fx app
  itself is also still pending — they exist as plain constructors today.
- HTTP and SQS transport.
