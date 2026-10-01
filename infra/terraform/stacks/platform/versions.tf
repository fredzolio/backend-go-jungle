terraform {
  required_version = "~> 1.16.0"

  # State path is supplied at init time (-backend-config=path=...) by provision.sh,
  # pointing at the jungle_tfstate volume. This stack is never applied from the host.
  backend "local" {}

  required_providers {
    aws        = { source = "hashicorp/aws", version = "6.67.0" }
    keycloak   = { source = "keycloak/keycloak", version = "5.9.0" }
    postgresql = { source = "cyrilgdn/postgresql", version = "1.27.0" }
    random     = { source = "hashicorp/random", version = "3.9.1" }
    local      = { source = "hashicorp/local", version = "2.9.1" }
  }
}
