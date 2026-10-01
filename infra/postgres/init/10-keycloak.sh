#!/bin/sh
# Bootstrap only what must exist before Terraform runs: Keycloak's own database.
# Application roles/databases are managed by Terraform (module postgres_access).
set -eu
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname postgres \
  -v kc_password="$KEYCLOAK_DB_PASSWORD" <<'SQL'
CREATE ROLE keycloak LOGIN PASSWORD :'kc_password';
CREATE DATABASE keycloak OWNER keycloak;
SQL
