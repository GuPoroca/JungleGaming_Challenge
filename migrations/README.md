# Migrations

Managed with [golang-migrate](https://github.com/golang-migrate/migrate),
run via the `migrate` service in `docker-compose.yml` — no local install
required. It's in the `tools` profile so `docker compose up` never starts
it by itself.

Naming convention: `{sequence}_{description}.up.sql` /
`{sequence}_{description}.down.sql`, e.g.
`000001_init_financial_schema.up.sql`.

```sh
# start Postgres first
docker compose up -d postgres

# apply all pending migrations
docker compose --profile tools run --rm migrate up

# roll back the last migration
docker compose --profile tools run --rm migrate down 1
```

## `000001_init_financial_schema`

Creates `wallets`, `wager_transactions`, and `wallet_ledger_entries` —
the three persisted aggregates built so far. Inbox/outbox tables aren't
here yet; those domain types don't exist in Go yet either, so there's no
schema for them until there is.

Every constraint mirrors a domain invariant already enforced in Go, so
the database rejects the same things the domain does even if a bug, a
direct SQL statement, or a future caller bypasses the Go layer entirely:

- `wallets.balance_minor_units >= 0` and `version >= 1`.
- `wager_transactions`: the amount rule per kind (`LOSS` = 0, else > 0),
  which metadata fields OPENING must/mustn't carry, when a reference is
  required, and that a terminal state (`PROCESSED`/`REJECTED`/`FAILED`)
  carries the data it requires.
- A trigger rejects any `UPDATE` on a `wager_transactions` row that is
  already terminal — a terminal transaction must never transition again.
- `wallet_ledger_entries.balance_after = balance_before ± amount`,
  checked per `direction`.
- A trigger rejects any `UPDATE` or `DELETE` on `wallet_ledger_entries` —
  the ledger is append-only, full stop.
- `UNIQUE (wallet_id, transaction_id)` on the ledger, `UNIQUE (provider_id,
  external_transaction_id)` and `UNIQUE (provider_id, idempotency_key)`
  on transactions, and a partial unique index allowing at most one
  `OPENING` row per wallet.

IDs are always supplied by the application (no `DEFAULT
gen_random_uuid()`), matching the Go constructors, which take an id as a
parameter rather than generating one internally.
