#!/usr/bin/env bash
# Rolling deploy of the Jungle API on the lab VM, with smoke test and automatic rollback.
#
#   deploy.sh <image_ref> <git_sha>
#
# Called by ssh-entry (after signature + main-ancestry checks) or manually from the VM
# (`make lab-deploy IMAGE=... SHA=...`). A ref without a registry host (e.g. jungle/api:x)
# is treated as a locally built image and never pulled.
#
# Never touches Caddy, the edge-lab/obs profiles, volumes, `down` or `--remove-orphans`.
# Migrations follow expand/contract: they stay compatible with the previous version and
# are NOT rolled back.
set -Eeuo pipefail

IMAGE_REF=${1:?usage: deploy.sh <image_ref> <git_sha>}
GIT_SHA=${2:?usage: deploy.sh <image_ref> <git_sha>}
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
STATE_DIR=${JUNGLE_STATE_DIR:-$HOME/.local/state/jungle-deploy}
EDGE_URL=http://127.0.0.1:18080
TRAEFIK_API=http://127.0.0.1:18090/api/http/services/jungle-api@file
ROLLBACK_TAG=jungle/api:rollback
SMOKE_IMAGE=golang:1.27.1
export PATH="$HOME/.local/bin:$PATH" SOPS_AGE_KEY_FILE=${SOPS_AGE_KEY_FILE:-$HOME/.config/sops/age/keys.txt}

