#!/usr/bin/env bash
set -euo pipefail
task_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
export GOMODCACHE="$task_root/.cache/go-mod"
export GOCACHE="$task_root/.cache/go-build"
export GOPATH="$task_root/.cache/go"
export CGO_ENABLED=0
task_caller_dir="$PWD"
task_args=()
append_explicit_path() {
  local task_value="$1" task_prefix="${2-}"
  if [[ -n "$task_value" && "$task_value" != /* ]]; then
    task_value="$task_caller_dir/$task_value"
  fi
  task_args+=("$task_prefix$task_value")
}
task_mode="${1:-help}"
if (( $# > 0 )); then
  shift
fi
case "$task_mode" in
  migrate|server)
    # Go runs from server/, but explicit paths belong to the caller's cwd.
    while (( $# > 0 )); do
      case "$1" in
        --model-config|--env-file|--execution-config|--budget-change|--cloud-config|--db|--web-dir|-model-config|-env-file|-execution-config|-budget-change|-cloud-config|-db|-web-dir)
          task_args+=("$1")
          shift
          if (( $# > 0 )); then
            append_explicit_path "$1"
            shift
          fi
          ;;
        --model-config=*|--env-file=*|--execution-config=*|--budget-change=*|--cloud-config=*|--db=*|--web-dir=*|-model-config=*|-env-file=*|-execution-config=*|-budget-change=*|-cloud-config=*|-db=*|-web-dir=*)
          append_explicit_path "${1#*=}" "${1%%=*}="
          shift
          ;;
        --)
          task_args+=("$@")
          break
          ;;
        *)
          task_args+=("$1")
          shift
          ;;
      esac
    done
    ;;
  *)
    if (( $# > 0 )); then
      printf 'Mode %s does not accept extra arguments.\n' "$task_mode" >&2
      exit 2
    fi
    ;;
esac
case "$task_mode" in
  migrate)
    cd "$task_root/server"
    exec go run -mod=readonly ./cmd/server --migrate "${task_args[@]}"
    ;;
  server)
    cd "$task_root/server"
    exec go run -mod=readonly ./cmd/server "${task_args[@]}"
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
    exec go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g main.go -d cmd/server,internal/httpapi,internal/storage,internal/app,internal/domain,internal/recheck,internal/agentquery,internal/workload --output ../docs/api --outputTypes json,yaml --parseInternal
    ;;
  *)
    printf '%s\n' 'Usage: bash scripts/e0.sh {migrate|server|test|build|api}'
    ;;
esac
