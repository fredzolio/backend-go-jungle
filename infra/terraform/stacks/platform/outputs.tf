output "queues" {
  value = module.messaging.queues
}

output "events_topic_arn" {
  value = module.messaging.events_topic_arn
}

output "realm" {
  value = module.keycloak.realm
}

output "keycloak_clients" {
  value     = module.keycloak.clients
  sensitive = true
}

output "aws_credentials" {
  value     = module.messaging.credentials
  sensitive = true
}
