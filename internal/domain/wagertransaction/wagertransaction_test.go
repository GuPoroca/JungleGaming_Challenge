package wagertransaction

import (
	"errors"
	"testing"
	"time"

	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/money"
	"github.com/gustavoporoca/jungle-gaming-challenge/internal/domain/wallet"
)

func walletFrom(id string) wallet.ID       { return wallet.ID(id) }
func playerFrom(id string) wallet.PlayerID { return wallet.PlayerID(id) }

var fixedNow = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func mustMoney(t *testing.T, amount, currency string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, currency)
	if err != nil {
		t.Fatalf("money.Parse(%q, %q) unexpected error: %v", amount, currency, err)
	}
	return m
}

func validBetInput(t *testing.T) ExternalInput {
	t.Helper()
	return ExternalInput{
		ID:                    "tx-1",
		ProviderID:            "provider-a",
		ExternalTransactionID: "ext-1",
		IdempotencyKey:        "provider-a:ext-1",
		PayloadHash:           "hash-1",
		WalletID:              "wallet-1",
		PlayerID:              "player-1",
		RoundID:               "round-1",
		GameID:                "game-1",
		Kind:                  Bet,
		Money:                 mustMoney(t, "25.00", "BRL"),
	}
}

func TestKind_Valid(t *testing.T) {
	valid := []Kind{Opening, Bet, Win, Loss, Refund, Rollback}
	for _, k := range valid {
		if !k.Valid() {
			t.Errorf("Kind(%q).Valid() = false, want true", k)
		}
	}
	if Kind("BOGUS").Valid() {
		t.Errorf("Kind(BOGUS).Valid() = true, want false")
	}
}

func TestKind_RequiresReference(t *testing.T) {
	if !Refund.RequiresReference() || !Rollback.RequiresReference() {
		t.Errorf("REFUND/ROLLBACK should require a reference")
	}
	for _, k := range []Kind{Opening, Bet, Win, Loss} {
		if k.RequiresReference() {
			t.Errorf("%s should not require a reference", k)
		}
	}
}

func TestState_IsTerminal(t *testing.T) {
	terminal := []State{Processed, Rejected, Failed}
	for _, s := range terminal {
		if !s.IsTerminal() {
			t.Errorf("State(%q).IsTerminal() = false, want true", s)
		}
	}
	nonTerminal := []State{Pending, PendingReference}
	for _, s := range nonTerminal {
		if s.IsTerminal() {
			t.Errorf("State(%q).IsTerminal() = true, want false", s)
		}
	}
}

func TestNewExternal_ValidBet(t *testing.T) {
	tx, err := NewExternal(validBetInput(t), fixedNow)
	if err != nil {
		t.Fatalf("NewExternal unexpected error: %v", err)
	}
	if tx.State() != Pending {
		t.Errorf("State() = %q, want PENDING", tx.State())
	}
	if tx.Kind() != Bet {
		t.Errorf("Kind() = %q, want BET", tx.Kind())
	}
	if tx.CreatedAt() != fixedNow || tx.UpdatedAt() != fixedNow {
		t.Errorf("timestamps = (%v, %v), want (%v, %v)", tx.CreatedAt(), tx.UpdatedAt(), fixedNow, fixedNow)
	}
	if _, ok := tx.ResultingBalance(); ok {
		t.Errorf("ResultingBalance() ok = true for a fresh PENDING transaction")
	}
}

func TestNewExternal_ValidLossRequiresZero(t *testing.T) {
	input := validBetInput(t)
	input.Kind = Loss
	input.Money, _ = money.Zero("BRL")

	tx, err := NewExternal(input, fixedNow)
	if err != nil {
		t.Fatalf("NewExternal(LOSS, zero) unexpected error: %v", err)
	}
	if !tx.Money().IsZero() {
		t.Errorf("LOSS money = %s, want 0.00", tx.Money().String())
	}
}

func TestNewExternal_LossWithPositiveAmountRejected(t *testing.T) {
	input := validBetInput(t)
	input.Kind = Loss
	// Money left at the fixture's 25.00 — invalid for LOSS.

	if _, err := NewExternal(input, fixedNow); !errors.Is(err, ErrInvalidAmountForKind) {
		t.Fatalf("NewExternal(LOSS, positive) error = %v, want ErrInvalidAmountForKind", err)
	}
}

