-- Wallets: one per (player, currency). Balance in minor units, never negative.
-- The guard trigger keeps identity immutable, forbids deletion and enforces that
-- the version grows by exactly one when — and only when — the balance changes.

-- +goose Up
CREATE TABLE wallets (
    id            uuid        PRIMARY KEY,
    player_id     uuid        NOT NULL,
    currency      char(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    balance_minor bigint      NOT NULL CHECK (balance_minor >= 0),
    version       bigint      NOT NULL CHECK (version >= 1),
    created_at    timestamptz NOT NULL,
    updated_at    timestamptz NOT NULL CHECK (updated_at >= created_at),
    CONSTRAINT wallets_player_currency_key UNIQUE (player_id, currency),
    -- target of composite foreign keys (child rows must carry the wallet currency)
    CONSTRAINT wallets_id_currency_key UNIQUE (id, currency)
);

-- +goose StatementBegin
CREATE FUNCTION forbid_truncate() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'TRUNCATE is forbidden on %', TG_TABLE_NAME USING ERRCODE = 'restrict_violation';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION wallets_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'wallets are never deleted' USING ERRCODE = 'restrict_violation';
    END IF;
    IF (NEW.id, NEW.player_id, NEW.currency, NEW.created_at) IS DISTINCT FROM (OLD.id, OLD.player_id, OLD.currency, OLD.created_at) THEN
        RAISE EXCEPTION 'wallet identity is immutable' USING ERRCODE = 'restrict_violation';
    END IF;
    IF NEW.balance_minor <> OLD.balance_minor AND NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'a balance change must increment the version by exactly 1' USING ERRCODE = 'restrict_violation';
    END IF;
    IF NEW.balance_minor = OLD.balance_minor AND NEW.version <> OLD.version THEN
        RAISE EXCEPTION 'the version changes only together with the balance' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER wallets_guard BEFORE UPDATE OR DELETE ON wallets
    FOR EACH ROW EXECUTE FUNCTION wallets_guard();
CREATE TRIGGER wallets_no_truncate BEFORE TRUNCATE ON wallets
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_truncate();

-- +goose Down
DROP TABLE wallets;
DROP FUNCTION wallets_guard();
DROP FUNCTION forbid_truncate();
