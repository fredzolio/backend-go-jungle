-- Outbox claim performance (found by the load test): the relay looks for the
-- oldest pending events whose partition has no earlier pending event. Both
-- lookups get partial indexes over unpublished rows only (parked rows included,
-- since a parked head must keep blocking its partition).

-- +goose Up
DROP INDEX outbox_events_pending;
CREATE INDEX outbox_events_unpublished_seq ON outbox_events (seq) WHERE published_at IS NULL;
CREATE INDEX outbox_events_unpublished_partition ON outbox_events (partition_key, seq) WHERE published_at IS NULL;

-- +goose Down
DROP INDEX outbox_events_unpublished_partition;
DROP INDEX outbox_events_unpublished_seq;
CREATE INDEX outbox_events_pending ON outbox_events (partition_key, seq)
    WHERE published_at IS NULL AND dead_at IS NULL;
