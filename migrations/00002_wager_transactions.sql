-- Wager transactions: external provider operations and the internal OPENING.
-- Idempotency and the financial invariants live in constraints and indexes:
--   * (provider_id, external_transaction_id) and (provider_id, idempotency_key) unique
--   * at most one OPENING per wallet
--   * a transaction is reversed successfully at most once (REFUND or ROLLBACK, any kind)
-- Terminal rows are immutable; identity columns never change; rows are never deleted.

-- +goose Up
CREATE TABLE wager_transactions (
    id                                 uuid        PRIMARY KEY,
    origin                             text        NOT NULL CHECK (origin IN ('EXTERNAL', 'INTERNAL')),
    kind                               text        NOT NULL CHECK (kind IN ('OPENING', 'BET', 'WIN', 'LOSS', 'REFUND', 'ROLLBACK')),
    status                             text        NOT NULL CHECK (status IN ('PENDING', 'PENDING_REFERENCE', 'PROCESSED', 'REJECTED', 'FAILED')),
    wallet_id                          uuid        NOT NULL,
    player_id                          uuid        NOT NULL,
    currency                           char(3)     NOT NULL,
    amount_minor                       bigint      NOT NULL CHECK (amount_minor >= 0),
    provider_id                        text,
    external_transaction_id            text,
    idempotency_key                    text,
    payload_hash                       text        CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
    round_id                           text,
    game_id                            text,
    reference_external_transaction_id  text,
    reference_transaction_id           uuid        REFERENCES wager_transactions (id),
    failure_code                       text,
    result_balance_minor               bigint,
    result_wallet_version              bigint      CHECK (result_wallet_version >= 1),
    attempts                           integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at                    timestamptz,
    reference_deadline                 timestamptz,
    created_at                         timestamptz NOT NULL,
    updated_at                         timestamptz NOT NULL,
    processed_at                       timestamptz,

    -- wallet only (not currency): a CURRENCY_MISMATCH rejection must still be persisted for audit;
    -- currency consistency of real movements is enforced by the ledger composite FKs.
    CONSTRAINT wager_transactions_wallet_fkey FOREIGN KEY (wallet_id) REFERENCES wallets (id),
    -- target of the ledger composite FK: an entry must match its transaction's wallet, currency and amount
    CONSTRAINT wager_transactions_ledger_target_key UNIQUE (id, wallet_id, currency, amount_minor),

    CONSTRAINT wager_transactions_origin_shape CHECK (
        (origin = 'INTERNAL' AND kind = 'OPENING'
            AND provider_id IS NULL AND external_transaction_id IS NULL AND idempotency_key IS NULL
            AND payload_hash IS NULL AND round_id IS NULL AND game_id IS NULL
            AND reference_external_transaction_id IS NULL)
        OR
        (origin = 'EXTERNAL' AND kind <> 'OPENING'
            AND provider_id IS NOT NULL AND external_transaction_id IS NOT NULL AND idempotency_key IS NOT NULL
            AND payload_hash IS NOT NULL AND round_id IS NOT NULL AND game_id IS NOT NULL)
    ),
    CONSTRAINT wager_transactions_amount_by_kind CHECK (
        (kind = 'LOSS' AND amount_minor = 0) OR (kind <> 'LOSS' AND amount_minor > 0)
    ),
    CONSTRAINT wager_transactions_reference_required CHECK (
        kind NOT IN ('REFUND', 'ROLLBACK') OR reference_external_transaction_id IS NOT NULL
    ),
    CONSTRAINT wager_transactions_failure_code CHECK (
        (status IN ('REJECTED', 'FAILED')) = (failure_code IS NOT NULL)
    ),
    CONSTRAINT wager_transactions_processed_result CHECK (
        status <> 'PROCESSED' OR (result_balance_minor IS NOT NULL AND result_wallet_version IS NOT NULL)
    ),
    CONSTRAINT wager_transactions_terminal_timestamp CHECK (
        status NOT IN ('PROCESSED', 'REJECTED', 'FAILED') OR processed_at IS NOT NULL
    ),
    CONSTRAINT wager_transactions_pending_schedule CHECK (
        status <> 'PENDING_REFERENCE' OR (next_attempt_at IS NOT NULL AND reference_deadline IS NOT NULL)
    )
);

CREATE UNIQUE INDEX wager_transactions_provider_external_key
    ON wager_transactions (provider_id, external_transaction_id) WHERE origin = 'EXTERNAL';
CREATE UNIQUE INDEX wager_transactions_provider_idempotency_key
    ON wager_transactions (provider_id, idempotency_key) WHERE origin = 'EXTERNAL';
CREATE UNIQUE INDEX wager_transactions_one_opening_per_wallet
    ON wager_transactions (wallet_id) WHERE kind = 'OPENING';
CREATE UNIQUE INDEX wager_transactions_single_successful_reversal
    ON wager_transactions (reference_transaction_id) WHERE status = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK');
CREATE INDEX wager_transactions_pending_due
    ON wager_transactions (next_attempt_at) WHERE status = 'PENDING_REFERENCE';
CREATE INDEX wager_transactions_waiting_on
    ON wager_transactions (provider_id, reference_external_transaction_id) WHERE status = 'PENDING_REFERENCE';

-- +goose StatementBegin
CREATE FUNCTION wager_transactions_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'wager transactions are never deleted' USING ERRCODE = 'restrict_violation';
    END IF;
    IF OLD.status IN ('PROCESSED', 'REJECTED', 'FAILED') THEN
        RAISE EXCEPTION 'transaction % is terminal (%) and immutable', OLD.id, OLD.status USING ERRCODE = 'restrict_violation';
    END IF;
    IF (NEW.id, NEW.origin, NEW.kind, NEW.wallet_id, NEW.player_id, NEW.currency, NEW.amount_minor,
        NEW.provider_id, NEW.external_transaction_id, NEW.idempotency_key, NEW.payload_hash,
        NEW.round_id, NEW.game_id, NEW.reference_external_transaction_id, NEW.created_at)
       IS DISTINCT FROM
       (OLD.id, OLD.origin, OLD.kind, OLD.wallet_id, OLD.player_id, OLD.currency, OLD.amount_minor,
        OLD.provider_id, OLD.external_transaction_id, OLD.idempotency_key, OLD.payload_hash,
        OLD.round_id, OLD.game_id, OLD.reference_external_transaction_id, OLD.created_at) THEN
        RAISE EXCEPTION 'transaction identity is immutable' USING ERRCODE = 'restrict_violation';
    END IF;
    IF NEW.status = 'PENDING' AND OLD.status <> 'PENDING' THEN
        RAISE EXCEPTION 'a transaction cannot return to PENDING' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER wager_transactions_guard BEFORE UPDATE OR DELETE ON wager_transactions
    FOR EACH ROW EXECUTE FUNCTION wager_transactions_guard();
CREATE TRIGGER wager_transactions_no_truncate BEFORE TRUNCATE ON wager_transactions
    FOR EACH STATEMENT EXECUTE FUNCTION forbid_truncate();

-- +goose Down
DROP TABLE wager_transactions;
DROP FUNCTION wager_transactions_guard();
