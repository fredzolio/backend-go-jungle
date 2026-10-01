#!/bin/sh
# edge apply | edge destroy | edge <terraform args>
set -eu
terraform init -reconfigure -lockfile=readonly -backend-config="path=${TF_STATE_PATH:-/state/edge-lab.tfstate}" >/dev/null
case "${1:-apply}" in
  apply|destroy) exec terraform "$1" -auto-approve ;;
  *) exec terraform "$@" ;;
esac
