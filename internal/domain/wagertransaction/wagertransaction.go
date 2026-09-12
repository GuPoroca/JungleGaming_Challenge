// Package wagertransaction models WagerTransaction: the record of a single
// wallet operation (an external BET/WIN/LOSS/REFUND/ROLLBACK, or the
// internal OPENING) and its five-state machine
// (PENDING, PENDING_REFERENCE, PROCESSED, REJECTED, FAILED). It depends on
// money for amounts and on wallet only for the ID and PlayerID types, and
// has no dependency on ledger, Fx, HTTP, or persistence libraries.
//
// This package covers construction, kind-specific amount rules, and valid
// state transitions. It deliberately does not cover: resolving a
// reference by querying storage, retry/backoff timing for
// PENDING_REFERENCE, or detecting a second successful reversal against
// the same reference — those need a repository and are use-case
// responsibilities. ValidateReversalAgreement is the one pure check this
// package can do without I/O: given two already-loaded transactions, does
// the reversal actually agree with what it claims to reverse.
package wagertransaction

import (
	"errors"
	"fmt"
	"time"

	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/money"
	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/wallet"
)

// ID identifies a WagerTransaction internally.
type ID string

// ProviderID identifies the external game provider. Empty for OPENING.
type ProviderID string

// ExternalTransactionID identifies an operation in the provider's own
// system. Empty for OPENING.
type ExternalTransactionID string

// IdempotencyKey is the client-supplied idempotency key. Empty for
// OPENING.
type IdempotencyKey string

// PayloadHash is the deterministic hash of the operation's business
// fields, computed by the caller (HTTP handler or SQS consumer) before
// construction. Empty for OPENING.
type PayloadHash string

// RoundID identifies the game round an operation belongs to. Empty for
// OPENING.
type RoundID string

// GameID identifies the game an operation belongs to. Empty for OPENING.
type GameID string

// Kind is one of the six transaction types.
type Kind string

const (
	Opening  Kind = "OPENING"
	Bet      Kind = "BET"
	Win      Kind = "WIN"
	Loss     Kind = "LOSS"
	Refund   Kind = "REFUND"
	Rollback Kind = "ROLLBACK"
)

// Valid reports whether k is one of the six recognized kinds.
func (k Kind) Valid() bool {
	switch k {
	case Opening, Bet, Win, Loss, Refund, Rollback:
		return true
	default:
		return false
	}
}

// RequiresReference reports whether k must carry a
// ReferenceExternalTransactionID (REFUND and ROLLBACK).
func (k Kind) RequiresReference() bool {
	return k == Refund || k == Rollback
}

// State is one of the five transaction states.
type State string

const (
	Pending          State = "PENDING"
	PendingReference State = "PENDING_REFERENCE"
	Processed        State = "PROCESSED"
	Rejected         State = "REJECTED"
	Failed           State = "FAILED"
)

// Valid reports whether s is one of the five recognized states.
func (s State) Valid() bool {
	switch s {
	case Pending, PendingReference, Processed, Rejected, Failed:
		return true
	default:
		return false
	}
}

// IsTerminal reports whether s is a terminal state. A terminal
// transaction must not undergo any further transition.
func (s State) IsTerminal() bool {
	return s == Processed || s == Rejected || s == Failed
}

// FailureCode is a stable, documented reason attached to a REJECTED or
// FAILED transaction. Distinct codes exist wherever the challenge
// requires distinguishing outcomes that look similar but aren't (e.g. a
// bet failing for insufficient balance vs. a reversal failing for the
// same underlying reason).
type FailureCode string

