#!/usr/bin/env bash
# Idempotent setup of the lab deploy channel for the current (non-root) user:
#   - non-root sshd on <tailscale ip>:2222 as a systemd --user unit
#   - authorized_keys with ONE restricted line for the CI key (forced command, tailnet-only)
#   - ssh-entry forced command, cosign (pinned, sha256-verified), deploy checkout
# Pre-existing and never regenerated: ~/.config/jungle-deploy/{ssh_host_ed25519_key,ci_client_ed25519.pub}
set -Eeuo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
CONF=$HOME/.config/jungle-deploy
STATE=$HOME/.local/state/jungle-deploy
LIBEXEC=$HOME/.local/libexec/jungle-deploy
BIN=$HOME/.local/bin
UNIT_DIR=$HOME/.config/systemd/user
LISTEN_ADDR=${JUNGLE_DEPLOY_LISTEN_ADDR:-100.86.214.26}
LISTEN_PORT=${JUNGLE_DEPLOY_LISTEN_PORT:-2222}
CHECKOUT=${JUNGLE_DEPLOY_CHECKOUT:-$HOME/deploy/backend-go-jungle}
REPO_URL=https://github.com/fredzolio/backend-go-jungle.git
COSIGN_VERSION=v3.1.3
ENTRY=$LIBEXEC/ssh-entry
export XDG_RUNTIME_DIR=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}

log() { printf '[bootstrap] %s\n' "$*"; }

[[ -f "$CONF/ssh_host_ed25519_key" ]] || { echo "missing host key $CONF/ssh_host_ed25519_key" >&2; exit 1; }
[[ -f "$CONF/ci_client_ed25519.pub" ]] || { echo "missing CI public key $CONF/ci_client_ed25519.pub" >&2; exit 1; }
install -d -m 700 "$CONF" "$STATE" "$HOME/.config/jungle-deploy"
install -d -m 755 "$LIBEXEC" "$BIN" "$UNIT_DIR" "$(dirname "$CHECKOUT")"

log "installing forced command $ENTRY"
install -m 0755 "$HERE/ssh-entry.sh" "$ENTRY"

log "rendering sshd_config"
sed -e "s|@HOME@|$HOME|g" -e "s|@USER@|$(id -un)|g" \
  -e "s|@LISTEN_ADDR@|$LISTEN_ADDR|g" -e "s|@LISTEN_PORT@|$LISTEN_PORT|g" \
  "$HERE/sshd_config.tmpl" > "$CONF/sshd_config.new"
mv "$CONF/sshd_config.new" "$CONF/sshd_config"
chmod 600 "$CONF/sshd_config"

log "writing authorized_keys (single CI key)"
pubkey=$(cut -d' ' -f1,2 "$CONF/ci_client_ed25519.pub")
(umask 077 && printf 'restrict,from="100.64.0.0/10,fd7a:115c:a1e0::/48",command="%s" %s\n' "$ENTRY" "$pubkey" > "$CONF/authorized_keys.new")
mv "$CONF/authorized_keys.new" "$CONF/authorized_keys"

if [[ ! -x "$BIN/cosign" ]]; then
  log "installing cosign $COSIGN_VERSION"
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  base=https://github.com/sigstore/cosign/releases/download/$COSIGN_VERSION
  curl -fsSL -o "$tmp/cosign-linux-amd64" "$base/cosign-linux-amd64"
  curl -fsSL -o "$tmp/cosign_checksums.txt" "$base/cosign_checksums.txt"
  (cd "$tmp" && grep ' cosign-linux-amd64$' cosign_checksums.txt | sha256sum -c -)
  install -m 0755 "$tmp/cosign-linux-amd64" "$BIN/cosign"
fi

if [[ ! -d "$CHECKOUT/.git" ]]; then
  log "cloning deploy checkout $CHECKOUT"
  git clone "$REPO_URL" "$CHECKOUT"
fi

log "validating sshd configuration"
/usr/sbin/sshd -t -f "$CONF/sshd_config"

log "enabling user unit"
install -m 0644 "$HERE/jungle-deploy-sshd.service" "$UNIT_DIR/jungle-deploy-sshd.service"
systemctl --user daemon-reload
systemctl --user enable jungle-deploy-sshd.service
systemctl --user restart jungle-deploy-sshd.service
log "enabling lab watchdog timer"
install -m 0644 "$HERE/jungle-lab-watchdog.service" "$HERE/jungle-lab-watchdog.timer" "$UNIT_DIR/"
systemctl --user daemon-reload
systemctl --user enable --now jungle-lab-watchdog.timer
sleep 1
systemctl --user --no-pager --lines=5 status jungle-deploy-sshd.service || true
systemctl --user --no-pager list-timers jungle-lab-watchdog.timer || true
ss -ltnH "sport = :$LISTEN_PORT" || true
