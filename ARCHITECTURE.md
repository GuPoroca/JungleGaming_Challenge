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
- **Concurrency strategy**: not yet decided in code — `version` exists to
  support either pessimistic locking (`SELECT ... FOR UPDATE`) or
  optimistic compare-and-swap (`UPDATE ... WHERE version = $1`) once the
  repository layer is built. Leaning pessimistic (simpler to reason about
  for this domain, avoids a retry loop), but this isn't final until the
  repository exists.

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
- **Not yet decided**: the concurrency-control mechanism (pessimistic
  lock vs. optimistic CAS) touches these tables but isn't implemented
  yet — no repository code exists to attach it to. `wallets.version`
  exists specifically so either approach can use it once that layer is
  built.

## Go module

- Module path: `github.com/gustavoporoca/jungle-gaming-challenge`.
- `go.mod` pins `go 1.23` rather than the exact local toolchain version, so
  the declared version stays portable across machines and the eventual
  Docker build.

## Pending (not yet implemented)

- Inbox / outbox (§6.5).
- Idempotency (key handling, payload hashing, conflict detection).
- The concurrency strategy above, once there's a repository to attach it
  to.
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
  publisher.
- `pgx` repositories over the schema in `migrations/` (the schema itself
  is done — see Persistence schema above).
- HTTP and SQS transport.