const (
	// FailureCodeInsufficientBalanceForBet is used when a BET is
	// rejected because the wallet balance is too low.
	FailureCodeInsufficientBalanceForBet FailureCode = "INSUFFICIENT_BALANCE_FOR_BET"

	// FailureCodeInsufficientBalanceForReversal is used when a REFUND or
	// ROLLBACK would debit more than the wallet's available balance.
	// Kept distinct from FailureCodeInsufficientBalanceForBet per the
	// challenge's explicit requirement that the two be distinguishable.
	FailureCodeInsufficientBalanceForReversal FailureCode = "INSUFFICIENT_BALANCE_FOR_REVERSAL"

	// FailureCodeReferenceNotFound is used when a REFUND or ROLLBACK's
	// reference never resolves within the retry budget (max attempts or
	// TTL, enforced by the pending-reference worker, not this package).
	FailureCodeReferenceNotFound FailureCode = "REFERENCE_NOT_FOUND"

	// FailureCodeReferenceNotProcessed is used when the referenced
	// transaction exists but is not in PROCESSED state (still pending,
	// or terminal without success) at the time the reversal is
	// evaluated.
	FailureCodeReferenceNotProcessed FailureCode = "REFERENCE_NOT_PROCESSED"

	// FailureCodeReferenceMismatch is used when the reversal and its
	// reference disagree on provider, player, wallet, currency, round,
	// or amount.
	FailureCodeReferenceMismatch FailureCode = "REFERENCE_MISMATCH"

	// FailureCodeDuplicateReversal is used when the reference already
	// has a successful reversal (REFUND or ROLLBACK — see
	// ARCHITECTURE.md for why both kinds count against the same limit).
	// Detecting this requires querying prior reversals and is therefore
	// applied by the use-case layer, not this package.
	FailureCodeDuplicateReversal FailureCode = "DUPLICATE_REVERSAL"
)

var (
	ErrInvalidID                    = errors.New("wagertransaction: id is required")
	ErrInvalidProviderID            = errors.New("wagertransaction: providerId is required")
	ErrInvalidExternalTransactionID = errors.New("wagertransaction: externalTransactionId is required")
	ErrInvalidIdempotencyKey        = errors.New("wagertransaction: idempotencyKey is required")
	ErrInvalidPayloadHash           = errors.New("wagertransaction: payloadHash is required")
	ErrInvalidWalletID              = errors.New("wagertransaction: walletId is required")
	ErrInvalidPlayerID              = errors.New("wagertransaction: playerId is required")
	ErrInvalidRoundID               = errors.New("wagertransaction: roundId is required")
	ErrInvalidGameID                = errors.New("wagertransaction: gameId is required")
	ErrInvalidKind                  = errors.New("wagertransaction: invalid kind")
	ErrInvalidState                 = errors.New("wagertransaction: invalid state")
	ErrOpeningNotAllowedExternally  = errors.New("wagertransaction: OPENING is not allowed for external operations")
	ErrInvalidAmountForKind         = errors.New("wagertransaction: amount is not valid for this kind")
	ErrReferenceRequired            = errors.New("wagertransaction: referenceExternalTransactionId is required for this kind")
	ErrReferenceNotApplicable       = errors.New("wagertransaction: referenceExternalTransactionId is not applicable for this kind")
	ErrTerminalTransaction          = errors.New("wagertransaction: transaction is already in a terminal state")
	ErrFailureCodeRequired          = errors.New("wagertransaction: failureCode is required")

	// ErrReferenceKindMismatch, ErrReferenceNotProcessed,
	// ErrReferenceFieldMismatch and ErrReferenceAmountMismatch are
	// returned by ValidateReversalAgreement. The use-case layer maps
	// them to FailureCodeReferenceNotProcessed or
	// FailureCodeReferenceMismatch when rejecting a reversal.
	ErrReferenceKindMismatch   = errors.New("wagertransaction: reference kind cannot be reversed by this operation kind")
	ErrReferenceNotProcessed   = errors.New("wagertransaction: reference is not in a processed state")
	ErrReferenceFieldMismatch  = errors.New("wagertransaction: reference does not match provider, player, wallet, currency or round")
	ErrReferenceAmountMismatch = errors.New("wagertransaction: reference amount does not match")
)

// ExternalInput groups the fields required to construct an external
// WagerTransaction (every kind except OPENING).
type ExternalInput struct {
	ID                             ID
	ProviderID                     ProviderID
	ExternalTransactionID          ExternalTransactionID
	IdempotencyKey                 IdempotencyKey
	PayloadHash                    PayloadHash
	WalletID                       wallet.ID
	PlayerID                       wallet.PlayerID
	RoundID                        RoundID
	GameID                         GameID
	Kind                           Kind
	Money                          money.Money
	ReferenceExternalTransactionID ExternalTransactionID // required for REFUND/ROLLBACK, empty otherwise
}

