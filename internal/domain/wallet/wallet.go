// Package wallet models the Wallet aggregate: the root of the financial
// domain, holding identity, player, balance, and version. It depends only
// on the money package — never on Fx, HTTP, or persistence libraries.
//
// Wallet is deliberately independent of the ledger package: Debit and
// Credit return a Movement describing the balance change instead of
// constructing a ledger entry themselves. The caller (the application
// layer, inside the same SQL transaction as the balance update) is
// responsible for turning that Movement into a persisted, validated
// WalletLedgerEntry. This keeps Wallet's only dependency Money, and keeps
// the balanceAfter = balanceBefore ± money check that ledger entries
// require in exactly one place: the ledger package itself.
package wallet

import (
	"errors"
	"fmt"
	"time"

	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/money"
)

// ID identifies a wallet.
type ID string

// PlayerID identifies the player a wallet belongs to. The pair
// (PlayerID, currency) uniquely identifies a wallet; that uniqueness is
// enforced by the persistence schema, not by this type.
type PlayerID string

// Direction is the sign of a balance movement.
type Direction string

const (
	Debit  Direction = "DEBIT"
	Credit Direction = "CREDIT"
)

var (
	// ErrInvalidID is returned when a wallet id is empty.
	ErrInvalidID = errors.New("wallet: id is required")

	// ErrInvalidPlayerID is returned when a player id is empty.
	ErrInvalidPlayerID = errors.New("wallet: playerId is required")

	// ErrInvalidBalance is returned when a balance passed to New or
	// Rehydrate is negative.
	ErrInvalidBalance = errors.New("wallet: balance cannot be negative")

	// ErrInvalidVersion is returned by Rehydrate when the persisted
	// version is less than 1.
	ErrInvalidVersion = errors.New("wallet: version must be >= 1")

	// ErrInvalidAmount is returned by Debit or Credit when the amount is
	// not strictly positive.
	ErrInvalidAmount = errors.New("wallet: amount must be positive")

	// ErrInsufficientBalance is returned by Debit when subtracting the
	// amount would take the balance below zero.
	ErrInsufficientBalance = errors.New("wallet: insufficient balance")
)

// Movement describes the effect of a single Debit or Credit call: the
// direction, the amount moved, and the balance immediately before and
// after. It carries exactly the fields a WalletLedgerEntry needs, without
// this package depending on the ledger package.
type Movement struct {
	Direction     Direction
	Amount        money.Money
	BalanceBefore money.Money
	BalanceAfter  money.Money
}

// Wallet is the aggregate root for a player's balance in one currency.
// The zero value is not valid; use New or Rehydrate.
type Wallet struct {
	id        ID
	playerID  PlayerID
	balance   money.Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

// New creates a wallet with an initial balance. Version starts at 1
// regardless of the initial balance; only a balance-changing operation
// after creation increments it.
func New(id ID, playerID PlayerID, initialBalance money.Money, now time.Time) (Wallet, error) {
	if id == "" {
		return Wallet{}, ErrInvalidID
	}
	if playerID == "" {
		return Wallet{}, ErrInvalidPlayerID
	}
	if initialBalance.IsNegative() {
		return Wallet{}, ErrInvalidBalance
	}
	return Wallet{
		id:        id,
		playerID:  playerID,
		balance:   initialBalance,
		version:   1,
		createdAt: now,
		updatedAt: now,
	}, nil
}

// Rehydrate reconstructs a wallet from persisted state. It does not
// reapply any movement, transition, or event emission that produced that
// state — it only validates that the persisted values are internally
// consistent.
func Rehydrate(id ID, playerID PlayerID, balance money.Money, version int64, createdAt, updatedAt time.Time) (Wallet, error) {
	if id == "" {
		return Wallet{}, ErrInvalidID
	}
	if playerID == "" {
		return Wallet{}, ErrInvalidPlayerID
	}
	if balance.IsNegative() {
		return Wallet{}, ErrInvalidBalance
	}
	if version < 1 {
		return Wallet{}, ErrInvalidVersion
	}
	return Wallet{
		id:        id,
		playerID:  playerID,
		balance:   balance,
		version:   version,
		createdAt: createdAt,
		updatedAt: updatedAt,
	}, nil
}

// Debit subtracts amount from the balance and returns the resulting
// wallet and the Movement describing the change. It fails if amount is
// not strictly positive, if its currency does not match the wallet's, or
// if the resulting balance would be negative: debits can never push a
// wallet below zero, independent of whatever concurrency control the
// persistence layer applies around this call.
func (w Wallet) Debit(amount money.Money, now time.Time) (Wallet, Movement, error) {
	if !amount.IsPositive() {
		return Wallet{}, Movement{}, fmt.Errorf("%w: %s", ErrInvalidAmount, amount.String())
	}
	newBalance, err := w.balance.Sub(amount)
	if err != nil {
		return Wallet{}, Movement{}, err
	}
	if newBalance.IsNegative() {
		return Wallet{}, Movement{}, fmt.Errorf("%w: balance %s, requested %s", ErrInsufficientBalance, w.balance.String(), amount.String())
	}

	movement := Movement{
		Direction:     Debit,
		Amount:        amount,
		BalanceBefore: w.balance,
		BalanceAfter:  newBalance,
	}
	next := w
	next.balance = newBalance
	next.version = w.version + 1
	next.updatedAt = now
	return next, movement, nil
}

// Credit adds amount to the balance and returns the resulting wallet and
// the Movement describing the change. It fails if amount is not strictly
// positive or if its currency does not match the wallet's.
func (w Wallet) Credit(amount money.Money, now time.Time) (Wallet, Movement, error) {
	if !amount.IsPositive() {
		return Wallet{}, Movement{}, fmt.Errorf("%w: %s", ErrInvalidAmount, amount.String())
	}
	newBalance, err := w.balance.Add(amount)
	if err != nil {
		return Wallet{}, Movement{}, err
	}

	movement := Movement{
		Direction:     Credit,
		Amount:        amount,
		BalanceBefore: w.balance,
		BalanceAfter:  newBalance,
	}
	next := w
	next.balance = newBalance
	next.version = w.version + 1
	next.updatedAt = now
	return next, movement, nil
}

// ID returns the wallet's identity.
func (w Wallet) ID() ID { return w.id }

// PlayerID returns the owning player's identity.
func (w Wallet) PlayerID() PlayerID { return w.playerID }

// Balance returns the current balance.
func (w Wallet) Balance() money.Money { return w.balance }

// Currency returns the wallet's currency, derived from its balance so
// there is exactly one source of truth for it.
func (w Wallet) Currency() string { return w.balance.Currency() }

// Version returns the optimistic-concurrency version. It starts at 1 and
// increments by exactly 1 on every successful Debit or Credit.
func (w Wallet) Version() int64 { return w.version }

// CreatedAt returns the creation timestamp.
func (w Wallet) CreatedAt() time.Time { return w.createdAt }

// UpdatedAt returns the timestamp of the last balance-changing operation,
// or the creation timestamp if none has happened yet.
func (w Wallet) UpdatedAt() time.Time { return w.updatedAt }