func TestNewExternal_ZeroAmountRejectedForBet(t *testing.T) {
	input := validBetInput(t)
	input.Money, _ = money.Zero("BRL")

	if _, err := NewExternal(input, fixedNow); !errors.Is(err, ErrInvalidAmountForKind) {
		t.Fatalf("NewExternal(BET, zero) error = %v, want ErrInvalidAmountForKind", err)
	}
}

func TestNewExternal_OpeningRejected(t *testing.T) {
	input := validBetInput(t)
	input.Kind = Opening

	if _, err := NewExternal(input, fixedNow); !errors.Is(err, ErrOpeningNotAllowedExternally) {
		t.Fatalf("NewExternal(OPENING) error = %v, want ErrOpeningNotAllowedExternally", err)
	}
}

func TestNewExternal_RefundRequiresReference(t *testing.T) {
	input := validBetInput(t)
	input.Kind = Refund
	// ReferenceExternalTransactionID left empty.

	if _, err := NewExternal(input, fixedNow); !errors.Is(err, ErrReferenceRequired) {
		t.Fatalf("NewExternal(REFUND, no reference) error = %v, want ErrReferenceRequired", err)
	}
}

func TestNewExternal_RollbackRequiresReference(t *testing.T) {
	input := validBetInput(t)
	input.Kind = Rollback

	if _, err := NewExternal(input, fixedNow); !errors.Is(err, ErrReferenceRequired) {
		t.Fatalf("NewExternal(ROLLBACK, no reference) error = %v, want ErrReferenceRequired", err)
	}
}

func TestNewExternal_ReferenceNotApplicableForBet(t *testing.T) {
	input := validBetInput(t)
	input.ReferenceExternalTransactionID = "ext-0"

	if _, err := NewExternal(input, fixedNow); !errors.Is(err, ErrReferenceNotApplicable) {
		t.Fatalf("NewExternal(BET, with reference) error = %v, want ErrReferenceNotApplicable", err)
	}
}

func TestNewExternal_ValidRefundWithReference(t *testing.T) {
	input := validBetInput(t)
	input.Kind = Refund
	input.ReferenceExternalTransactionID = "ext-0"

	tx, err := NewExternal(input, fixedNow)
	if err != nil {
		t.Fatalf("NewExternal(REFUND) unexpected error: %v", err)
	}
	if tx.ReferenceExternalTransactionID() != "ext-0" {
		t.Errorf("ReferenceExternalTransactionID() = %q, want ext-0", tx.ReferenceExternalTransactionID())
	}
}

func TestNewExternal_MissingRequiredFields(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*ExternalInput)
		wantErr error
	}{
		{"id", func(i *ExternalInput) { i.ID = "" }, ErrInvalidID},
		{"providerId", func(i *ExternalInput) { i.ProviderID = "" }, ErrInvalidProviderID},
		{"externalTransactionId", func(i *ExternalInput) { i.ExternalTransactionID = "" }, ErrInvalidExternalTransactionID},
		{"idempotencyKey", func(i *ExternalInput) { i.IdempotencyKey = "" }, ErrInvalidIdempotencyKey},
		{"payloadHash", func(i *ExternalInput) { i.PayloadHash = "" }, ErrInvalidPayloadHash},
		{"walletId", func(i *ExternalInput) { i.WalletID = "" }, ErrInvalidWalletID},
		{"playerId", func(i *ExternalInput) { i.PlayerID = "" }, ErrInvalidPlayerID},
		{"roundId", func(i *ExternalInput) { i.RoundID = "" }, ErrInvalidRoundID},
		{"gameId", func(i *ExternalInput) { i.GameID = "" }, ErrInvalidGameID},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			input := validBetInput(t)
			c.mutate(&input)
			if _, err := NewExternal(input, fixedNow); !errors.Is(err, c.wantErr) {
				t.Errorf("NewExternal(missing %s) error = %v, want %v", c.name, err, c.wantErr)
			}
		})
	}
}