// OpeningInput groups the fields required to construct the internal
// OPENING transaction. Provider, external id, idempotency key, payload
// hash, round, game and reference don't apply to this origin, so they
// aren't parameters here at all.
type OpeningInput struct {
	ID       ID
	WalletID wallet.ID
	PlayerID wallet.PlayerID
	Money    money.Money
}

// RehydrateInput groups every persisted field needed to reconstruct a
// WagerTransaction of any kind and in any state.
type RehydrateInput struct {
	ID                             ID
	ProviderID                     ProviderID
	ExternalTransactionID          ExternalTransactionID
	IdempotencyKey                 IdempotencyKey
	PayloadHash                    PayloadHash
	WalletID                       wallet.ID
	PlayerID                       wallet.PlayerID
	RoundID                        RoundID
	GameID                         GameID
	Kind                           Kind
	Money                          money.Money
	ReferenceExternalTransactionID ExternalTransactionID
	ResolvedReferenceID            ID
	State                          State
	FailureCode                    FailureCode
	ResultingBalance               *money.Money
	CreatedAt                      time.Time
	UpdatedAt                      time.Time
}

// WagerTransaction is the record of one wallet operation and its
// processing state. The zero value is not valid; use NewExternal,
// NewOpening, or Rehydrate.
type WagerTransaction struct {
	id                             ID
	providerID                     ProviderID
	externalTransactionID          ExternalTransactionID
	idempotencyKey                 IdempotencyKey
	payloadHash                    PayloadHash
	walletID                       wallet.ID
	playerID                       wallet.PlayerID
	roundID                        RoundID
	gameID                         GameID
	kind                           Kind
	amount                         money.Money
	referenceExternalTransactionID ExternalTransactionID
	resolvedReferenceID            ID
	state                          State
	failureCode                    FailureCode
	resultingBalance               *money.Money
	createdAt                      time.Time
	updatedAt                      time.Time
}

// NewExternal constructs a PENDING transaction for an HTTP- or
// SQS-originated operation. It rejects Kind Opening — that origin must
// use NewOpening — and validates every field the external contract
// requires for the given kind, plus the kind's amount rule (§7): LOSS
// requires exactly zero, every other kind requires a positive amount.
func NewExternal(input ExternalInput, now time.Time) (WagerTransaction, error) {
	if input.ID == "" {
		return WagerTransaction{}, ErrInvalidID
	}
	if input.Kind == Opening {
		return WagerTransaction{}, ErrOpeningNotAllowedExternally
	}
	if !input.Kind.Valid() {
		return WagerTransaction{}, fmt.Errorf("%w: %q", ErrInvalidKind, input.Kind)
	}
	if input.WalletID == "" {
		return WagerTransaction{}, ErrInvalidWalletID
	}
	if input.PlayerID == "" {
		return WagerTransaction{}, ErrInvalidPlayerID
	}
	if err := validateFieldsForKind(input.Kind, input.ProviderID, input.ExternalTransactionID, input.IdempotencyKey, input.PayloadHash, input.RoundID, input.GameID, input.ReferenceExternalTransactionID); err != nil {
		return WagerTransaction{}, err
	}
	if err := validateAmountForKind(input.Kind, input.Money); err != nil {
		return WagerTransaction{}, err
	}

	return WagerTransaction{
		id:                             input.ID,
		providerID:                     input.ProviderID,
		externalTransactionID:          input.ExternalTransactionID,
		idempotencyKey:                 input.IdempotencyKey,
		payloadHash:                    input.PayloadHash,
		walletID:                       input.WalletID,
		playerID:                       input.PlayerID,
		roundID:                        input.RoundID,
		gameID:                         input.GameID,
		kind:                           input.Kind,
		amount:                         input.Money,
		referenceExternalTransactionID: input.ReferenceExternalTransactionID,
		state:                          Pending,
		createdAt:                      now,
		updatedAt:                      now,
	}, nil
}

