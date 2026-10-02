module "postgres" {
  source = "../../modules/postgres_access"
}

module "keycloak" {
  source             = "../../modules/keycloak_realm"
  game_providers     = var.game_providers
  demo_client_secret = var.demo_client_secret
  demo_clients = var.demo_client_secret == "" ? {} : {
    "demo-internal"   = ""
    "demo-provider-1" = "demo-provider-1"
    "demo-provider-2" = "demo-provider-2"
  }
}

module "messaging" {
  source         = "../../modules/messaging"
  game_providers = var.game_providers
}

# ---------- credentials handed to the services (shared volume, read-only for them) ----------
resource "local_sensitive_file" "db_password" {
  for_each        = module.postgres.roles
  filename        = "${var.provisioned_dir}/db/${each.key}_password"
  content         = module.postgres.passwords[each.key]
  file_permission = "0400"
}

resource "local_sensitive_file" "aws_access_key_id" {
  for_each        = module.messaging.principals
  filename        = "${var.provisioned_dir}/aws/${trimprefix(each.key, "jungle-")}/access_key_id"
  content         = module.messaging.credentials[each.key].access_key_id
  file_permission = "0400"
}

resource "local_sensitive_file" "aws_secret_access_key" {
  for_each        = module.messaging.principals
  filename        = "${var.provisioned_dir}/aws/${trimprefix(each.key, "jungle-")}/secret_access_key"
  content         = module.messaging.credentials[each.key].secret_access_key
  file_permission = "0400"
}

# Sender identities allowed on the ingress queue => providerId (consumer-side authorization).
resource "local_file" "senders" {
  filename        = "${var.provisioned_dir}/aws/senders.json"
  file_permission = "0444"
  content = jsonencode({
    for principal, provider in module.messaging.producer_of : provider => {
      access_key_id = module.messaging.credentials[principal].access_key_id
      user_id       = module.messaging.credentials[principal].user_id
    }
  })
}

# Client credentials for tests, the traffic simulator and documentation flows.
resource "local_sensitive_file" "clients" {
  filename        = "${var.provisioned_dir}/keycloak/clients.json"
  content         = jsonencode(module.keycloak.clients)
  file_permission = "0400"
}

# Integration events destination for the outbox relay.
resource "local_file" "events_topic_arn" {
  filename        = "${var.provisioned_dir}/aws/events_topic_arn"
  file_permission = "0444"
  content         = module.messaging.events_topic_arn
}
