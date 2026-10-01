# Realm "jungle": confidential service clients using client_credentials only.
#
# Authorization model
#   * Game providers (provider-a, provider-b, ...): scopes wagering.write + wagering.read and a
#     hardcoded `provider_id` claim. The API derives the authorized providerId from that claim.
#   * Internal service (jungle-internal): wallet scopes; it has no provider_id claim.
#   * Every token carries aud=jungle-api, which the API validates.
#   * provider-c-shortlived: token lifespan of a few seconds, for expired-token tests.
terraform {
  required_providers {
    keycloak = { source = "keycloak/keycloak" }
    random   = { source = "hashicorp/random" }
  }
}

locals {
  wagering_scopes = ["wagering.write", "wagering.read"]
  wallet_scopes   = ["wallets.write", "wallets.read", "wallets.reconcile", "wagering.read"]
  all_scopes      = toset(concat(local.wagering_scopes, local.wallet_scopes))

  provider_clients = merge(
    { for p in var.game_providers : p => { provider_id = p, lifespan = "" } },
    { (var.shortlived_client) = { provider_id = var.shortlived_provider_id, lifespan = var.shortlived_lifespan_seconds } },
  )
  clients = merge(
    { for id, c in local.provider_clients : id => { scopes = local.wagering_scopes, provider_id = c.provider_id, lifespan = c.lifespan } },
    { (var.internal_client) = { scopes = local.wallet_scopes, provider_id = "", lifespan = "" } },
  )
}

resource "keycloak_realm" "jungle" {
  realm                 = var.realm
  enabled               = true
  access_token_lifespan = "5m"
  ssl_required          = "external"
}

resource "keycloak_openid_client_scope" "scope" {
  for_each               = local.all_scopes
  realm_id               = keycloak_realm.jungle.id
  name                   = each.key
  include_in_token_scope = true
}

resource "keycloak_openid_client_scope" "audience" {
  realm_id               = keycloak_realm.jungle.id
  name                   = "${var.audience}-audience"
  include_in_token_scope = false
}

resource "keycloak_openid_audience_protocol_mapper" "audience" {
  realm_id                 = keycloak_realm.jungle.id
  client_scope_id          = keycloak_openid_client_scope.audience.id
  name                     = "audience"
  included_custom_audience = var.audience
  add_to_access_token      = true
  add_to_id_token          = false
}

resource "random_password" "client" {
  for_each = local.clients
  length   = 40
  special  = false
}

resource "keycloak_openid_client" "client" {
  for_each                     = local.clients
  realm_id                     = keycloak_realm.jungle.id
  client_id                    = each.key
  name                         = each.key
  enabled                      = true
  access_type                  = "CONFIDENTIAL"
  client_secret                = random_password.client[each.key].result
  service_accounts_enabled     = true
  standard_flow_enabled        = false
  implicit_flow_enabled        = false
  direct_access_grants_enabled = false
  full_scope_allowed           = false
  access_token_lifespan        = each.value.lifespan
}

resource "keycloak_openid_hardcoded_claim_protocol_mapper" "provider_id" {
  for_each            = { for id, c in local.clients : id => c if c.provider_id != "" }
  realm_id            = keycloak_realm.jungle.id
  client_id           = keycloak_openid_client.client[each.key].id
  name                = "provider-id"
  claim_name          = "provider_id"
  claim_value         = each.value.provider_id
  claim_value_type    = "String"
  add_to_access_token = true
  add_to_id_token     = false
  add_to_userinfo     = false
}

resource "keycloak_openid_client_default_scopes" "client" {
  for_each  = local.clients
  realm_id  = keycloak_realm.jungle.id
  client_id = keycloak_openid_client.client[each.key].id
  default_scopes = concat(
    ["basic", keycloak_openid_client_scope.audience.name],
    [for s in each.value.scopes : keycloak_openid_client_scope.scope[s].name],
  )
}

resource "keycloak_openid_client_optional_scopes" "client" {
  for_each        = local.clients
  realm_id        = keycloak_realm.jungle.id
  client_id       = keycloak_openid_client.client[each.key].id
  optional_scopes = []
}
