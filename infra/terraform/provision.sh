#!/bin/sh
# Entry point of the provisioner container.
#   provision                 init + apply the platform stack (idempotent; runs on every `compose up`)
#   provision <terraform args> run an arbitrary terraform command against the same state
#                              e.g. `docker compose run --rm provisioner output -json queues`
set -eu

STATE_PATH="${TF_STATE_PATH:-/state/platform.tfstate}"
PROVISIONED_DIR="${TF_VAR_provisioned_dir:-/provisioned}"
APP_UID="${APP_UID:-65532}"

terraform init -reconfigure -lockfile=readonly -backend-config="path=${STATE_PATH}" >/dev/null

if [ "$#" -gt 0 ]; then
  exec terraform "$@"
fi

# Dependencies can report healthy slightly before they accept every request
# (e.g. Keycloak's bootstrap filter answers 503); retry a few times.
attempt=1
until terraform apply -auto-approve -lock-timeout=60s; do
  if [ "$attempt" -ge 5 ]; then
    echo "provisioning failed after ${attempt} attempts" >&2
    exit 1
  fi
  echo "apply failed (attempt ${attempt}); retrying in $((attempt * 5))s" >&2
  sleep $((attempt * 5))
  attempt=$((attempt + 1))
done
# Services run as the distroless nonroot user; hand them the generated files.
chown -R "${APP_UID}:${APP_UID}" "${PROVISIONED_DIR}"
echo "provisioning complete"
