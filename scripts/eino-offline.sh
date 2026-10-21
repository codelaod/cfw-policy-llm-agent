#!/usr/bin/env bash
set -euo pipefail
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
export GOMODCACHE="$root/.cache/go-mod"
export GOCACHE="$root/.cache/go-build"
export GOTOOLCHAIN=local
mkdir -p "$GOMODCACHE" "$GOCACHE"
cd "$root/experiments/eino-offline"
action="${1:-test}"
if [[ $# -gt 0 ]]; then shift; fi
if [[ "$action" == "prepare" ]]; then
    export GOPROXY="${CFW_GO_PROXY:-https://proxy.golang.org}"
    export GOSUMDB=sum.golang.org
    go mod download
    go mod verify
    exit
fi
# Disable module lookup/checksum network access after explicit preparation.
export GOPROXY=off
export GONOPROXY=none
export GOSUMDB=off
case "$action" in
    config-check) go run -mod=readonly . -check-model-config -env-file "$root/.env.local" "$@" ;;
    test) go test -count=1 "$@" ./... ;;
    run) go run -mod=readonly . "$@" ;;
    vet) go vet "$@" ./... ;;
    verify) go mod verify ;;
    *) echo "Usage: bash scripts/eino-offline.sh {prepare|config-check|test|run|vet|verify}" >&2; exit 2 ;;
esac
