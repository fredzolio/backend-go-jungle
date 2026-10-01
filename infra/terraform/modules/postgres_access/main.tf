# Application database and least-privilege roles.
#   jungle_owner    owns the schema; runs migrations.
#   jungle_app      DML for the service. Ledger UPDATE/DELETE/TRUNCATE are revoked by migrations.
#   jungle_readonly read-only (reconciliation dashboards, ad-hoc audit).
terraform {
  required_providers {
    postgresql = { source = "cyrilgdn/postgresql" }
    random     = { source = "hashicorp/random" }
  }
}

locals {
  roles = toset(["owner", "app", "readonly"])
}

resource "random_password" "role" {
  for_each = local.roles
  length   = 32
  special  = false
}

resource "postgresql_role" "role" {
  for_each = local.roles
  name     = "jungle_${each.key}"
  login    = true
  password = random_password.role[each.key].result
}

resource "postgresql_database" "jungle" {
  name  = var.database
  owner = postgresql_role.role["owner"].name
}

resource "postgresql_grant" "connect" {
  for_each    = toset(["app", "readonly"])
  database    = postgresql_database.jungle.name
  role        = postgresql_role.role[each.key].name
  object_type = "database"
  privileges  = ["CONNECT"]
}

resource "postgresql_grant" "schema_usage" {
  for_each    = toset(["app", "readonly"])
  database    = postgresql_database.jungle.name
  schema      = "public"
  role        = postgresql_role.role[each.key].name
  object_type = "schema"
  privileges  = ["USAGE"]
}

# Privileges on tables the owner creates later through migrations.
resource "postgresql_default_privileges" "app_tables" {
  database    = postgresql_database.jungle.name
  schema      = "public"
  owner       = postgresql_role.role["owner"].name
  role        = postgresql_role.role["app"].name
  object_type = "table"
  privileges  = ["SELECT", "INSERT", "UPDATE"]
}

resource "postgresql_default_privileges" "app_sequences" {
  database    = postgresql_database.jungle.name
  schema      = "public"
  owner       = postgresql_role.role["owner"].name
  role        = postgresql_role.role["app"].name
  object_type = "sequence"
  privileges  = ["USAGE", "SELECT"]
}

resource "postgresql_default_privileges" "readonly_tables" {
  database    = postgresql_database.jungle.name
  schema      = "public"
  owner       = postgresql_role.role["owner"].name
  role        = postgresql_role.role["readonly"].name
  object_type = "table"
  privileges  = ["SELECT"]
}
