provider "postgresql" {
  host            = var.postgres_host
  port            = 5432
  username        = var.postgres_superuser
  password        = var.postgres_superuser_password
  sslmode         = "disable"
  superuser       = true
  connect_timeout = 15
}

provider "keycloak" {
  client_id = "admin-cli"
  username  = var.keycloak_admin_user
  password  = var.keycloak_admin_password
  url       = var.keycloak_url
  base_path = "/auth"
  realm     = "master"
}

# Root credentials of the emulator; application components never use them.
provider "aws" {
  region                      = var.aws_region
  access_key                  = var.aws_root_access_key
  secret_key                  = var.aws_root_secret_key
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true
  endpoints {
    sqs = var.aws_endpoint
    sns = var.aws_endpoint
    iam = var.aws_endpoint
    sts = var.aws_endpoint
  }
}
