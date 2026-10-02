#!/usr/bin/env bash
# Forced command of the CI deploy key (installed to ~/.local/libexec/jungle-deploy/ssh-entry).
#
# Accepted commands (anything else: exit 2):
#   deploy <40-hex git sha> ghcr.io/fredzolio/backend-go-jungle@sha256:<64-hex>
#   rollback          redeploy the previous successful release
#   status
#
# deploy/rollback only proceed when the commit is an ancestor of origin/main AND the image
# carries a keyless cosign signature from a main-branch workflow of this repository.
# There is deliberately no bypass flag. An optional GHCR token is read from stdin and used
# only for an ephemeral docker login.
set -Eeuo pipefail

STATE_DIR=$HOME/.local/state/jungle-deploy
CHECKOUT=${JUNGLE_DEPLOY_CHECKOUT:-/home/zolio/deploy/backend-go-jungle}
AUDIT_LOG=$STATE_DIR/audit.log
IMAGE_RE='^ghcr\.io/fredzolio/backend-go-jungle@sha256:[0-9a-f]{64}$'
SIGNER_RE='^https://github\.com/fredzolio/backend-go-jungle/\.github/workflows/.+@refs/heads/main$'
OIDC_ISSUER=https://token.actions.githubusercontent.com
export PATH="$HOME/.local/bin:/usr/local/bin:/usr/bin:/bin"

mkdir -p "$STATE_DIR"
chmod 700 "$STATE_DIR"
client_ip=${SSH_CONNECTION:-local}
client_ip=${client_ip%% *}
cmd=${SSH_ORIGINAL_COMMAND:-}
audit() { printf '%s client=%s result=%s command=%q\n' "$(date -u +%FT%TZ)" "${client_ip:-local}" "$1" "$cmd" >> "$AUDIT_LOG"; }
reject() { audit "rejected: $1"; echo "rejected: $1" >&2; exit 2; }

if [[ "$cmd" == status ]]; then
  audit accepted
  for f in current previous; do
    echo "== $f"
    cat "$STATE_DIR/$f" 2> /dev/null || echo "(none)"
  done
  echo "== containers"
  for n in 1 2 3; do
    docker inspect -f '{{.Name}} {{.Config.Image}} {{.Image}} {{.State.Health.Status}}' "jungle-api-$n-1" 2>&1 || true
  done
  exit 0
fi

sha="" image=""
if [[ "$cmd" =~ ^deploy\ ([0-9a-f]{40})\ (ghcr\.io/[A-Za-z0-9._/-]+@sha256:[0-9a-f]{64})$ ]]; then
  sha=${BASH_REMATCH[1]}
  image=${BASH_REMATCH[2]}
  [[ "$image" =~ $IMAGE_RE ]] || reject "image repository not allowed"
elif [[ "$cmd" == rollback ]]; then
  prev=$STATE_DIR/previous
  [[ -f "$prev" ]] || reject "no previous release recorded"
  sha=$(sed -n 's/^sha=//p' "$prev")
  image=$(sed -n 's/^image=//p' "$prev")
  [[ "$sha" =~ ^[0-9a-f]{40}$ && "$image" =~ $IMAGE_RE ]] || reject "previous release is not a signed registry image"
else
  reject "unknown command"
fi
audit accepted

# Serialize deploys; wait up to 10 minutes for a running one.
exec 9> "$STATE_DIR/lock"
flock -w 600 9 || { echo "another deploy holds the lock" >&2; exit 3; }

# Ephemeral docker credentials, removed on exit.
DOCKER_CONFIG=$(mktemp -d)
export DOCKER_CONFIG
trap 'rm -rf "$DOCKER_CONFIG"' EXIT
[[ -d "$HOME/.docker/cli-plugins" ]] && ln -s "$HOME/.docker/cli-plugins" "$DOCKER_CONFIG/cli-plugins"
token=""
if [[ "$cmd" != rollback ]]; then
  IFS= read -r -t 10 token || true
fi
if [[ -n "$token" ]]; then
  printf '%s' "$token" | docker login ghcr.io -u x-access-token --password-stdin > /dev/null
fi
unset token

cd "$CHECKOUT"
git fetch --prune --quiet origin
git merge-base --is-ancestor "$sha" origin/main || { echo "commit $sha is not on origin/main" >&2; audit "refused: not on main"; exit 2; }
echo "verifying signature of $image"
cosign verify "$image" \
  --certificate-identity-regexp "$SIGNER_RE" \
  --certificate-oidc-issuer "$OIDC_ISSUER" > /dev/null || { echo "signature verification failed" >&2; audit "refused: bad signature"; exit 2; }
git checkout --quiet --detach "$sha"

# Run detached from the client: a dropped SSH session must not abort a half-finished roll.
# (Not `exec`: the lock and the credentials trap must outlive deploy.sh.)
trap '' HUP PIPE
log=$STATE_DIR/last-deploy.log
: > "$log"
"$CHECKOUT/deploy/lab/deploy.sh" "$image" "$sha" > "$log" 2>&1 &
pid=$!
tail -n +1 -f --pid="$pid" "$log" || true
rc=0
wait "$pid" || rc=$?
audit "finished rc=$rc"
exit "$rc"
