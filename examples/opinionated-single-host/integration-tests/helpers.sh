#!/usr/bin/env bash
# Helper functions for the integration test harness. Sourced by run.sh.

set -uo pipefail

# SABLIER_BIN is the path to the built sablier binary. Override via env var
# if you've built it somewhere else. Default assumes you ran
# `go build -o ./bin/sablier ./cmd/sablier` from the repo root.
SABLIER_BIN="${SABLIER_BIN:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)/bin/sablier}"
SABLIER_LOG="/tmp/sablier.log"
SABLIER_URL="http://127.0.0.1:10000"
SABLIER_PID_FILE="/tmp/sablier.pid"

cleanup_containers() {
  for c in $(docker ps -aq --filter "name=^/sab-test-"); do
    docker rm -f "$c" >/dev/null 2>&1 || true
  done
}

create_test_container() {
  # Args: name, peak_vram_mb (or empty for non-participant), priority (or empty)
  local name="$1"
  local peak="${2:-}"
  local pri="${3:-}"
  local labels=()
  labels+=(--label "sablier.enable=true")
  labels+=(--label "sablier.group=default")
  if [[ -n "$peak" ]]; then
    labels+=(--label "sablier.peak_vram_mb=$peak")
  fi
  if [[ -n "$pri" ]]; then
    labels+=(--label "sablier.priority=$pri")
  fi
  docker create \
    --name "sab-test-$name" \
    "${labels[@]}" \
    --health-cmd "wget -q -O - http://localhost/ >/dev/null 2>&1 || exit 1" \
    --health-interval=1s \
    --health-timeout=2s \
    --health-retries=3 \
    --health-start-period=1s \
    nginx:alpine >/dev/null
  # Stop immediately if not yet running. `docker create` does not start.
  echo "  created sab-test-$name (peak=${peak:-none}, priority=${pri:-none})"
}

start_sablier() {
  # Args: extra args (space-separated)
  : >"$SABLIER_LOG"
  nohup "$SABLIER_BIN" start \
    --provider.name=docker \
    --provider.auto-stop-on-startup=true \
    --logging.level=debug \
    --sessions.default-duration="$SESSION_DURATION" \
    --sessions.expiration-interval=2s \
    --strategy.blocking.default-timeout=20s \
    --strategy.blocking.default-refresh-frequency=500ms \
    "$@" >"$SABLIER_LOG" 2>&1 &
  echo $! >"$SABLIER_PID_FILE"
  # Wait up to 5s for /health
  for i in $(seq 1 50); do
    if curl -sf "$SABLIER_URL/health" >/dev/null 2>&1; then
      echo "  sablier ready (pid $(cat $SABLIER_PID_FILE))"
      return 0
    fi
    sleep 0.1
  done
  echo "  sablier failed to start; tail of log:" >&2
  tail -20 "$SABLIER_LOG" >&2
  return 1
}

stop_sablier() {
  if [[ -f "$SABLIER_PID_FILE" ]]; then
    local pid
    pid=$(cat "$SABLIER_PID_FILE")
    kill "$pid" 2>/dev/null || true
    for i in $(seq 1 20); do
      if ! kill -0 "$pid" 2>/dev/null; then break; fi
      sleep 0.1
    done
    kill -9 "$pid" 2>/dev/null || true
    rm -f "$SABLIER_PID_FILE"
  fi
}

# Trigger a blocking start. Args: name, [session_duration], [timeout]
trigger_start() {
  local name="$1"
  local sd="${2:-$SESSION_DURATION}"
  local to="${3:-20s}"
  local url="$SABLIER_URL/api/strategies/blocking?names=sab-test-$name&session_duration=$sd&timeout=$to"
  local body
  local code
  body=$(curl -sS -o /tmp/last_body.json -w "%{http_code}" "$url" 2>&1)
  code="$body"
  echo "  GET blocking?names=sab-test-$name -> HTTP $code"
  cat /tmp/last_body.json
  echo
  return 0
}

# List which sab-test-* containers are currently running.
running_containers() {
  docker ps --filter "name=^/sab-test-" --format '{{.Names}}' | sort
}

# Assert that the given list (space separated) is exactly the running set.
assert_running() {
  local expected
  expected=$(echo "$@" | tr ' ' '\n' | sort)
  local actual
  actual=$(running_containers)
  if [[ "$actual" == "$expected" ]]; then
    echo "  ASSERT PASS: running set = [$@]"
    return 0
  fi
  echo "  ASSERT FAIL: expected [$@], got: $(echo $actual | tr '\n' ' ')"
  return 1
}

print_log_tail() {
  local n="${1:-20}"
  echo "--- last $n sablier log lines:"
  tail -n "$n" "$SABLIER_LOG"
  echo "---"
}