cd "$ROOT"
mkdir -p "$STATE_DIR"
log() { printf '%s [deploy] %s\n' "$(date -u +%FT%TZ)" "$*" || true; }
compose() { docker compose --env-file .env.lab "$@"; }
is_local_ref() { [[ "${1%%/*}" != *.* && "${1%%/*}" != *:* && "${1%%/*}" != localhost ]] || [[ "$1" != */* ]]; }
http_code() { curl -s -o /dev/null -m 5 -w '%{http_code}' "$1" || true; }

ROLLBACK_ARMED=0
HAVE_ROLLBACK=0
PREV_SHA=""
[[ -f "$STATE_DIR/current" ]] && PREV_SHA=$(sed -n 's/^sha=//p' "$STATE_DIR/current")

# Waits until Traefik reports api-<n> UP and the edge answers ready several times in a row.
wait_edge_up() {
  local n=$1 deadline=$((SECONDS + 90)) ok=0
  while ((SECONDS < deadline)); do
    if curl -fsS -m 3 "$TRAEFIK_API" 2>/dev/null | tr -d ' \n' | grep -qF "\"http://api-$n:8080\":\"UP\"" &&
      [[ $(http_code "$EDGE_URL/health/ready") == 200 ]]; then
      ok=$((ok + 1))
      ((ok >= 3)) && return 0
    else
      ok=0
    fi
    sleep 1
  done
  log "api-$n never became ready behind the edge"
  return 1
}

# roll_api <image> [--force-recreate]: api-1 -> api-2 -> api-3, one at a time.
roll_api() {
  local image=$1 force=${2:-} n
  for n in 1 2 3; do
    log "rolling api-$n -> $image"
    # shellcheck disable=SC2086 # $force is empty or one flag
    JUNGLE_API_IMAGE=$image compose up -d --no-deps --no-build --wait --wait-timeout 120 $force "api-$n"
    wait_edge_up "$n"
  done
}

# verify_running <image>: every api container runs that image and health is OK locally + publicly.
verify_running() {
  local image=$1 want got n
  want=$(docker image inspect -f '{{.Id}}' "$image")
  for n in 1 2 3; do
    got=$(docker inspect -f '{{.Image}}' "jungle-api-$n-1")
    [[ "$got" == "$want" ]] || { log "api-$n runs $got, expected $want"; return 1; }
  done
  [[ $(http_code "$EDGE_URL/health/ready") == 200 ]] || { log "local /health/ready != 200"; return 1; }
  [[ $(http_code "$PUBLIC_URL/health/ready") == 200 ]] || { log "public /health/ready != 200"; return 1; }
}

smoke_e2e() {
  log "smoke: e2e suite against $PUBLIC_URL"
  docker run --rm --network host \
    -v "$ROOT":/src:ro -w /src \
    -v jungle_provisioned:/provisioned:ro \
    -v jungle_deploy_gomod:/go/pkg/mod -v jungle_deploy_gocache:/root/.cache/go-build \
    -e GOTOOLCHAIN=local -e JUNGLE_BASE_URL="$PUBLIC_URL" -e JUNGLE_PROVISIONED_DIR=/provisioned \
    "$SMOKE_IMAGE" go test -tags=e2e -count=1 ./test/e2e/...
}

write_state() {
  local ts; ts=$(date -u +%FT%TZ)
  [[ -f "$STATE_DIR/current" ]] && cp "$STATE_DIR/current" "$STATE_DIR/previous"
  printf 'sha=%s\nimage=%s\nts=%s\n' "$GIT_SHA" "$IMAGE_REF" "$ts" > "$STATE_DIR/current.tmp"
  mv "$STATE_DIR/current.tmp" "$STATE_DIR/current"
}

on_error() {
  local rc=$?
  trap - ERR
  set +e
  if ((ROLLBACK_ARMED == 0 || HAVE_ROLLBACK == 0)); then
    log "FAILED (rc=$rc) before any change to the API fleet or with no rollback image"
    echo "DEPLOY_RESULT=failed"
    exit 1
  fi
  log "FAILED (rc=$rc): rolling api-1..3 back to the previous image"
  # No set -e here: report the real outcome instead of aborting mid-rollback.
  if roll_api "$ROLLBACK_TAG" && verify_running "$ROLLBACK_TAG"; then
    echo "DEPLOY_RESULT=rolled_back"
  else
    echo "DEPLOY_RESULT=rollback_failed"
  fi
  exit 1
}
trap on_error ERR

# 1. Secrets: decrypted next to the compose file, readable by the owner only.
log "decrypting deploy/lab/lab.enc.env"
(umask 077 && sops decrypt --input-type dotenv --output-type dotenv deploy/lab/lab.enc.env > .env.lab)
PUBLIC_URL=$(sed -n 's/^PUBLIC_BASE_URL=//p' .env.lab)
PUBLIC_URL=${PUBLIC_URL%/}
: "${PUBLIC_URL:?PUBLIC_BASE_URL missing from lab.enc.env}"

# 2. Image + rollback anchor (taken before anything is touched).
if is_local_ref "$IMAGE_REF"; then
  docker image inspect "$IMAGE_REF" > /dev/null
elif ! docker image inspect "$IMAGE_REF" > /dev/null 2>&1; then
  log "pulling $IMAGE_REF"
  docker pull "$IMAGE_REF"
fi
if current_id=$(docker inspect -f '{{.Image}}' jungle-api-1-1 2> /dev/null) && docker tag "$current_id" "$ROLLBACK_TAG" 2> /dev/null; then
  HAVE_ROLLBACK=1
  log "rollback anchor: $ROLLBACK_TAG = $current_id"
else
  log "WARNING: no usable running api-1 image: rollback unavailable for this deploy"
fi

# 3-5. Infra images are built locally; infra is converged without touching Caddy.
# A rebuild yields a new image ID even from cache, which would needlessly recreate Keycloak
# on every deploy: rebuild only when the sources changed (or the images are missing).
if [[ -n "$PREV_SHA" ]] && git cat-file -e "$PREV_SHA^{commit}" 2> /dev/null &&
  docker image inspect jungle/keycloak:local jungle/provisioner:local > /dev/null 2>&1 &&
  git diff --quiet "$PREV_SHA" "$GIT_SHA" -- infra/keycloak infra/terraform; then
  log "infra images unchanged since $PREV_SHA: not rebuilding"
else
  log "building infra images"
  compose build keycloak provisioner
fi
log "converging infra (postgres keycloak ministack edge)"
compose up -d --no-build --wait postgres keycloak ministack edge
log "provisioning (idempotent Terraform; also heals a MiniStack that lost state)"
compose run --rm provisioner

# 6. From here on a failure triggers the automatic rollback.
ROLLBACK_ARMED=1
log "migrating"
JUNGLE_API_IMAGE=$IMAGE_REF compose run --rm --no-deps migrate

# 7-8. Rolling update and verification.
roll_api "$IMAGE_REF" --force-recreate
verify_running "$IMAGE_REF"

# 9. Smoke test through the public URL.
smoke_e2e

write_state
if [[ -n "$PREV_SHA" ]] && git cat-file -e "$PREV_SHA^{commit}" 2> /dev/null &&
  ! git diff --quiet "$PREV_SHA" "$GIT_SHA" -- infra/terraform/stacks/edge-lab/; then
  log "WARNING: infra/terraform/stacks/edge-lab/ changed since $PREV_SHA: run 'make lab-expose' manually (deploy never touches Caddy)"
fi
echo "DEPLOY_RESULT=success"
echo "sha=$GIT_SHA"
echo "image=$IMAGE_REF"
echo "public=$PUBLIC_URL"
docker ps --filter name=jungle-api --format '{{.Names}} {{.Status}}'
