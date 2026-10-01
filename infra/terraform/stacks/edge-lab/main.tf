# Public exposure of the stack on the VM edge (Caddy, managed by the zoliolab repo).
#
# Contract with the VM: a file in /etc/caddy/conf.d in the exact format of the VM's
# `preview` tool (so `preview ls` lists it), validated with the Caddy CLI pinned to
# the VM's version. This stack has its own state and never touches the VM's
# Terraform. The Makefile restarts Caddy on the host after apply/destroy.
terraform {
  required_version = "~> 1.16.0"
  backend "local" {}
  required_providers {
    local = { source = "hashicorp/local", version = "2.9.1" }
  }
}

variable "name" {
  description = "Preview name: the route is <name>.lab.fredzol.io"
  type        = string
  default     = "jungle"
}

variable "upstream" {
  description = "Stack edge (Traefik) published on the host loopback."
  type        = string
  default     = "127.0.0.1:18080"
}

variable "conf_dir" {
  type    = string
  default = "/etc/caddy/conf.d"
}

variable "caddyfile" {
  type    = string
  default = "/etc/caddy/Caddyfile"
}

locals {
  hostname = "${var.name}.lab.fredzol.io"
}

resource "local_file" "route" {
  filename        = "${var.conf_dir}/${var.name}.caddy"
  content         = "${local.hostname} {\n    reverse_proxy ${var.upstream}\n}\n"
  file_permission = "0664"
}

# The VM Caddy (2.6.2) panics when it is *reloaded* through its admin API (also
# what `systemctl reload caddy` does), so this stack never reloads it: it only
# validates the resulting configuration. `make lab-expose` restarts the service on
# the host after a successful apply (a restart re-reads the Caddyfile safely).
resource "terraform_data" "validate" {
  triggers_replace = [local_file.route.content_sha256]
  provisioner "local-exec" {
    command = "caddy validate --config ${var.caddyfile} --adapter caddyfile"
  }
}

output "url" {
  value = "https://${local.hostname}"
}
