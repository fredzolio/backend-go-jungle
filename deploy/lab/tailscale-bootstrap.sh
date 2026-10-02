#!/usr/bin/env bash
# One-off Tailscale setup for the CI deploy path (run by a human, never by CI).
#
#   TS_API_KEY=<short-lived API access token> deploy/lab/tailscale-bootstrap.sh [--apply]
#
# Idempotent. Without --apply it only prints the policy diff and what would be created.
#   1. Policy (HuJSON, edited textually so comments survive): tagOwners tag:ci; the default
#      allow-all grant is narrowed to members (+ tag:homelab) so CI tags do not inherit it;
#      tag:ci may only reach the deploy sshd (VM_ADDR tcp:SSH_PORT); a policy test pins that.
#   2. Federated identity (GitHub OIDC, env `lab`, tag:ci, scope auth_keys) — no secret is
#      stored in GitHub; TS_OAUTH_CLIENT_ID / TS_AUDIENCE become env `lab` variables.
# Aborts if the policy does not have the expected shape (e.g. allow-all line not found).
set -Eeuo pipefail

API=https://api.tailscale.com/api/v2
REPO=fredzolio/backend-go-jungle
export VM_ADDR=100.86.214.26 SSH_PORT=2222
APPLY=0
[[ "${1:-}" == --apply ]] && APPLY=1
[[ -n "${TS_API_KEY:-}" ]] || { echo "TS_API_KEY is not set" >&2; exit 1; }
for tool in curl jq python3 diff gh; do command -v "$tool" > /dev/null || { echo "missing tool: $tool" >&2; exit 1; }; done
# The repo uses GitHub's immutable OIDC subject (repo:<owner>@<id>/<repo>@<id>:...), so the
# prefix is read from GitHub instead of being assumed.
SUBJECT="$(gh api "repos/$REPO/actions/oidc/customization/sub" --jq .sub_claim_prefix):environment:lab"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# api <method> <path> <out-file> [curl args...]: the key goes through a curl config on stdin,
# never through argv. Non-2xx prints the response body and aborts.
api() {
  local method=$1 path=$2 out=$3 code
  shift 3
  code=$(printf 'user = "%s:"\n' "$TS_API_KEY" |
    curl -sS -K - -X "$method" -o "$out" -w '%{http_code}' "$@" "$API$path")
  if [[ ! "$code" =~ ^2 ]]; then
    echo "Tailscale API $method $path failed with HTTP $code:" >&2
    cat "$out" >&2
    exit 1
  fi
}

echo "== policy"
api GET /tailnet/-/acl "$work/current.hujson" -H 'Accept: application/hujson' -D "$work/headers"
etag=$(tr -d '\r' < "$work/headers" | sed -n 's/^[Ee][Tt]ag: *//p' | head -n1)
[[ -n "$etag" ]] || { echo "no ETag in policy response" >&2; exit 1; }

python3 - "$work/current.hujson" "$work/new.hujson" << 'EOF'
import os, sys
src, dst = sys.argv[1], sys.argv[2]
vm, port = os.environ["VM_ADDR"], os.environ["SSH_PORT"]
s = open(src).read()

def once(old, new, what):
    global s
    if s.count(old) != 1:
        sys.exit(f"policy shape not recognised ({what}); edit it by hand")
    s = s.replace(old, new)

if '"tag:ci"' not in s:
    once('"tagOwners": {', '"tagOwners": {\n\t\t// GitHub Actions runners (' + "fredzolio/backend-go-jungle"
         + ', env lab) via OIDC federated identity.\n\t\t"tag:ci": ["autogroup:admin"],', "tagOwners")
if '"src": ["tag:ci"]' not in s:
    once('{"src": ["*"], "dst": ["*"], "ip": ["*"]},',
         '// Members (and homelab tags) keep unrestricted access; CI tags do NOT inherit it.\n'
         '\t\t{"src": ["autogroup:member", "tag:homelab"], "dst": ["*"], "ip": ["*"]},\n\n'
         '\t\t// CI deploy: only the restricted deploy sshd (forced command) on zoliolab-server.\n'
         f'\t\t{{"src": ["tag:ci"], "dst": ["{vm}"], "ip": ["tcp:{port}"]}},', "default allow-all grant")
if '"src":    "tag:ci"' not in s:
    test = ('\t"tests": [\n\t\t{\n\t\t\t"src":    "tag:ci",\n'
            f'\t\t\t"accept": ["{vm}:{port}"],\n'
            f'\t\t\t"deny":   ["{vm}:22", "{vm}:443", "{vm}:15432"],\n\t\t}},\n\t],\n')
    once("\t// Test access rules every time they're saved.\n",
         "\t// Test access rules every time they're saved.\n" + test, "tests anchor")
open(dst, "w").write(s)
EOF

if cmp -s "$work/current.hujson" "$work/new.hujson"; then
  echo "policy already up to date"
  policy_changed=0
else
  diff -u "$work/current.hujson" "$work/new.hujson" || true
  policy_changed=1
fi

echo "== federated identity ($SUBJECT)"
# all=true: without it only the caller's own keys are listed (federated identities are not).
api GET '/tailnet/-/keys?all=true' "$work/keys.json"
existing=$(jq -r --arg s "$SUBJECT" \
  '[.keys[]? | select(.keyType == "federated" and .subject == $s) | .id][0] // empty' "$work/keys.json")
[[ -n "$existing" ]] && echo "already exists: $existing"

if ((APPLY == 0)); then
  echo "Dry run only. Re-run with --apply."
  exit 0
fi

if ((policy_changed)); then
  api POST /tailnet/-/acl/validate "$work/validate.out" -H 'Content-Type: application/hujson' --data-binary @"$work/new.hujson"
  jq -e 'length == 0' "$work/validate.out" > /dev/null || { cat "$work/validate.out" >&2; exit 1; }
  api POST /tailnet/-/acl "$work/apply.out" -H 'Content-Type: application/hujson' -H "If-Match: $etag" --data-binary @"$work/new.hujson"
  echo "policy applied"
fi

if [[ -z "$existing" ]]; then
  jq -n --arg subject "$SUBJECT" '{keyType: "federated", description: "GHA backend-go-jungle env lab",
    scopes: ["auth_keys"], tags: ["tag:ci"], issuer: "https://token.actions.githubusercontent.com",
    subject: $subject}' > "$work/key-req.json"
  api POST /tailnet/-/keys "$work/key.json" -H 'Content-Type: application/json' --data-binary @"$work/key-req.json"
  existing=$(jq -er .id "$work/key.json")
fi
api GET "/tailnet/-/keys/$existing" "$work/key.json"
gh variable set TS_OAUTH_CLIENT_ID --env lab --repo "$REPO" --body "$existing"
gh variable set TS_AUDIENCE --env lab --repo "$REPO" --body "$(jq -er .audience "$work/key.json")"
echo "TS_OAUTH_CLIENT_ID / TS_AUDIENCE set on GitHub environment lab"
