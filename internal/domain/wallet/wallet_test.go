package wallet_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wallet"
)

var (
	brl = money.MustCurrency("BRL")
	t0  = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
)

func amount(t *testing.T, s string) money.Money {
	t.Helper()
	m, err := money.Parse(s, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func openWith(t *testing.T, initial string) *wallet.Wallet {
	t.Helper()
	w, _, err := wallet.Open(wallet.Opening{
		ID: uuid.New(), PlayerID: uuid.New(), InitialBalance: amount(t, initial),
		OpeningTransaction: uuid.New(), OpeningEntry: uuid.New(), At: t0,
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func move(t *testing.T, s string) wallet.Movement {
	t.Helper()
	return wallet.Movement{EntryID: uuid.New(), TransactionID: uuid.New(), Amount: amount(t, s), At: t0.Add(time.Second)}
}

func TestOpen_with_positive_balance_credits_opening_entry_at_version_1(t *testing.T) {
	w, entry, err := wallet.Open(wallet.Opening{
		ID: uuid.New(), PlayerID: uuid.New(), InitialBalance: amount(t, "1000.00"),
		OpeningTransaction: uuid.New(), OpeningEntry: uuid.New(), At: t0,
	})
	if err != nil {
		t.Fatal(err)
	}
	d := entry.Data()
	if w.Version() != 1 || d.WalletVersion != 1 || d.Direction != wallet.Credit ||
		d.BalanceBefore.Amount() != "0.00" || d.BalanceAfter.Amount() != "1000.00" || w.Balance().Amount() != "1000.00" {
		t.Fatalf("unexpected opening: version=%d entry=%+v", w.Version(), d)
	}
}

func TestOpen_with_zero_balance_creates_no_entry(t *testing.T) {
	w, entry, err := wallet.Open(wallet.Opening{ID: uuid.New(), PlayerID: uuid.New(), InitialBalance: amount(t, "0.00"), At: t0})
	if err != nil || entry != nil || w.Version() != 1 {
		t.Fatalf("w=%v entry=%v err=%v", w, entry, err)
	}
}

func TestOpen_rejects_invalid_input(t *testing.T) {
	neg, _ := money.FromMinor(-1, brl)
	cases := []wallet.Opening{
		{PlayerID: uuid.New(), InitialBalance: amount(t, "1.00"), At: t0},
		{ID: uuid.New(), InitialBalance: amount(t, "1.00"), At: t0},
		{ID: uuid.New(), PlayerID: uuid.New(), At: t0},
		{ID: uuid.New(), PlayerID: uuid.New(), InitialBalance: neg, At: t0},
		{ID: uuid.New(), PlayerID: uuid.New(), InitialBalance: amount(t, "1.00")},
	}
	for i, o := range cases {
		if _, _, err := wallet.Open(o); !errors.Is(err, wallet.ErrInvalidWallet) {
			t.Fatalf("case %d: err = %v", i, err)
		}
	}
}

func TestDebit_moves_balance_and_increments_version(t *testing.T) {
	w := openWith(t, "100.00")
	entry, err := w.Debit(move(t, "80.00"))
	if err != nil {
		t.Fatal(err)
	}
	d := entry.Data()
	if w.Balance().Amount() != "20.00" || w.Version() != 2 || d.WalletVersion != 2 ||
		d.BalanceBefore.Amount() != "100.00" || d.BalanceAfter.Amount() != "20.00" {
		t.Fatalf("balance=%s version=%d entry=%+v", w.Balance().Amount(), w.Version(), d)
	}
}

func TestDebit_beyond_balance_is_rejected_without_side_effects(t *testing.T) {
	w := openWith(t, "100.00")
	if _, err := w.Debit(move(t, "80.00")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Debit(move(t, "80.00")); !errors.Is(err, wallet.ErrInsufficientFunds) {
		t.Fatalf("err = %v", err)
	}
	if w.Balance().Amount() != "20.00" || w.Version() != 2 {
		t.Fatalf("state changed: %s v%d", w.Balance().Amount(), w.Version())
	}
}

func TestDebit_of_entire_balance_reaches_zero(t *testing.T) {
	w := openWith(t, "50.00")
	if _, err := w.Debit(move(t, "50.00")); err != nil || !w.Balance().IsZero() {
		t.Fatalf("balance=%s err=%v", w.Balance().Amount(), err)
	}
}

func TestCredit_adds_to_balance(t *testing.T) {
	w := openWith(t, "0.00")
	if _, err := w.Credit(move(t, "25.00")); err != nil || w.Balance().Amount() != "25.00" || w.Version() != 2 {
		t.Fatalf("balance=%s v=%d err=%v", w.Balance().Amount(), w.Version(), err)
	}
}

func TestMovement_rejects_zero_amount_and_foreign_currency(t *testing.T) {
	w := openWith(t, "10.00")
	if _, err := w.Credit(move(t, "0.00")); !errors.Is(err, wallet.ErrInvalidMovement) {
		t.Fatalf("zero: %v", err)
	}
	usd, _ := money.Parse("1.00", "USD")
	m := move(t, "1.00")
	m.Amount = usd
	if _, err := w.Debit(m); !errors.Is(err, wallet.ErrCurrencyMismatch) {
		t.Fatalf("currency: %v", err)
	}
	if w.Version() != 1 {
		t.Fatalf("version changed to %d", w.Version())
	}
}

func TestRehydrate_rejects_impossible_states(t *testing.T) {
	neg, _ := money.FromMinor(-100, brl)
	valid := wallet.Snapshot{ID: uuid.New(), PlayerID: uuid.New(), Balance: amount(t, "1.00"), Version: 3, CreatedAt: t0, UpdatedAt: t0}
	if _, err := wallet.Rehydrate(valid); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}
	for name, mutate := range map[string]func(*wallet.Snapshot){
		"negative balance": func(s *wallet.Snapshot) { s.Balance = neg },
		"version zero":     func(s *wallet.Snapshot) { s.Version = 0 },
		"no id":            func(s *wallet.Snapshot) { s.ID = uuid.Nil },
		"updated<created":  func(s *wallet.Snapshot) { s.UpdatedAt = t0.Add(-time.Hour) },
		"zero balance val": func(s *wallet.Snapshot) { s.Balance = money.Money{} },
	} {
		s := valid
		mutate(&s)
		if _, err := wallet.Rehydrate(s); !errors.Is(err, wallet.ErrInvalidWallet) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}

func TestNewLedgerEntry_requires_consistent_balances(t *testing.T) {
	base := wallet.LedgerEntryData{
		ID: uuid.New(), WalletID: uuid.New(), TransactionID: uuid.New(), Direction: wallet.Debit,
		Amount: amount(t, "10.00"), BalanceBefore: amount(t, "30.00"), BalanceAfter: amount(t, "20.00"),
		WalletVersion: 2, CreatedAt: t0,
	}
	if _, err := wallet.NewLedgerEntry(base); err != nil {
		t.Fatalf("valid entry rejected: %v", err)
	}
	wrongDirection := base
	wrongDirection.Direction = wallet.Credit
	wrongAfter := base
	wrongAfter.BalanceAfter = amount(t, "21.00")
	overdraw := base
	overdraw.BalanceBefore, overdraw.Amount = amount(t, "5.00"), amount(t, "10.00")
	overdraw.BalanceAfter, _ = money.FromMinor(-500, brl)
	for name, d := range map[string]wallet.LedgerEntryData{"direction": wrongDirection, "after": wrongAfter, "negative": overdraw} {
		if _, err := wallet.NewLedgerEntry(d); !errors.Is(err, wallet.ErrInvalidLedgerEntry) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}
