// Package ledger models WalletLedgerEntry: a single, immutable movement in
// a wallet's append-only ledger. It depends on money for amounts and on
// wallet only for the ID and Direction types, so an Entry can be built
// directly from a wallet.Movement without wallet ever depending back on
// ledger.
//
// Immutability and edit/delete protection are enforced at the schema level
// (no UPDATE/DELETE grants, or a trigger) once this type is persisted;
// this package enforces it in Go by exposing no mutating methods and no
// way to construct an Entry other than through validated constructors.
// Uniqueness of (walletId, transactionId) is likewise a database
// constraint, not something this package can enforce on its own.
package ledger

import (
	"errors"
	"fmt"
	"time"

	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/money"
	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/wallet"
)

// ID identifies a ledger entry.
type ID string

// TransactionID identifies the WagerTransaction that produced an entry.
// It is a plain string here (rather than a type imported from a
// wagertransaction package) so that ledger has no dependency on it; the
// application layer is responsible for keeping the two in sync.
type TransactionID string

var (
	// ErrInvalidID is returned when an entry id is empty.
	ErrInvalidID = errors.New("ledger: id is required")

	// ErrInvalidWalletID is returned when a wallet id is empty.
	ErrInvalidWalletID = errors.New("ledger: walletId is required")

	// ErrInvalidTransactionID is returned when a transaction id is empty.
	ErrInvalidTransactionID = errors.New("ledger: transactionId is required")

	// ErrInvalidDirection is returned when direction is neither
	// wallet.Debit nor wallet.Credit.
	ErrInvalidDirection = errors.New("ledger: direction must be DEBIT or CREDIT")

	// ErrInvalidAmount is returned when the amount is not strictly
	// positive.
	ErrInvalidAmount = errors.New("ledger: amount must be positive")

	// ErrBalanceMismatch is returned when balanceAfter does not equal
	// balanceBefore ± amount according to direction.
	ErrBalanceMismatch = errors.New("ledger: balanceAfter does not match balanceBefore and direction")
)

// Entry is a single, immutable ledger movement. The zero value is not
// valid; use New or Rehydrate.
type Entry struct {
	id            ID
	walletID      wallet.ID
	transactionID TransactionID
	direction     wallet.Direction
	amount        money.Money
	balanceBefore money.Money
	balanceAfter  money.Money
	createdAt     time.Time
}

// New constructs a ledger entry directly from a wallet.Movement, so the
// numbers recorded here are exactly the numbers the Wallet aggregate
// computed for the same operation — there is no separate transcription
// step where they could drift apart. It validates that
// balanceAfter = balanceBefore ± amount according to direction before
// allowing the entry to exist.
func New(id ID, walletID wallet.ID, transactionID TransactionID, movement wallet.Movement, now time.Time) (Entry, error) {
	if id == "" {
		return Entry{}, ErrInvalidID
	}
	if walletID == "" {
		return Entry{}, ErrInvalidWalletID
	}
	if transactionID == "" {
		return Entry{}, ErrInvalidTransactionID
	}
	if !movement.Amount.IsPositive() {
		return Entry{}, fmt.Errorf("%w: %s", ErrInvalidAmount, movement.Amount.String())
	}
	if err := validateBalances(movement.Direction, movement.Amount, movement.BalanceBefore, movement.BalanceAfter); err != nil {
		return Entry{}, err
	}

	return Entry{
		id:            id,
		walletID:      walletID,
		transactionID: transactionID,
		direction:     movement.Direction,
		amount:        movement.Amount,
		balanceBefore: movement.BalanceBefore,
		balanceAfter:  movement.BalanceAfter,
		createdAt:     now,
	}, nil
}

// Rehydrate reconstructs a persisted entry from its stored fields. No
// wallet.Movement exists once persisted, so this validates the same
// structural invariant directly: balanceAfter must equal
// balanceBefore ± amount according to direction.
func Rehydrate(id ID, walletID wallet.ID, transactionID TransactionID, direction wallet.Direction, amount, balanceBefore, balanceAfter money.Money, createdAt time.Time) (Entry, error) {
	if id == "" {
		return Entry{}, ErrInvalidID
	}
	if walletID == "" {
		return Entry{}, ErrInvalidWalletID
	}
	if transactionID == "" {
		return Entry{}, ErrInvalidTransactionID
	}
	if !amount.IsPositive() {
		return Entry{}, fmt.Errorf("%w: %s", ErrInvalidAmount, amount.String())
	}
	if err := validateBalances(direction, amount, balanceBefore, balanceAfter); err != nil {
		return Entry{}, err
	}

	return Entry{
		id:            id,
		walletID:      walletID,
		transactionID: transactionID,
		direction:     direction,
		amount:        amount,
		balanceBefore: balanceBefore,
		balanceAfter:  balanceAfter,
		createdAt:     createdAt,
	}, nil
}

func validateBalances(direction wallet.Direction, amount, before, after money.Money) error {
	var expected money.Money
	var err error
	switch direction {
	case wallet.Debit:
		expected, err = before.Sub(amount)
	case wallet.Credit:
		expected, err = before.Add(amount)
	default:
		return fmt.Errorf("%w: %q", ErrInvalidDirection, direction)
	}
	if err != nil {
		return err
	}
	if expected != after {
		return fmt.Errorf("%w: expected %s, got %s", ErrBalanceMismatch, expected.String(), after.String())
	}
	return nil
}

// ID returns the entry's identity.
func (e Entry) ID() ID { return e.id }

// WalletID returns the wallet this entry belongs to.
func (e Entry) WalletID() wallet.ID { return e.walletID }

// TransactionID returns the WagerTransaction that produced this entry.
func (e Entry) TransactionID() TransactionID { return e.transactionID }

// Direction returns whether this entry debited or credited the wallet.
func (e Entry) Direction() wallet.Direction { return e.direction }

// Amount returns the moved amount.
func (e Entry) Amount() money.Money { return e.amount }

// BalanceBefore returns the wallet balance immediately before this entry.
func (e Entry) BalanceBefore() money.Money { return e.balanceBefore }

// BalanceAfter returns the wallet balance immediately after this entry.
func (e Entry) BalanceAfter() money.Money { return e.balanceAfter }

// CreatedAt returns when this entry was created.
func (e Entry) CreatedAt() time.Time { return e.createdAt }