// NewOpening constructs a PENDING internal OPENING transaction. Per §9,
// callers should only invoke this for a positive initial balance — a
// zero initial balance creates no OPENING transaction, ledger entry, or
// event at all — and NewOpening enforces that positivity so it can't be
// misused to represent a no-op opening.
func NewOpening(input OpeningInput, now time.Time) (WagerTransaction, error) {
	if input.ID == "" {
		return WagerTransaction{}, ErrInvalidID
	}
	if input.WalletID == "" {
		return WagerTransaction{}, ErrInvalidWalletID
	}
	if input.PlayerID == "" {
		return WagerTransaction{}, ErrInvalidPlayerID
	}
	if err := validateAmountForKind(Opening, input.Money); err != nil {
		return WagerTransaction{}, err
	}

	return WagerTransaction{
		id:        input.ID,
		walletID:  input.WalletID,
		playerID:  input.PlayerID,
		kind:      Opening,
		amount:    input.Money,
		state:     Pending,
		createdAt: now,
		updatedAt: now,
	}, nil
}

// Rehydrate reconstructs a persisted transaction in any state, without
// reapplying any transition or event emission that produced it. It
// re-validates structural consistency (recognized kind/state, fields
// appropriate to internal vs. external origin, and that terminal states
// carry the data they require) as a defense against corrupted storage.
func Rehydrate(input RehydrateInput) (WagerTransaction, error) {
	if input.ID == "" {
		return WagerTransaction{}, ErrInvalidID
	}
	if input.WalletID == "" {
		return WagerTransaction{}, ErrInvalidWalletID
	}
	if input.PlayerID == "" {
		return WagerTransaction{}, ErrInvalidPlayerID
	}
	if !input.Kind.Valid() {
		return WagerTransaction{}, fmt.Errorf("%w: %q", ErrInvalidKind, input.Kind)
	}
	if !input.State.Valid() {
		return WagerTransaction{}, fmt.Errorf("%w: %q", ErrInvalidState, input.State)
	}
	if err := validateFieldsForKind(input.Kind, input.ProviderID, input.ExternalTransactionID, input.IdempotencyKey, input.PayloadHash, input.RoundID, input.GameID, input.ReferenceExternalTransactionID); err != nil {
		return WagerTransaction{}, err
	}
	if err := validateAmountForKind(input.Kind, input.Money); err != nil {
		return WagerTransaction{}, err
	}
	if input.State == Processed && input.ResultingBalance == nil {
		return WagerTransaction{}, fmt.Errorf("%w: PROCESSED requires a resulting balance", ErrInvalidState)
	}
	if (input.State == Rejected || input.State == Failed) && input.FailureCode == "" {
		return WagerTransaction{}, fmt.Errorf("%w: %s requires a failureCode", ErrInvalidState, input.State)
	}

	return WagerTransaction{
		id:                             input.ID,
		providerID:                     input.ProviderID,
		externalTransactionID:          input.ExternalTransactionID,
		idempotencyKey:                 input.IdempotencyKey,
		payloadHash:                    input.PayloadHash,
		walletID:                       input.WalletID,
		playerID:                       input.PlayerID,
		roundID:                        input.RoundID,
		gameID:                         input.GameID,
		kind:                           input.Kind,
		amount:                         input.Money,
		referenceExternalTransactionID: input.ReferenceExternalTransactionID,
		resolvedReferenceID:            input.ResolvedReferenceID,
		state:                          input.State,
		failureCode:                    input.FailureCode,
		resultingBalance:               input.ResultingBalance,
		createdAt:                      input.CreatedAt,
		updatedAt:                      input.UpdatedAt,
	}, nil
}

