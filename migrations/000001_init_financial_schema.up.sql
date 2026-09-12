-- Wallets, wager transactions, and the wallet ledger — the three
-- persisted aggregates built so far (Money is a value embedded in these,
-- never its own table). Inbox/outbox are intentionally not part of this
-- migration: those domain types don't exist yet, and a table with no
-- corresponding Go type would just be dead schema.
--
-- IDs are always supplied by the application (never DEFAULT
-- gen_random_uuid()), matching the domain constructors, which take an id
-- as a parameter rather than generating one internally.

CREATE TABLE wallets (
    id                    UUID PRIMARY KEY,
    player_id             UUID NOT NULL,
    currency              CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    balance_minor_units   BIGINT NOT NULL CHECK (balance_minor_units >= 0),
    version               BIGINT NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- §6.2: (playerId, currency) identifies a unique wallet.
    UNIQUE (player_id, currency)
);

CREATE TABLE wager_transactions (
    id                                UUID PRIMARY KEY,

    -- Populated for every kind except OPENING; see chk_wagertx_metadata.
    provider_id                       TEXT,
    external_transaction_id           TEXT,
    idempotency_key                   TEXT,
    payload_hash                      TEXT,
    round_id                          TEXT,
    game_id                           TEXT,

    wallet_id                         UUID NOT NULL REFERENCES wallets(id),
    player_id                         UUID NOT NULL,

    kind                              TEXT NOT NULL
                                        CHECK (kind IN ('OPENING','BET','WIN','LOSS','REFUND','ROLLBACK')),

    amount_minor_units                BIGINT NOT NULL,
    currency                          CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),

    -- Populated only for REFUND/ROLLBACK; see chk_wagertx_reference.
    reference_external_transaction_id TEXT,
    resolved_reference_id             UUID REFERENCES wager_transactions(id),

    state                             TEXT NOT NULL
                                        CHECK (state IN ('PENDING','PENDING_REFERENCE','PROCESSED','REJECTED','FAILED')),

    -- Populated only once the transaction reaches its terminal state;
    -- see chk_wagertx_state_data.
    failure_code                      TEXT,
    resulting_balance_minor_units     BIGINT,

    created_at                        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                        TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- §7: LOSS is always exactly zero; every other kind is strictly positive.
    CONSTRAINT chk_wagertx_amount CHECK (
        (kind = 'LOSS' AND amount_minor_units = 0)
        OR (kind <> 'LOSS' AND amount_minor_units > 0)
    ),

    -- §6.3: OPENING carries none of the external metadata; every other
    -- kind carries all of it. Mirrors validateFieldsForKind in Go.
    CONSTRAINT chk_wagertx_metadata CHECK (
        (kind = 'OPENING'
            AND provider_id IS NULL AND external_transaction_id IS NULL
            AND idempotency_key IS NULL AND payload_hash IS NULL
            AND round_id IS NULL AND game_id IS NULL)
        OR
        (kind <> 'OPENING'
            AND provider_id IS NOT NULL AND external_transaction_id IS NOT NULL
            AND idempotency_key IS NOT NULL AND payload_hash IS NOT NULL
            AND round_id IS NOT NULL AND game_id IS NOT NULL)
    ),

    -- §7: a reference is required for REFUND/ROLLBACK and inapplicable
    -- otherwise.
    CONSTRAINT chk_wagertx_reference CHECK (
        (kind IN ('REFUND','ROLLBACK') AND reference_external_transaction_id IS NOT NULL)
        OR
        (kind NOT IN ('REFUND','ROLLBACK') AND reference_external_transaction_id IS NULL)
    ),

    -- §6.3: PROCESSED must carry the balance to report back (needed for
    -- idempotent replay); REJECTED/FAILED must carry a stable failureCode.
    CONSTRAINT chk_wagertx_state_data CHECK (
        (state = 'PROCESSED' AND resulting_balance_minor_units IS NOT NULL)
        OR (state IN ('REJECTED','FAILED') AND failure_code IS NOT NULL)
        OR (state IN ('PENDING','PENDING_REFERENCE'))
    ),

    -- A financial operation identified by (providerId, externalTransactionId)
    -- can never be reapplied under a different idempotency key. NULLs
    -- (OPENING rows) are not considered equal by Postgres, so this does
    -- not constrain OPENING at all.
    UNIQUE (provider_id, external_transaction_id),

    -- Supports the idempotency-key lookup the use case performs on
    -- every request, scoped per provider.
    UNIQUE (provider_id, idempotency_key)
);

-- §9: initial credit must not be duplicated — at most one OPENING per
-- wallet. A partial index rather than a table-wide UNIQUE(wallet_id),
-- since non-OPENING kinds legitimately share a wallet across many rows.
CREATE UNIQUE INDEX uq_wagertx_opening_per_wallet
    ON wager_transactions (wallet_id)
    WHERE kind = 'OPENING';

CREATE INDEX idx_wagertx_wallet_id ON wager_transactions (wallet_id);

-- §6.3: a terminal transaction must never transition again. Enforced in
-- Go (WagerTransaction.MarkX refuses from a terminal state) and mirrored
-- here so a bug or a direct SQL update can't silently violate it either.
CREATE OR REPLACE FUNCTION forbid_terminal_wagertx_update() RETURNS TRIGGER AS $$
BEGIN
    IF OLD.state IN ('PROCESSED', 'REJECTED', 'FAILED') THEN
        RAISE EXCEPTION 'wager_transactions %: cannot modify a transaction already in terminal state %', OLD.id, OLD.state;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_wagertx_terminal_immutable
    BEFORE UPDATE ON wager_transactions
    FOR EACH ROW EXECUTE FUNCTION forbid_terminal_wagertx_update();

CREATE TABLE wallet_ledger_entries (
    id                          UUID PRIMARY KEY,
    wallet_id                   UUID NOT NULL REFERENCES wallets(id),
    transaction_id              UUID NOT NULL REFERENCES wager_transactions(id),
    direction                   TEXT NOT NULL CHECK (direction IN ('DEBIT','CREDIT')),
    amount_minor_units          BIGINT NOT NULL CHECK (amount_minor_units > 0),
    currency                    CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    balance_before_minor_units  BIGINT NOT NULL CHECK (balance_before_minor_units >= 0),
    balance_after_minor_units   BIGINT NOT NULL CHECK (balance_after_minor_units >= 0),
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- §6.4: balanceAfter = balanceBefore ± amount, enforced here exactly
    -- as ledger.validateBalances enforces it in Go.
    CONSTRAINT chk_ledger_balance_math CHECK (
        (direction = 'DEBIT' AND balance_after_minor_units = balance_before_minor_units - amount_minor_units)
        OR
        (direction = 'CREDIT' AND balance_after_minor_units = balance_before_minor_units + amount_minor_units)
    ),

    -- §6.4: uniqueness of (walletId, transactionId).
    UNIQUE (wallet_id, transaction_id)
);

CREATE INDEX idx_ledger_wallet_pagination ON wallet_ledger_entries (wallet_id, created_at, id);

-- §6.4: the ledger is append-only — no edit, no delete, ever.
CREATE OR REPLACE FUNCTION forbid_ledger_mutation() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'wallet_ledger_entries is append-only: % is not permitted', TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_ledger_immutable
    BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION forbid_ledger_mutation();
