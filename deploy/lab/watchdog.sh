#!/usr/bin/env bash
# Lab watchdog (systemd --user timer, every minute). Narrow on purpose: it only heals the
# known failure mode where MiniStack loses its in-memory state (queues/topic/IAM gone) and
# every API reports readiness DOWN on the `sqs` check. Recovery = `make recover-broker`:
# start MiniStack, re-apply the Terraform platform stack, restart the APIs.
#
# It never acts when: a deploy holds the lock; the stack is intentionally down (no api
# container running); readiness fails for any other reason (logged, left to a human);
# it already recovered in the last RECOVERY_COOLDOWN seconds.
set -Eeuo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
STATE=${JUNGLE_STATE_DIR:-$HOME/.local/state/jungle-deploy}
READY_URL=${WATCHDOG_READY_URL:-http://127.0.0.1:18080/health/ready}
FAILS_BEFORE_ACTION=${WATCHDOG_FAILS:-3}
RECOVERY_COOLDOWN=${WATCHDOG_COOLDOWN:-600}
mkdir -p "$STATE"
LOG=$STATE/watchdog.log
log() { printf '%s %s\n' "$(date -u +%FT%TZ)" "$*" | tee -a "$LOG"; }
compose() { docker compose --env-file .env.lab "$@"; }
ready() { [[ "$(curl -s -o /dev/null -m 5 -w '%{http_code}' "$READY_URL" || true)" == 200 ]]; }

# Same lock as ssh-entry: never interfere with a deploy or rollback in progress.
exec 9> "$STATE/lock"
flock -n 9 || exit 0

if ready; then
  rm -f "$STATE/watchdog.fails"
  exit 0
fi
fails=$(($(cat "$STATE/watchdog.fails" 2> /dev/null || echo 0) + 1))
echo "$fails" > "$STATE/watchdog.fails"
((fails >= FAILS_BEFORE_ACTION)) || exit 0

if [[ -z "$(docker ps -q --filter label=com.docker.compose.project=jungle --filter name=jungle-api-)" ]]; then
  log "not ready but no api container is running (stack intentionally down?) - nothing to do"
  rm -f "$STATE/watchdog.fails"
  exit 0
fi

# Broker state loss: recent `sqs` readiness failures that are not shutdown cancellations.
broker_lost=0
for n in 1 2 3; do
  failures=$(docker logs --since 3m "jungle-api-$n-1" 2>&1 |
    grep '"msg":"readiness probe failed"' | grep '"check":"sqs"' || true)
  if [[ -n "$failures" ]] && grep -qv 'context canceled' <<< "$failures"; then
    broker_lost=1
  fi
done
if ((broker_lost == 0)); then
  if ((fails % 15 == FAILS_BEFORE_ACTION % 15)); then log "not ready for ${fails} min, cause is not the broker - not acting"; fi
  exit 0
fi

last=$(cat "$STATE/watchdog.last-recovery" 2> /dev/null || echo 0)
if (($(date +%s) - last < RECOVERY_COOLDOWN)); then
  log "broker still unhealthy but last recovery was under ${RECOVERY_COOLDOWN}s ago - waiting"
  exit 0
fi
date +%s > "$STATE/watchdog.last-recovery"

log "broker state lost (sqs readiness DOWN for ${fails} checks) - recovering"
cd "$ROOT"
[[ -f .env.lab ]] || { log "missing $ROOT/.env.lab - cannot recover"; exit 1; }
{
  compose up -d --no-build ministack
  compose run --rm provisioner
  compose restart api-1 api-2 api-3
} >> "$LOG" 2>&1 || { log "recovery commands failed"; exit 1; }

for _ in $(seq 1 30); do
  if ready; then
    log "recovered: readiness UP"
    rm -f "$STATE/watchdog.fails"
    exit 0
  fi
  sleep 3
done
log "recovery ran but readiness is still DOWN"
exit 1