// validateFieldsForKind enforces which metadata fields apply to a kind:
// OPENING must carry none of the external metadata; every other kind
// must carry all of it, plus a reference if and only if the kind is a
// reversal.
func validateFieldsForKind(kind Kind, providerID ProviderID, externalTransactionID ExternalTransactionID, idempotencyKey IdempotencyKey, payloadHash PayloadHash, roundID RoundID, gameID GameID, referenceExternalTransactionID ExternalTransactionID) error {
	if kind == Opening {
		if providerID != "" || externalTransactionID != "" || idempotencyKey != "" || payloadHash != "" || roundID != "" || gameID != "" || referenceExternalTransactionID != "" {
			return fmt.Errorf("%w: OPENING must not carry external metadata", ErrInvalidKind)
		}
		return nil
	}
	if providerID == "" {
		return ErrInvalidProviderID
	}
	if externalTransactionID == "" {
		return ErrInvalidExternalTransactionID
	}
	if idempotencyKey == "" {
		return ErrInvalidIdempotencyKey
	}
	if payloadHash == "" {
		return ErrInvalidPayloadHash
	}
	if roundID == "" {
		return ErrInvalidRoundID
	}
	if gameID == "" {
		return ErrInvalidGameID
	}
	if kind.RequiresReference() {
		if referenceExternalTransactionID == "" {
			return ErrReferenceRequired
		}
		return nil
	}
	if referenceExternalTransactionID != "" {
		return ErrReferenceNotApplicable
	}
	return nil
}

// validateAmountForKind enforces §7's amount rule: LOSS must be exactly
// zero; every other kind (including OPENING) must be strictly positive.
func validateAmountForKind(kind Kind, amount money.Money) error {
	if kind == Loss {
		if !amount.IsZero() {
			return fmt.Errorf("%w: LOSS requires a zero amount, got %s", ErrInvalidAmountForKind, amount.String())
		}
		return nil
	}
	if !amount.IsPositive() {
		return fmt.Errorf("%w: %s requires a positive amount, got %s", ErrInvalidAmountForKind, kind, amount.String())
	}
	return nil
}

// MarkPendingReference transitions PENDING to PENDING_REFERENCE. Only
// reachable from PENDING, and only for a kind that actually uses a
// reference (REFUND or ROLLBACK).
func (t WagerTransaction) MarkPendingReference(now time.Time) (WagerTransaction, error) {
	if t.state.IsTerminal() {
		return WagerTransaction{}, fmt.Errorf("%w: from %s", ErrTerminalTransaction, t.state)
	}
	if t.state != Pending {
		return WagerTransaction{}, fmt.Errorf("%w: PENDING_REFERENCE only reachable from PENDING, was %s", ErrInvalidState, t.state)
	}
	if !t.kind.RequiresReference() {
		return WagerTransaction{}, fmt.Errorf("%w: %s does not use a reference", ErrInvalidState, t.kind)
	}
	next := t
	next.state = PendingReference
	next.updatedAt = now
	return next, nil
}

// MarkProcessed transitions PENDING or PENDING_REFERENCE to the terminal
// PROCESSED state. resultingBalance is the wallet balance to report back
// for this transaction — including on future idempotent replays — and
// must share the transaction's currency. resolvedReferenceID may be
// empty for kinds that don't use a reference.
func (t WagerTransaction) MarkProcessed(resultingBalance money.Money, resolvedReferenceID ID, now time.Time) (WagerTransaction, error) {
	if t.state.IsTerminal() {
		return WagerTransaction{}, fmt.Errorf("%w: from %s", ErrTerminalTransaction, t.state)
	}
	if resultingBalance.Currency() != t.amount.Currency() {
		return WagerTransaction{}, fmt.Errorf("%w: resulting balance is %s, transaction is %s", money.ErrCurrencyMismatch, resultingBalance.Currency(), t.amount.Currency())
	}
	next := t
	next.state = Processed
	next.resultingBalance = &resultingBalance
	if resolvedReferenceID != "" {
		next.resolvedReferenceID = resolvedReferenceID
	}
	next.updatedAt = now
	return next, nil
}

// MarkRejected transitions PENDING or PENDING_REFERENCE to the terminal
// REJECTED state with a stable failureCode.
func (t WagerTransaction) MarkRejected(code FailureCode, now time.Time) (WagerTransaction, error) {
	if t.state.IsTerminal() {
		return WagerTransaction{}, fmt.Errorf("%w: from %s", ErrTerminalTransaction, t.state)
	}
	if code == "" {
		return WagerTransaction{}, ErrFailureCodeRequired
	}
	next := t
	next.state = Rejected
	next.failureCode = code
	next.updatedAt = now
	return next, nil
}