func TestNewOpening_Valid(t *testing.T) {
	tx, err := NewOpening(OpeningInput{
		ID:       "tx-open-1",
		WalletID: "wallet-1",
		PlayerID: "player-1",
		Money:    mustMoney(t, "1000.00", "BRL"),
	}, fixedNow)
	if err != nil {
		t.Fatalf("NewOpening unexpected error: %v", err)
	}
	if tx.Kind() != Opening {
		t.Errorf("Kind() = %q, want OPENING", tx.Kind())
	}
	if tx.ProviderID() != "" || tx.ExternalTransactionID() != "" || tx.RoundID() != "" || tx.GameID() != "" {
		t.Errorf("OPENING must not carry external metadata: %+v", tx)
	}
}

func TestNewOpening_ZeroAmountRejected(t *testing.T) {
	zero, _ := money.Zero("BRL")
	_, err := NewOpening(OpeningInput{
		ID:       "tx-open-1",
		WalletID: "wallet-1",
		PlayerID: "player-1",
		Money:    zero,
	}, fixedNow)
	if !errors.Is(err, ErrInvalidAmountForKind) {
		t.Fatalf("NewOpening(zero) error = %v, want ErrInvalidAmountForKind", err)
	}
}

func TestNewOpening_MissingFields(t *testing.T) {
	base := OpeningInput{ID: "tx-open-1", WalletID: "wallet-1", PlayerID: "player-1", Money: mustMoney(t, "10.00", "BRL")}

	noID := base
	noID.ID = ""
	if _, err := NewOpening(noID, fixedNow); !errors.Is(err, ErrInvalidID) {
		t.Errorf("NewOpening(no id) error = %v, want ErrInvalidID", err)
	}

	noWallet := base
	noWallet.WalletID = ""
	if _, err := NewOpening(noWallet, fixedNow); !errors.Is(err, ErrInvalidWalletID) {
		t.Errorf("NewOpening(no walletId) error = %v, want ErrInvalidWalletID", err)
	}

	noPlayer := base
	noPlayer.PlayerID = ""
	if _, err := NewOpening(noPlayer, fixedNow); !errors.Is(err, ErrInvalidPlayerID) {
		t.Errorf("NewOpening(no playerId) error = %v, want ErrInvalidPlayerID", err)
	}
}

func validRehydrateInput(t *testing.T) RehydrateInput {
	t.Helper()
	return RehydrateInput{
		ID:                    "tx-1",
		ProviderID:            "provider-a",
		ExternalTransactionID: "ext-1",
		IdempotencyKey:        "provider-a:ext-1",
		PayloadHash:           "hash-1",
		WalletID:              "wallet-1",
		PlayerID:              "player-1",
		RoundID:               "round-1",
		GameID:                "game-1",
		Kind:                  Bet,
		Money:                 mustMoney(t, "25.00", "BRL"),
		State:                 Pending,
		CreatedAt:             fixedNow,
		UpdatedAt:             fixedNow,
	}
}

func TestRehydrate_ValidPending(t *testing.T) {
	tx, err := Rehydrate(validRehydrateInput(t))
	if err != nil {
		t.Fatalf("Rehydrate unexpected error: %v", err)
	}
	if tx.State() != Pending {
		t.Errorf("State() = %q, want PENDING", tx.State())
	}
}

func TestRehydrate_ValidProcessed(t *testing.T) {
	input := validRehydrateInput(t)
	input.State = Processed
	balance := mustMoney(t, "75.00", "BRL")
	input.ResultingBalance = &balance

	tx, err := Rehydrate(input)
	if err != nil {
		t.Fatalf("Rehydrate(PROCESSED) unexpected error: %v", err)
	}
	got, ok := tx.ResultingBalance()
	if !ok || got.String() != "75.00" {
		t.Errorf("ResultingBalance() = (%s, %v), want (75.00, true)", got.String(), ok)
	}
}

func TestRehydrate_ProcessedWithoutResultingBalanceRejected(t *testing.T) {
	input := validRehydrateInput(t)
	input.State = Processed

	if _, err := Rehydrate(input); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Rehydrate(PROCESSED, no balance) error = %v, want ErrInvalidState", err)
	}
}

func TestRehydrate_RejectedWithoutFailureCodeRejected(t *testing.T) {
	input := validRehydrateInput(t)
	input.State = Rejected

	if _, err := Rehydrate(input); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Rehydrate(REJECTED, no failureCode) error = %v, want ErrInvalidState", err)
	}
}

