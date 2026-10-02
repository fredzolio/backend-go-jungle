variable "postgres_host" {
  type    = string
  default = "postgres"
}

variable "postgres_superuser" {
  type    = string
  default = "postgres"
}

variable "postgres_superuser_password" {
  type      = string
  sensitive = true
}

variable "keycloak_url" {
  description = "Keycloak root URL reachable from the provisioner (without /auth)."
  type        = string
  default     = "http://keycloak:8080"
}

variable "keycloak_admin_user" {
  type    = string
  default = "admin"
}

variable "keycloak_admin_password" {
  type      = string
  sensitive = true
}

variable "aws_endpoint" {
  type    = string
  default = "http://ministack:4566"
}

variable "aws_region" {
  type    = string
  default = "us-east-1"
}

variable "aws_root_access_key" {
  type    = string
  default = "test"
}

variable "aws_root_secret_key" {
  type      = string
  sensitive = true
  default   = "test"
}

variable "game_providers" {
  description = "Game provider ids provisioned in the IdP and the broker."
  type        = list(string)
  default     = ["provider-a", "provider-b"]
}

variable "provisioned_dir" {
  description = "Directory (shared volume) where generated credentials are written for the services."
  type        = string
  default     = "/provisioned"
}

variable "demo_client_secret" {
  description = "Public secret of the evaluation clients (demo-internal, demo-provider-1/2); empty disables them."
  type        = string
  default     = ""
}
