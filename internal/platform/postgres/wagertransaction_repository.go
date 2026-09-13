package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/money"
	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/wagertransaction"
	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/wallet"
)

var (
	// ErrTransactionNotFound is returned when a lookup or update finds
	// no matching row.
	ErrTransactionNotFound = errors.New("postgres: wager transaction not found")

	// ErrTransactionAlreadyExists is returned by Create on any of the
	// table's three uniqueness rules: (providerId, externalTransactionId),
	// (providerId, idempotencyKey), or a second OPENING for the same
	// wallet. The use-case layer is expected to look up an existing row
	// before inserting (that's how idempotent replay works); this is the
	// defensive fallback for a race on the insert itself.
	ErrTransactionAlreadyExists = errors.New("postgres: wager transaction already exists")
)

// foreignKeyViolation is Postgres's stable SQLSTATE code for a foreign
// key violation (23503).
const foreignKeyViolation = "23503"

const selectWagerTransactionColumns = `
	id, provider_id, external_transaction_id, idempotency_key, payload_hash,
	round_id, game_id, wallet_id, player_id, kind, amount_minor_units, currency,
	reference_external_transaction_id, resolved_reference_id, state, failure_code,
	resulting_balance_minor_units, created_at, updated_at
	FROM wager_transactions`

// WagerTransactionRepository reads and writes the wager_transactions
// table. Every method takes a Querier so the caller controls the
// transaction boundary.
type WagerTransactionRepository struct{}

// NewWagerTransactionRepository constructs a WagerTransactionRepository.
// It holds no state — the pool or transaction is supplied per call.
func NewWagerTransactionRepository() *WagerTransactionRepository {
	return &WagerTransactionRepository{}
}

