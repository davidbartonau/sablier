#!/usr/bin/env bash
# Create the example "model" containers used by the opinionated stack.
# These are nginx:alpine instances that identify themselves so we can
# verify nginx is routing correctly. Replace with your real model
# containers — only the labels and the network matter to Sablier.

set -euo pipefail

NETWORK=sablier-net

create_model() {
  local name="$1"
  local peak="$2"
  local priority="$3"
  local body="$4"

  # If a container with this name already exists in any state, remove it
  # so the labels reflect the latest values.
  if docker inspect "$name" >/dev/null 2>&1; then
    docker rm -f "$name" >/dev/null
  fi

  docker create \
    --name "$name" \
    --network "$NETWORK" \
    --label sablier.enable=true \
    --label sablier.group=models \
    --label "sablier.peak_vram_mb=$peak" \
    --label "sablier.priority=$priority" \
    --health-cmd="wget -q -O - http://127.0.0.1/ >/dev/null 2>&1 || exit 1" \
    --health-interval=1s \
    --health-timeout=2s \
    --health-retries=3 \
    --health-start-period=500ms \
    --entrypoint=sh \
    nginx:alpine \
    -c "echo '$body' > /usr/share/nginx/html/index.html && exec nginx -g 'daemon off;'" \
    >/dev/null

  echo "  created $name (peak=$peak, priority=$priority)"
}

# Ensure the network exists (compose creates it, but if you run this script
# before `docker compose up`, fall back to creating it ourselves).
docker network inspect "$NETWORK" >/dev/null 2>&1 || \
  docker network create "$NETWORK" >/dev/null

create_model whisper-test 2000 10 "I am whisper-test, peak=2000 prio=10"
create_model embed-test   4000 50 "I am embed-test, peak=4000 prio=50"
create_model llm-test     5000 80 "I am llm-test, peak=5000 prio=80"

echo
echo "All model containers created (Stopped). docker ps -a:"
docker ps -a --filter "name=^/(whisper-test|embed-test|llm-test)$" \
  --format "table {{.Names}}\t{{.Status}}\t{{.Networks}}"