func TestRehydrate_OpeningWithExternalMetadataRejected(t *testing.T) {
	input := validRehydrateInput(t)
	input.Kind = Opening
	// ProviderID etc. left populated from the fixture — invalid for OPENING.

	if _, err := Rehydrate(input); !errors.Is(err, ErrInvalidKind) {
		t.Fatalf("Rehydrate(OPENING with metadata) error = %v, want ErrInvalidKind", err)
	}
}

func TestRehydrate_InvalidKindOrState(t *testing.T) {
	badKind := validRehydrateInput(t)
	badKind.Kind = "BOGUS"
	if _, err := Rehydrate(badKind); !errors.Is(err, ErrInvalidKind) {
		t.Errorf("Rehydrate(bad kind) error = %v, want ErrInvalidKind", err)
	}

	badState := validRehydrateInput(t)
	badState.State = "BOGUS"
	if _, err := Rehydrate(badState); !errors.Is(err, ErrInvalidState) {
		t.Errorf("Rehydrate(bad state) error = %v, want ErrInvalidState", err)
	}
}

func pendingBet(t *testing.T) WagerTransaction {
	t.Helper()
	tx, err := NewExternal(validBetInput(t), fixedNow)
	if err != nil {
		t.Fatalf("NewExternal unexpected error: %v", err)
	}
	return tx
}

func pendingRefund(t *testing.T) WagerTransaction {
	t.Helper()
	input := validBetInput(t)
	input.Kind = Refund
	input.ReferenceExternalTransactionID = "ext-0"
	tx, err := NewExternal(input, fixedNow)
	if err != nil {
		t.Fatalf("NewExternal(REFUND) unexpected error: %v", err)
	}
	return tx
}

func TestMarkProcessed_Success(t *testing.T) {
	tx := pendingBet(t)
	later := fixedNow.Add(time.Minute)

	processed, err := tx.MarkProcessed(mustMoney(t, "75.00", "BRL"), "", later)
	if err != nil {
		t.Fatalf("MarkProcessed unexpected error: %v", err)
	}
	if processed.State() != Processed {
		t.Errorf("State() = %q, want PROCESSED", processed.State())
	}
	if processed.UpdatedAt() != later {
		t.Errorf("UpdatedAt() = %v, want %v", processed.UpdatedAt(), later)
	}
	balance, ok := processed.ResultingBalance()
	if !ok || balance.String() != "75.00" {
		t.Errorf("ResultingBalance() = (%s, %v), want (75.00, true)", balance.String(), ok)
	}

	// Original must be unaffected.
	if tx.State() != Pending {
		t.Errorf("original transaction mutated: state=%q", tx.State())
	}
}

func TestMarkProcessed_CurrencyMismatch(t *testing.T) {
	tx := pendingBet(t)
	if _, err := tx.MarkProcessed(mustMoney(t, "75.00", "USD"), "", fixedNow); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Fatalf("MarkProcessed(wrong currency) error = %v, want money.ErrCurrencyMismatch", err)
	}
}

func TestMarkProcessed_WithResolvedReference(t *testing.T) {
	tx := pendingRefund(t)
	processed, err := tx.MarkProcessed(mustMoney(t, "125.00", "BRL"), "tx-0", fixedNow)
	if err != nil {
		t.Fatalf("MarkProcessed unexpected error: %v", err)
	}
	if processed.ResolvedReferenceID() != "tx-0" {
		t.Errorf("ResolvedReferenceID() = %q, want tx-0", processed.ResolvedReferenceID())
	}
}

func TestMarkPendingReference_Success(t *testing.T) {
	tx := pendingRefund(t)
	waiting, err := tx.MarkPendingReference(fixedNow)
	if err != nil {
		t.Fatalf("MarkPendingReference unexpected error: %v", err)
	}
	if waiting.State() != PendingReference {
		t.Errorf("State() = %q, want PENDING_REFERENCE", waiting.State())
	}
}

func TestMarkPendingReference_RejectedForKindWithoutReference(t *testing.T) {
	tx := pendingBet(t)
	if _, err := tx.MarkPendingReference(fixedNow); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("MarkPendingReference(BET) error = %v, want ErrInvalidState", err)
	}
}

