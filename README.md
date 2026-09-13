# Jungle Gaming Challenge

A Go service that processes betting-provider operations (`BET`, `WIN`,
`LOSS`, `REFUND`, `ROLLBACK`) against player wallets — money handled
without floats, an append-only ledger, persistent idempotency, and
correct behavior under concurrency and partial failure. Built against a
take-home backend challenge spec (distributed bet processing in Go with
Uber Fx); the spec itself isn't part of this repo.

This README covers what actually exists right now and how to run it —
not the finished shape of the whole system. `ARCHITECTURE.md` records the
design decisions behind what's built so far and lists what's still
pending; read it if you want the *why*, not just the *how*.

## Status

Built so far:

- Domain model (`internal/domain/`): `Money`, `Wallet`, `Ledger`,
  `WagerTransaction` — all pure Go, no infrastructure dependency, fully
  unit tested.
- Postgres schema (`migrations/000001_init_financial_schema`) for
  `wallets`, `wager_transactions`, `wallet_ledger_entries`.
- `internal/platform/postgres`: connection pool and all three
  repositories (`WalletRepository`, `WagerTransactionRepository`,
  `LedgerRepository`), integration-tested against real Postgres,
  including the mandatory concurrent-debit scenario (§8 of the
  challenge) and cursor-paginated ledger listing.

Not built yet: the use-case layer that ties the three repositories
together in one transaction, HTTP, the SQS consumer, auth, and the Fx
wiring that connects any of it — `main.go` currently only boots and
shuts down an empty Fx app. See
ARCHITECTURE.md's "Pending" section for the full list.

## Prerequisites

- Go (see `go.mod` for the exact `go` directive)
- Docker and Docker Compose

## Local infrastructure

```sh
# Postgres, LocalStack (SQS), and Keycloak
docker compose up -d

# or just what today's code actually uses:
docker compose up -d postgres
```

Check status with `docker compose ps`; all three define healthchecks.
Tear down with `docker compose down` (add `-v` to also wipe volumes,
i.e. start from a clean database next time).

LocalStack and Keycloak are provisioned (queues auto-created, Keycloak
running in dev mode) but nothing in the codebase talks to them yet.

## Migrations

Applied via the `migrate` service in `docker-compose.yml` (the
`migrate/migrate` image) — no local `golang-migrate` install needed.

```sh
# apply all pending migrations
docker compose --profile tools run --rm migrate up

# roll back the last migration
docker compose --profile tools run --rm migrate down 1
```

See `migrations/README.md` for what each migration does and the naming
convention for new ones.

## Running the application

```sh
go run .
```

Right now this only demonstrates the Fx lifecycle bootstrap (logs
`starting`, then `stopping` on `SIGINT`/`SIGTERM`) — there's no HTTP
server or business logic wired into `main.go` yet. The actual behavior
built so far is exercised through tests, not by running the app.

## Tests

```sh
# unit tests — pure Go, no infrastructure needed
go test ./...
go test -race ./...

go vet ./...
gofmt -l .   # should print nothing
```

Integration tests live behind a build tag so `go test ./...` stays fast
and infra-free:

```sh
docker compose up -d postgres
docker compose --profile tools run --rm migrate up

go test -tags=integration -race ./internal/platform/postgres/...
```

`DATABASE_URL` overrides the default connection string, which matches
`docker-compose.yml` (`postgres://app:app@localhost:5432/jungle_gaming?sslmode=disable`).

## Project layout

```
internal/
  domain/            money, wallet, ledger, wagertransaction — pure Go
  platform/
    postgres/        pgx connection pool + repositories
migrations/          versioned SQL, applied via docker-compose's migrate service
deploy/              supporting files for LocalStack and Keycloak
```
