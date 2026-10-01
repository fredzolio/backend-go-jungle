package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/fredzolio/backend-go-jungle/internal/domain/events"
	"github.com/fredzolio/backend-go-jungle/internal/domain/money"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wagering"
	"github.com/fredzolio/backend-go-jungle/internal/domain/wallet"
)

// Deps are the collaborators shared by every use case.
type Deps struct {
	UoW     UnitOfWork
	Clock   Clock
	IDs     IDs
	Metrics Metrics // optional; NopMetrics when nil
}

// Wallets implements the internal wallet operations.
type Wallets struct{ d Deps }

// NewWallets builds the wallet use cases.
func NewWallets(d Deps) *Wallets { return &Wallets{d: d} }

// OpenWalletCommand opens a wallet for (PlayerID, InitialBalance currency).
type OpenWalletCommand struct {
	InitialBalance money.Money
	Meta           Meta
	PlayerID       uuid.UUID
}

// Open creates the wallet. With a positive initial balance, the PROCESSED OPENING,
// its CREDIT entry and the WagerTransactionProcessed + WalletBalanceChanged events
// commit together with the wallet (version 1). A zero balance creates none of them.
// A second wallet for the same (player, currency) fails with ErrWalletExists.
func (uc *Wallets) Open(ctx context.Context, cmd OpenWalletCommand) (wallet.Snapshot, error) {
	now := uc.d.Clock.Now()
	walletID, openingID := uc.d.IDs.New(), uc.d.IDs.New()
	w, entry, err := wallet.Open(wallet.Opening{
		ID: walletID, PlayerID: cmd.PlayerID, InitialBalance: cmd.InitialBalance,
		OpeningTransaction: openingID, OpeningEntry: uc.d.IDs.New(), At: now,
	})
	if err != nil {
		return wallet.Snapshot{}, err
	}
	err = uc.d.UoW.Do(ctx, func(ctx context.Context, tx Tx) error {
		if err := tx.Wallets().Insert(ctx, w); err != nil {
			return err
		}
		if entry == nil {
			return nil
		}
		opening, err := wagering.NewOpening(openingID, walletID, cmd.PlayerID, cmd.InitialBalance, now)
		if err != nil {
			return err
		}
		if err := opening.Process(wagering.Result{Balance: w.Balance(), WalletVersion: w.Version()}, uuid.Nil, now); err != nil {
			return err
		}
		if err := tx.Transactions().InsertOpening(ctx, opening); err != nil {
			return err
		}
		if err := tx.Ledger().Insert(ctx, *entry); err != nil {
			return err
		}
		records, err := uc.d.movementEvents(cmd.Meta, opening, entry)
		if err != nil {
			return err
		}
		return tx.Outbox().Insert(ctx, records...)
	})
	if err != nil {
		return wallet.Snapshot{}, err
	}
	return w.Snapshot(), nil
}

// movementEvents builds WagerTransactionProcessed and, when the balance changed,
// WalletBalanceChanged.
func (d Deps) movementEvents(meta Meta, t *wagering.Transaction, entry *wallet.LedgerEntry) ([]OutboxRecord, error) {
	at := t.Snapshot().UpdatedAt
	processed, err := events.NewTransactionProcessed(meta.event(d.IDs, at, t.ID().String()), t)
	if err != nil {
		return nil, err
	}
	rec, err := outboxRecord(processed, "WagerTransaction", t.WalletID())
	if err != nil {
		return nil, err
	}
	records := []OutboxRecord{rec}
	if entry != nil {
		changed, err := events.NewWalletBalanceChanged(meta.event(d.IDs, at, t.ID().String()), *entry)
		if err != nil {
			return nil, err
		}
		rec, err := outboxRecord(changed, "Wallet", t.WalletID())
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, nil
}
