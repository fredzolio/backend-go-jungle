output "realm" {
  value = keycloak_realm.jungle.realm
}

output "clients" {
  description = "client_id => { client_secret, provider_id }"
  value = {
    for id, c in local.clients : id => {
      client_secret = local.secrets[id]
      provider_id   = c.provider_id
    }
  }
  sensitive = true
}