// MarkFailed transitions PENDING or PENDING_REFERENCE to the terminal
// FAILED state with a stable failureCode, for permanent infrastructure
// failures kept for audit rather than a transient error worth retrying.
func (t WagerTransaction) MarkFailed(code FailureCode, now time.Time) (WagerTransaction, error) {
	if t.state.IsTerminal() {
		return WagerTransaction{}, fmt.Errorf("%w: from %s", ErrTerminalTransaction, t.state)
	}
	if code == "" {
		return WagerTransaction{}, ErrFailureCodeRequired
	}
	next := t
	next.state = Failed
	next.failureCode = code
	next.updatedAt = now
	return next, nil
}

// ValidateReversalAgreement checks that reversal (a REFUND or ROLLBACK)
// actually agrees with referenced, the transaction it names via
// ReferenceExternalTransactionID: referenced must be a kind reversal can
// reverse (REFUND only reverses BET; ROLLBACK reverses BET, WIN, or
// REFUND), must be PROCESSED, and both must agree on provider, player,
// wallet, currency, round, and amount. This is the one reference check
// this package can do without I/O — resolving
// ReferenceExternalTransactionID to a referenced transaction in the first
// place, and checking for a prior successful reversal, both require a
// repository and belong to the use-case layer.
func ValidateReversalAgreement(reversal, referenced WagerTransaction) error {
	switch reversal.kind {
	case Refund:
		if referenced.kind != Bet {
			return fmt.Errorf("%w: REFUND must reference a BET, got %s", ErrReferenceKindMismatch, referenced.kind)
		}
	case Rollback:
		switch referenced.kind {
		case Bet, Win, Refund:
		default:
			return fmt.Errorf("%w: ROLLBACK cannot reference %s", ErrReferenceKindMismatch, referenced.kind)
		}
	default:
		return fmt.Errorf("wagertransaction: %s is not a reversal kind", reversal.kind)
	}

	if referenced.state != Processed {
		return fmt.Errorf("%w: reference is %s", ErrReferenceNotProcessed, referenced.state)
	}

	if reversal.providerID != referenced.providerID ||
		reversal.playerID != referenced.playerID ||
		reversal.walletID != referenced.walletID ||
		reversal.amount.Currency() != referenced.amount.Currency() ||
		reversal.roundID != referenced.roundID {
		return ErrReferenceFieldMismatch
	}

	cmp, err := reversal.amount.Compare(referenced.amount)
	if err != nil {
		return err
	}
	if cmp != 0 {
		return ErrReferenceAmountMismatch
	}
	return nil
}

func (t WagerTransaction) ID() ID                 { return t.id }
func (t WagerTransaction) ProviderID() ProviderID { return t.providerID }
func (t WagerTransaction) ExternalTransactionID() ExternalTransactionID {
	return t.externalTransactionID
}
func (t WagerTransaction) IdempotencyKey() IdempotencyKey { return t.idempotencyKey }
func (t WagerTransaction) PayloadHash() PayloadHash       { return t.payloadHash }
func (t WagerTransaction) WalletID() wallet.ID            { return t.walletID }
func (t WagerTransaction) PlayerID() wallet.PlayerID      { return t.playerID }
func (t WagerTransaction) RoundID() RoundID               { return t.roundID }
func (t WagerTransaction) GameID() GameID                 { return t.gameID }
func (t WagerTransaction) Kind() Kind                     { return t.kind }
func (t WagerTransaction) Money() money.Money             { return t.amount }
func (t WagerTransaction) ReferenceExternalTransactionID() ExternalTransactionID {
	return t.referenceExternalTransactionID
}
func (t WagerTransaction) ResolvedReferenceID() ID  { return t.resolvedReferenceID }
func (t WagerTransaction) State() State             { return t.state }
func (t WagerTransaction) FailureCode() FailureCode { return t.failureCode }

// ResultingBalance returns the balance recorded for a PROCESSED
// transaction, and false if none has been recorded yet.
func (t WagerTransaction) ResultingBalance() (money.Money, bool) {
	if t.resultingBalance == nil {
		return money.Money{}, false
	}
	return *t.resultingBalance, true
}

func (t WagerTransaction) CreatedAt() time.Time { return t.createdAt }
func (t WagerTransaction) UpdatedAt() time.Time { return t.updatedAt }
