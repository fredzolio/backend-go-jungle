-- Append-only ledger. Every balance change of a wallet is one entry; entries are
-- chained per wallet by wallet_version (stable order and pagination cursor).
-- Guarantees enforced here, independent of application code:
--   * balance_after = balance_before ± amount, never negative            (CHECK)
--   * one entry per (wallet, transaction) and per (wallet, version)        (UNIQUE)
--   * entry matches its transaction's wallet, currency and amount         (composite FK)
--   * chain continuity: before = previous after, version = previous + 1   (BEFORE INSERT)
--   * no UPDATE / DELETE / TRUNCATE                                       (triggers + REVOKE)
--   * at commit, wallets.balance/version equal the last entry             (DEFERRED constraint triggers)

-- +goose Up
CREATE TABLE wallet_ledger_entries (
    id                   uuid        PRIMARY KEY,
    wallet_id            uuid        NOT NULL,
    transaction_id       uuid        NOT NULL,
    direction            text        NOT NULL CHECK (direction IN ('DEBIT', 'CREDIT')),
    currency             char(3)     NOT NULL,
    amount_minor         bigint      NOT NULL CHECK (amount_minor > 0),
    balance_before_minor bigint      NOT NULL CHECK (balance_before_minor >= 0),
    balance_after_minor  bigint      NOT NULL CHECK (balance_after_minor >= 0),
    wallet_version       bigint      NOT NULL CHECK (wallet_version >= 1),
    created_at           timestamptz NOT NULL,

    CONSTRAINT wallet_ledger_entries_arithmetic CHECK (
        (direction = 'CREDIT' AND balance_after_minor = balance_before_minor + amount_minor)
        OR (direction = 'DEBIT' AND balance_after_minor = balance_before_minor - amount_minor)
    ),
    CONSTRAINT wallet_ledger_entries_wallet_transaction_key UNIQUE (wallet_id, transaction_id),
    CONSTRAINT wallet_ledger_entries_wallet_version_key UNIQUE (wallet_id, wallet_version),
    CONSTRAINT wallet_ledger_entries_wallet_fkey FOREIGN KEY (wallet_id, currency) REFERENCES wallets (id, currency),
    CONSTRAINT wallet_ledger_entries_transaction_fkey FOREIGN KEY (transaction_id, wallet_id, currency, amount_minor)
        REFERENCES wager_transactions (id, wallet_id, currency, amount_minor)
);

-- +goose StatementBegin
CREATE FUNCTION ledger_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'wallet_ledger_entries is append-only (% rejected)', TG_OP USING ERRCODE = 'restrict_violation';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION ledger_chain_check() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    prev wallet_ledger_entries%ROWTYPE;
BEGIN
    SELECT * INTO prev FROM wallet_ledger_entries
     WHERE wallet_id = NEW.wallet_id ORDER BY wallet_version DESC LIMIT 1;
    IF FOUND THEN
        IF NEW.wallet_version <> prev.wallet_version + 1 OR NEW.balance_before_minor <> prev.balance_after_minor THEN
            RAISE EXCEPTION 'ledger chain broken for wallet %: version % after %, before % after %',
                NEW.wallet_id, NEW.wallet_version, prev.wallet_version, NEW.balance_before_minor, prev.balance_after_minor
                USING ERRCODE = 'integrity_constraint_violation';
        END IF;
    ELSIF NEW.balance_before_minor <> 0 OR NEW.wallet_version NOT IN (1, 2) THEN
        -- version 1: opening credit; version 2: first movement of a wallet opened with zero
        RAISE EXCEPTION 'first ledger entry of wallet % must start from zero', NEW.wallet_id
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION wallet_matches_ledger() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    subject      uuid;
    w            wallets%ROWTYPE;
    last_after   bigint;
    last_version bigint;
BEGIN
    -- IF (not CASE): PL/pgSQL resolves NEW.<column> per row type, and wallets has no wallet_id.
    IF TG_TABLE_NAME = 'wallets' THEN
        subject := NEW.id;
    ELSE
        subject := NEW.wallet_id;
    END IF;
    SELECT * INTO w FROM wallets WHERE id = subject;
    SELECT balance_after_minor, wallet_version INTO last_after, last_version
      FROM wallet_ledger_entries WHERE wallet_id = subject ORDER BY wallet_version DESC LIMIT 1;
    IF w.balance_minor <> COALESCE(last_after, 0) OR w.version <> COALESCE(last_version, 1) THEN
        RAISE EXCEPTION 'wallet % (balance %, version %) diverges from its ledger (balance %, version %)',
            subject, w.balance_minor, w.version, COALESCE(last_after, 0), COALESCE(last_version, 1)
            USING ERRCODE = 'integrity_constraint_violation';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER wallet_ledger_entries_append_only BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_append_only();
CREATE TRIGGER wallet_ledger_entries_no_truncate BEFORE TRUNCATE ON wallet_ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_truncate();
CREATE TRIGGER wallet_ledger_entries_chain BEFORE INSERT ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_chain_check();
CREATE CONSTRAINT TRIGGER wallets_match_ledger AFTER INSERT OR UPDATE ON wallets
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION wallet_matches_ledger();
CREATE CONSTRAINT TRIGGER wallet_ledger_entries_match_wallet AFTER INSERT ON wallet_ledger_entries
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION wallet_matches_ledger();

-- +goose Down
DROP TRIGGER wallets_match_ledger ON wallets;
DROP TABLE wallet_ledger_entries;
DROP FUNCTION wallet_matches_ledger();
DROP FUNCTION ledger_chain_check();
DROP FUNCTION ledger_append_only();