func TestMarkRejected_Success(t *testing.T) {
	tx := pendingBet(t)
	rejected, err := tx.MarkRejected(FailureCodeInsufficientBalanceForBet, fixedNow)
	if err != nil {
		t.Fatalf("MarkRejected unexpected error: %v", err)
	}
	if rejected.State() != Rejected {
		t.Errorf("State() = %q, want REJECTED", rejected.State())
	}
	if rejected.FailureCode() != FailureCodeInsufficientBalanceForBet {
		t.Errorf("FailureCode() = %q, want %q", rejected.FailureCode(), FailureCodeInsufficientBalanceForBet)
	}
}

func TestMarkRejected_RequiresFailureCode(t *testing.T) {
	tx := pendingBet(t)
	if _, err := tx.MarkRejected("", fixedNow); !errors.Is(err, ErrFailureCodeRequired) {
		t.Fatalf("MarkRejected(empty code) error = %v, want ErrFailureCodeRequired", err)
	}
}

func TestMarkFailed_Success(t *testing.T) {
	tx := pendingBet(t)
	failed, err := tx.MarkFailed("INFRA_TIMEOUT", fixedNow)
	if err != nil {
		t.Fatalf("MarkFailed unexpected error: %v", err)
	}
	if failed.State() != Failed {
		t.Errorf("State() = %q, want FAILED", failed.State())
	}
}

func TestTerminalTransaction_RejectsFurtherTransitions(t *testing.T) {
	tx := pendingBet(t)
	processed, err := tx.MarkProcessed(mustMoney(t, "75.00", "BRL"), "", fixedNow)
	if err != nil {
		t.Fatalf("MarkProcessed unexpected error: %v", err)
	}

	if _, err := processed.MarkProcessed(mustMoney(t, "75.00", "BRL"), "", fixedNow); !errors.Is(err, ErrTerminalTransaction) {
		t.Errorf("MarkProcessed on PROCESSED error = %v, want ErrTerminalTransaction", err)
	}
	if _, err := processed.MarkRejected(FailureCodeInsufficientBalanceForBet, fixedNow); !errors.Is(err, ErrTerminalTransaction) {
		t.Errorf("MarkRejected on PROCESSED error = %v, want ErrTerminalTransaction", err)
	}
	if _, err := processed.MarkFailed("INFRA_TIMEOUT", fixedNow); !errors.Is(err, ErrTerminalTransaction) {
		t.Errorf("MarkFailed on PROCESSED error = %v, want ErrTerminalTransaction", err)
	}
}

func processedTx(t *testing.T, kind Kind, amount string, providerID ProviderID, playerID string, walletID string, roundID string) WagerTransaction {
	t.Helper()
	input := ExternalInput{
		ID:                    ID("tx-" + string(kind)),
		ProviderID:            providerID,
		ExternalTransactionID: "ext-0",
		IdempotencyKey:        "key-0",
		PayloadHash:           "hash-0",
		WalletID:              walletFrom(walletID),
		PlayerID:              playerFrom(playerID),
		RoundID:               RoundID(roundID),
		GameID:                "game-1",
		Kind:                  kind,
		Money:                 mustMoney(t, amount, "BRL"),
	}
	tx, err := NewExternal(input, fixedNow)
	if err != nil {
		t.Fatalf("NewExternal(%s) unexpected error: %v", kind, err)
	}
	processed, err := tx.MarkProcessed(mustMoney(t, "1000.00", "BRL"), "", fixedNow)
	if err != nil {
		t.Fatalf("MarkProcessed unexpected error: %v", err)
	}
	return processed
}

func reversalTx(t *testing.T, kind Kind, amount string, providerID ProviderID, playerID string, walletID string, roundID string) WagerTransaction {
	t.Helper()
	input := ExternalInput{
		ID:                             ID("tx-reversal"),
		ProviderID:                     providerID,
		ExternalTransactionID:          "ext-1",
		IdempotencyKey:                 "key-1",
		PayloadHash:                    "hash-1",
		WalletID:                       walletFrom(walletID),
		PlayerID:                       playerFrom(playerID),
		RoundID:                        RoundID(roundID),
		GameID:                         "game-1",
		Kind:                           kind,
		Money:                          mustMoney(t, amount, "BRL"),
		ReferenceExternalTransactionID: "ext-0",
	}
	tx, err := NewExternal(input, fixedNow)
	if err != nil {
		t.Fatalf("NewExternal(%s) unexpected error: %v", kind, err)
	}
	return tx
}

