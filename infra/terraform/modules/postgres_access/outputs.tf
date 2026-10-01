output "database" {
  value = postgresql_database.jungle.name
}

output "passwords" {
  description = "Password per role suffix (owner, app, readonly)."
  value       = { for k, v in random_password.role : k => v.result }
  sensitive   = true
}

output "roles" {
  description = "Role suffixes (non-sensitive, usable as for_each keys)."
  value       = local.roles
}
