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

## Go module

- Module path: `github.com/gustavoporoca/jungle-gaming-challenge`.
- `go.mod` pins `go 1.23` rather than the exact local toolchain version, so
  the declared version stays portable across machines and the eventual
  Docker build.

## Pending (not yet implemented)

- `WagerTransaction` domain type and its five-state machine (§6.3).
- Inbox / outbox (§6.5).
- Idempotency (key handling, payload hashing, conflict detection).
- The concurrency strategy above, once there's a repository to attach it
  to.
- Pending-reference handling for `REFUND`/`ROLLBACK` arriving early.
- Reversal semantics (`REFUND` vs `ROLLBACK` combinations on one `BET`).
- Authentication / authorization (OIDC via Keycloak).
- Uber Fx composition beyond the minimal lifecycle bootstrap in `main.go`.
- Graceful shutdown behavior for HTTP, the SQS consumer, and the outbox
  publisher.
- Persistence: PostgreSQL schema, migrations, `pgx` repositories.
- HTTP and SQS transport.