// Create inserts a new PENDING transaction row (the only state
// NewExternal/NewOpening ever produce). Kind-appropriate nullability
// (OPENING carries no external metadata, only REFUND/ROLLBACK carry a
// reference) is derived from t itself, mirroring the schema's own
// chk_wagertx_metadata / chk_wagertx_reference constraints.
func (r *WagerTransactionRepository) Create(ctx context.Context, q Querier, t wagertransaction.WagerTransaction) error {
	var providerID, externalTransactionID, idempotencyKey, payloadHash, roundID, gameID *string
	if t.Kind() != wagertransaction.Opening {
		providerID = strPtr(string(t.ProviderID()))
		externalTransactionID = strPtr(string(t.ExternalTransactionID()))
		idempotencyKey = strPtr(string(t.IdempotencyKey()))
		payloadHash = strPtr(string(t.PayloadHash()))
		roundID = strPtr(string(t.RoundID()))
		gameID = strPtr(string(t.GameID()))
	}

	var referenceExternalTransactionID *string
	if t.Kind().RequiresReference() {
		referenceExternalTransactionID = strPtr(string(t.ReferenceExternalTransactionID()))
	}

	_, err := q.Exec(ctx, `
		INSERT INTO wager_transactions (
			id, provider_id, external_transaction_id, idempotency_key, payload_hash,
			round_id, game_id, wallet_id, player_id, kind, amount_minor_units, currency,
			reference_external_transaction_id, state, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		string(t.ID()), providerID, externalTransactionID, idempotencyKey, payloadHash,
		roundID, gameID, string(t.WalletID()), string(t.PlayerID()), string(t.Kind()),
		t.Money().MinorUnits(), t.Money().Currency(), referenceExternalTransactionID,
		string(t.State()), t.CreatedAt(), t.UpdatedAt(),
	)
	switch {
	case isUniqueViolation(err):
		return ErrTransactionAlreadyExists
	case isForeignKeyViolation(err):
		return ErrWalletNotFound
	default:
		return err
	}
}

// FindByID reads a transaction without locking it.
func (r *WagerTransactionRepository) FindByID(ctx context.Context, q Querier, id wagertransaction.ID) (wagertransaction.WagerTransaction, error) {
	return scanWagerTransaction(q.QueryRow(ctx, `SELECT `+selectWagerTransactionColumns+` WHERE id = $1`, string(id)))
}

// FindByIDForUpdate reads a transaction and locks its row for the rest
// of tx, so that another instance resuming the same PENDING transaction
// (per the challenge's durable-resumption requirement) serializes on
// this call instead of racing to transition it twice.
func (r *WagerTransactionRepository) FindByIDForUpdate(ctx context.Context, tx pgx.Tx, id wagertransaction.ID) (wagertransaction.WagerTransaction, error) {
	return scanWagerTransaction(tx.QueryRow(ctx, `SELECT `+selectWagerTransactionColumns+` WHERE id = $1 FOR UPDATE`, string(id)))
}

// FindByProviderAndExternalID resolves the business key
// (providerId, externalTransactionId) — used both for idempotent-replay
// lookups and for resolving a REFUND/ROLLBACK's reference (§7).
func (r *WagerTransactionRepository) FindByProviderAndExternalID(ctx context.Context, q Querier, providerID wagertransaction.ProviderID, externalTransactionID wagertransaction.ExternalTransactionID) (wagertransaction.WagerTransaction, error) {
	return scanWagerTransaction(q.QueryRow(ctx, `SELECT `+selectWagerTransactionColumns+` WHERE provider_id = $1 AND external_transaction_id = $2`, string(providerID), string(externalTransactionID)))
}

// FindByProviderAndIdempotencyKey resolves the Idempotency-Key header's
// lookup path, scoped per provider.
func (r *WagerTransactionRepository) FindByProviderAndIdempotencyKey(ctx context.Context, q Querier, providerID wagertransaction.ProviderID, idempotencyKey wagertransaction.IdempotencyKey) (wagertransaction.WagerTransaction, error) {
	return scanWagerTransaction(q.QueryRow(ctx, `SELECT `+selectWagerTransactionColumns+` WHERE provider_id = $1 AND idempotency_key = $2`, string(providerID), string(idempotencyKey)))
}

// Update persists a state transition (PENDING/PENDING_REFERENCE moving
// to PENDING_REFERENCE, PROCESSED, REJECTED, or FAILED). Call it with
// the same tx that obtained the row via FindByIDForUpdate. A concurrent
// or buggy attempt to transition an already-terminal row is rejected by
// the schema's trg_wagertx_terminal_immutable trigger, not just by the
// Go-level guard in WagerTransaction.MarkX — that error propagates as-is
// rather than being remapped to a Go sentinel, since correct application
// code is expected to check State().IsTerminal() before ever reaching
// this call.
func (r *WagerTransactionRepository) Update(ctx context.Context, tx pgx.Tx, t wagertransaction.WagerTransaction) error {
	var failureCode *string
	if t.FailureCode() != "" {
		failureCode = strPtr(string(t.FailureCode()))
	}
	var resolvedReferenceID *string
	if t.ResolvedReferenceID() != "" {
		resolvedReferenceID = strPtr(string(t.ResolvedReferenceID()))
	}
	var resultingBalanceMinorUnits *int64
	if balance, ok := t.ResultingBalance(); ok {
		v := balance.MinorUnits()
		resultingBalanceMinorUnits = &v
	}

	tag, err := tx.Exec(ctx, `
		UPDATE wager_transactions
		SET state = $1, failure_code = $2, resulting_balance_minor_units = $3,
			resolved_reference_id = $4, updated_at = $5
		WHERE id = $6`,
		string(t.State()), failureCode, resultingBalanceMinorUnits, resolvedReferenceID, t.UpdatedAt(), string(t.ID()),
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTransactionNotFound
	}
	return nil
}

func scanWagerTransaction(row pgx.Row) (wagertransaction.WagerTransaction, error) {
	var (
		id, walletID, playerID, kind, currency, state                                     string
		providerID, externalTransactionID, idempotencyKey, payloadHash                    *string
		roundID, gameID, referenceExternalTransactionID, resolvedReferenceID, failureCode *string
		amountMinorUnits                                                                  int64
		resultingBalanceMinorUnits                                                        *int64
		createdAt, updatedAt                                                              time.Time
	)

	err := row.Scan(
		&id, &providerID, &externalTransactionID, &idempotencyKey, &payloadHash,
		&roundID, &gameID, &walletID, &playerID, &kind, &amountMinorUnits, &currency,
		&referenceExternalTransactionID, &resolvedReferenceID, &state, &failureCode,
		&resultingBalanceMinorUnits, &createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return wagertransaction.WagerTransaction{}, ErrTransactionNotFound
		}
		return wagertransaction.WagerTransaction{}, err
	}

	amount, err := money.FromMinorUnits(amountMinorUnits, currency)
	if err != nil {
		return wagertransaction.WagerTransaction{}, err
	}

	var resultingBalance *money.Money
	if resultingBalanceMinorUnits != nil {
		rb, err := money.FromMinorUnits(*resultingBalanceMinorUnits, currency)
		if err != nil {
			return wagertransaction.WagerTransaction{}, err
		}
		resultingBalance = &rb
	}

	return wagertransaction.Rehydrate(wagertransaction.RehydrateInput{
		ID:                             wagertransaction.ID(id),
		ProviderID:                     wagertransaction.ProviderID(strOr(providerID)),
		ExternalTransactionID:          wagertransaction.ExternalTransactionID(strOr(externalTransactionID)),
		IdempotencyKey:                 wagertransaction.IdempotencyKey(strOr(idempotencyKey)),
		PayloadHash:                    wagertransaction.PayloadHash(strOr(payloadHash)),
		WalletID:                       wallet.ID(walletID),
		PlayerID:                       wallet.PlayerID(playerID),
		RoundID:                        wagertransaction.RoundID(strOr(roundID)),
		GameID:                         wagertransaction.GameID(strOr(gameID)),
		Kind:                           wagertransaction.Kind(kind),
		Money:                          amount,
		ReferenceExternalTransactionID: wagertransaction.ExternalTransactionID(strOr(referenceExternalTransactionID)),
		ResolvedReferenceID:            wagertransaction.ID(strOr(resolvedReferenceID)),
		State:                          wagertransaction.State(state),
		FailureCode:                    wagertransaction.FailureCode(strOr(failureCode)),
		ResultingBalance:               resultingBalance,
		CreatedAt:                      createdAt,
		UpdatedAt:                      updatedAt,
	})
}

func strPtr(s string) *string { return &s }

func strOr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation
}
