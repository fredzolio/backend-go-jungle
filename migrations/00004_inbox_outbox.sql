-- Transactional inbox (SQS deduplication per consumer) and outbox (integration
-- events published after commit by a separate relay).

-- +goose Up
CREATE TABLE inbox_messages (
    consumer_name  text        NOT NULL,
    message_id     text        NOT NULL,
    payload_hash   text        NOT NULL CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
    transaction_id uuid        REFERENCES wager_transactions (id),
    received_at    timestamptz NOT NULL,
    completed_at   timestamptz,
    PRIMARY KEY (consumer_name, message_id)
);

CREATE TABLE outbox_events (
    id             uuid        PRIMARY KEY,                -- eventId, preserved on republication
    seq            bigint      GENERATED ALWAYS AS IDENTITY UNIQUE,
    aggregate_type text        NOT NULL,
    aggregate_id   text        NOT NULL,
    partition_key  text        NOT NULL,                   -- walletId: ordering + MessageGroupId
    event_type     text        NOT NULL,
    event_version  integer     NOT NULL CHECK (event_version >= 1),
    payload        jsonb       NOT NULL,                   -- immutable snapshot (full envelope)
    occurred_at    timestamptz NOT NULL,
    attempts       integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL,
    locked_by      text,
    locked_until   timestamptz,
    claim_id       uuid,                                   -- fencing token of the current lease
    published_at   timestamptz,
    dead_at        timestamptz,
    last_error     text
);

-- relay scans the oldest pending event of each partition
CREATE INDEX outbox_events_pending ON outbox_events (partition_key, seq)
    WHERE published_at IS NULL AND dead_at IS NULL;

-- +goose StatementBegin
CREATE FUNCTION outbox_events_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.published_at IS NULL AND OLD.dead_at IS NULL THEN
            RAISE EXCEPTION 'pending outbox event % cannot be deleted', OLD.id USING ERRCODE = 'restrict_violation';
        END IF;
        RETURN OLD;
    END IF;
    IF (NEW.id, NEW.seq, NEW.aggregate_type, NEW.aggregate_id, NEW.partition_key, NEW.event_type,
        NEW.event_version, NEW.payload, NEW.occurred_at)
       IS DISTINCT FROM
       (OLD.id, OLD.seq, OLD.aggregate_type, OLD.aggregate_id, OLD.partition_key, OLD.event_type,
        OLD.event_version, OLD.payload, OLD.occurred_at) THEN
        RAISE EXCEPTION 'outbox event content is immutable' USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER outbox_events_guard BEFORE UPDATE OR DELETE ON outbox_events
    FOR EACH ROW EXECUTE FUNCTION outbox_events_guard();

-- +goose Down
DROP TABLE outbox_events;
DROP FUNCTION outbox_events_guard();
DROP TABLE inbox_messages;
