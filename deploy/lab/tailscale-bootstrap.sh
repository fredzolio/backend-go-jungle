#!/usr/bin/env bash
# One-off Tailscale setup for the CI deploy path (run by a human/orchestrator, not by CI).
#
#   TS_API_KEY=<short-lived API access token> deploy/lab/tailscale-bootstrap.sh [--apply]
#
# Without --apply: fetch the tailnet policy, compute the change and print a unified diff.
# With --apply: validate, POST the policy (If-Match ETag), create a federated identity for
# GitHub Actions (env `lab`) and store TS_OAUTH_CLIENT_ID / TS_AUDIENCE as env variables.
# NOTE: the policy is fetched as JSON, so HuJSON comments of the current policy are lost
# on write; review the diff first.
set -Eeuo pipefail

API=https://api.tailscale.com/api/v2
REPO=fredzolio/backend-go-jungle
VM_ADDR=100.86.214.26
SSH_PORT=2222
APPLY=0
[[ "${1:-}" == --apply ]] && APPLY=1
[[ -n "${TS_API_KEY:-}" ]] || { echo "TS_API_KEY is not set" >&2; exit 1; }
for tool in curl jq diff; do command -v "$tool" > /dev/null || { echo "missing tool: $tool" >&2; exit 1; }; done

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# api <method> <path> <out-file> [curl args...]: the key goes through a curl config on stdin,
# never through argv. Non-2xx prints the response body and aborts.
api() {
  local method=$1 path=$2 out=$3 code
  shift 3
  code=$(printf 'header = "Authorization: Bearer %s"\n' "$TS_API_KEY" |
    curl -sS -K - -X "$method" -o "$out" -w '%{http_code}' "$@" "$API$path")
  if [[ ! "$code" =~ ^2 ]]; then
    echo "Tailscale API $method $path failed with HTTP $code:" >&2
    cat "$out" >&2
    exit 1
  fi
}

echo "Fetching current policy"
api GET /tailnet/-/acl "$work/current.json" -H 'Accept: application/json' -D "$work/headers"
etag=$(tr -d '\r' < "$work/headers" | sed -n 's/^[Ee][Tt]ag: *//p' | head -n1)
[[ -n "$etag" ]] || { echo "no ETag in policy response" >&2; exit 1; }

# shellcheck disable=SC2016 # jq program, not shell
jq -S --arg dst "$VM_ADDR:$SSH_PORT" --arg vm "$VM_ADDR" '
  def narrow: .src |= map(if . == "*" then "autogroup:member" else . end);
  .tagOwners["tag:ci"] = ["autogroup:admin"]
  | .acls = ((.acls // []) | map(narrow))
  | (if has("grants") then .grants |= map(narrow) else . end)
  | .acls |= (if any(.[]; .src == ["tag:ci"] and .dst == [$dst]) then .
              else . + [{action: "accept", src: ["tag:ci"], dst: [$dst]}] end)
  | .tests = ((.tests // []) | if any(.[]; .src == "tag:ci") then .
              else . + [{src: "tag:ci", accept: [$dst], deny: [($vm + ":22"), ($vm + ":443"), ($vm + ":5432")]}] end)
' "$work/current.json" > "$work/new.json"
jq -S . "$work/current.json" > "$work/current.sorted.json"

echo "Policy diff (current -> new):"
diff -u "$work/current.sorted.json" "$work/new.json" || true
if cmp -s "$work/current.sorted.json" "$work/new.json"; then
  echo "(policy already up to date)"
fi

if ((APPLY == 0)); then
  echo "Dry run only. Re-run with --apply to push the policy and create the federated identity."
  exit 0
fi

echo "Validating new policy"
api POST /tailnet/-/acl/validate "$work/validate.out" -H 'Content-Type: application/json' --data-binary @"$work/new.json"
echo "Applying new policy"
api POST /tailnet/-/acl "$work/apply.out" -H 'Content-Type: application/json' -H "If-Match: $etag" --data-binary @"$work/new.json"

echo "Creating federated identity"
jq -n --arg subject "repo:$REPO:environment:lab" '{
  keyType: "federated",
  description: "GitHub Actions fredzolio/backend-go-jungle env lab",
  scopes: ["auth_keys"],
  tags: ["tag:ci"],
  issuer: "https://token.actions.githubusercontent.com",
  subject: $subject}' > "$work/key-req.json"
api POST /tailnet/-/keys "$work/key.json" -H 'Content-Type: application/json' --data-binary @"$work/key-req.json"
id=$(jq -er '.id' "$work/key.json")
audience=$(jq -er '.audience' "$work/key.json")
echo "federated identity id=$id audience=$audience"

gh variable set TS_OAUTH_CLIENT_ID --env lab --repo "$REPO" --body "$id"
gh variable set TS_AUDIENCE --env lab --repo "$REPO" --body "$audience"
echo "Set TS_OAUTH_CLIENT_ID and TS_AUDIENCE on GitHub environment lab."