func TestValidateReversalAgreement_RollbackOfBet(t *testing.T) {
	bet := processedTx(t, Bet, "25.00", "provider-a", "player-1", "wallet-1", "round-1")
	rollback := reversalTx(t, Rollback, "25.00", "provider-a", "player-1", "wallet-1", "round-1")

	if err := ValidateReversalAgreement(rollback, bet); err != nil {
		t.Fatalf("ValidateReversalAgreement unexpected error: %v", err)
	}
}

func TestValidateReversalAgreement_RefundOfBet(t *testing.T) {
	bet := processedTx(t, Bet, "25.00", "provider-a", "player-1", "wallet-1", "round-1")
	refund := reversalTx(t, Refund, "25.00", "provider-a", "player-1", "wallet-1", "round-1")

	if err := ValidateReversalAgreement(refund, bet); err != nil {
		t.Fatalf("ValidateReversalAgreement unexpected error: %v", err)
	}
}

func TestValidateReversalAgreement_RefundCannotReferenceWin(t *testing.T) {
	win := processedTx(t, Win, "25.00", "provider-a", "player-1", "wallet-1", "round-1")
	refund := reversalTx(t, Refund, "25.00", "provider-a", "player-1", "wallet-1", "round-1")

	if err := ValidateReversalAgreement(refund, win); !errors.Is(err, ErrReferenceKindMismatch) {
		t.Fatalf("ValidateReversalAgreement(REFUND of WIN) error = %v, want ErrReferenceKindMismatch", err)
	}
}

func TestValidateReversalAgreement_RollbackCannotReferenceLoss(t *testing.T) {
	loss := processedTx(t, Loss, "0.00", "provider-a", "player-1", "wallet-1", "round-1")
	rollback := reversalTx(t, Rollback, "25.00", "provider-a", "player-1", "wallet-1", "round-1")

	if err := ValidateReversalAgreement(rollback, loss); !errors.Is(err, ErrReferenceKindMismatch) {
		t.Fatalf("ValidateReversalAgreement(ROLLBACK of LOSS) error = %v, want ErrReferenceKindMismatch", err)
	}
}

func TestValidateReversalAgreement_ReferenceNotProcessed(t *testing.T) {
	bet := pendingBet(t) // still PENDING, not PROCESSED
	rollback := reversalTx(t, Rollback, "25.00", "provider-a", "player-1", "wallet-1", "round-1")

	if err := ValidateReversalAgreement(rollback, bet); !errors.Is(err, ErrReferenceNotProcessed) {
		t.Fatalf("ValidateReversalAgreement(unprocessed reference) error = %v, want ErrReferenceNotProcessed", err)
	}
}

func TestValidateReversalAgreement_FieldMismatch(t *testing.T) {
	bet := processedTx(t, Bet, "25.00", "provider-a", "player-1", "wallet-1", "round-1")
	rollback := reversalTx(t, Rollback, "25.00", "provider-a", "player-1", "wallet-1", "round-2") // different round

	if err := ValidateReversalAgreement(rollback, bet); !errors.Is(err, ErrReferenceFieldMismatch) {
		t.Fatalf("ValidateReversalAgreement(round mismatch) error = %v, want ErrReferenceFieldMismatch", err)
	}
}

func TestValidateReversalAgreement_AmountMismatch(t *testing.T) {
	bet := processedTx(t, Bet, "25.00", "provider-a", "player-1", "wallet-1", "round-1")
	rollback := reversalTx(t, Rollback, "30.00", "provider-a", "player-1", "wallet-1", "round-1")

	if err := ValidateReversalAgreement(rollback, bet); !errors.Is(err, ErrReferenceAmountMismatch) {
		t.Fatalf("ValidateReversalAgreement(amount mismatch) error = %v, want ErrReferenceAmountMismatch", err)
	}
}
