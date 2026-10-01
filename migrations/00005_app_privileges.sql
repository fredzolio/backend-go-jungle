-- Least privilege for the runtime role. Tables are owned by jungle_owner (who runs
-- migrations); default privileges (Terraform) grant jungle_app SELECT/INSERT/UPDATE.
-- Here the ledger becomes INSERT/SELECT only, and nothing can be deleted by the app.
-- Guarded so the migration also applies on databases without these roles.

-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'jungle_app') THEN
        REVOKE UPDATE, DELETE, TRUNCATE ON wallet_ledger_entries FROM jungle_app;
        REVOKE DELETE, TRUNCATE ON wallets, wager_transactions, inbox_messages, outbox_events FROM jungle_app;
        REVOKE ALL ON goose_db_version FROM jungle_app;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'jungle_readonly') THEN
        REVOKE ALL ON goose_db_version FROM jungle_readonly;
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'jungle_app') THEN
        GRANT UPDATE ON wallet_ledger_entries TO jungle_app;
    END IF;
END;
$$;
-- +goose StatementEnd
