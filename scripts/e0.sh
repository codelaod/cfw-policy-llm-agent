#!/usr/bin/env bash
set -euo pipefail
task_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
export GOMODCACHE="$task_root/.cache/go-mod"
export GOCACHE="$task_root/.cache/go-build"
export GOPATH="$task_root/.cache/go"
export CGO_ENABLED=0
case "${1:-help}" in
  migrate)
    cd "$task_root/server"
    exec go run -mod=readonly ./cmd/server --migrate
    ;;
  server)
    cd "$task_root/server"
    exec go run -mod=readonly ./cmd/server
    ;;
  test)
    cd "$task_root/server"
    exec go test -mod=readonly ./... -count=1
    ;;
  build)
    cd "$task_root/server"
    mkdir -p "$task_root/runtime/bin"
    exec go build -mod=readonly -o "$task_root/runtime/bin/cfw-e0" ./cmd/server
    ;;
  api)
    cd "$task_root/server"
    exec go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g main.go -d cmd/server,internal/httpapi,internal/storage,internal/app,internal/domain,internal/recheck,internal/agentquery --output ../docs/api --outputTypes json,yaml --parseInternal
    ;;
  *)
    printf '%s\n' 'Usage: bash scripts/e0.sh {migrate|server|test|build|api}'
    ;;
esac
