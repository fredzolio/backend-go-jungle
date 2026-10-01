output "realm" {
  value = keycloak_realm.jungle.realm
}

output "clients" {
  description = "client_id => { client_secret, provider_id }"
  value = {
    for id, c in local.clients : id => {
      client_secret = random_password.client[id].result
      provider_id   = c.provider_id
    }
  }
  sensitive = true
}
