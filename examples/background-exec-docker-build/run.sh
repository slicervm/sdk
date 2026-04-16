#!/bin/bash
# CLI-only equivalent of background-exec-docker-build/main.go.
#
# Runs against a Slicer daemon via the `slicer` CLI only (no SDK code),
# exercising: vm add (wait-userdata) → vm exec (clone) → vm bg exec
# (docker buildx build --push, detached) → 5s wait → vm bg info → vm bg logs
# --follow from_id=0 → vm bg remove → vm delete.
#
# Env:
#   SLICER_URL          required — e.g. http://box:8080 or /path/to/slicer.sock
#   SLICER_TOKEN_FILE   path to bearer token (preferred — avoids leaking the
#                       token in process args / shell history)
#   SLICER_HOST_GROUP   host group (default: demo)
#
# The image is tagged ttl.sh/slicer-mixctl-<rand>:1h and lives for 1 hour.
set -euo pipefail

: "${SLICER_URL:?SLICER_URL must be set}"
: "${SLICER_TOKEN_FILE:?SLICER_TOKEN_FILE must be set (path to token file)}"
HOST_GROUP="${SLICER_HOST_GROUP:-demo}"
SLICER_BIN="${SLICER_BIN:-slicer}"

SLICER="$SLICER_BIN --url $SLICER_URL --token-file $SLICER_TOKEN_FILE"

step() { printf '\n\033[36m=== %s ===\033[0m\n' "$*"; }

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

cat > "$TMP/userdata.sh" <<'USERDATA'
#!/bin/bash
set -euo pipefail
exec > >(tee -a /var/log/slicer-bgexec-userdata.log) 2>&1
export DEBIAN_FRONTEND=noninteractive
for i in 1 2 3; do apt-get update -qy && break; sleep 15; done
apt-get install -qy curl ca-certificates git
curl -fsSL https://get.docker.com | sh
systemctl enable --now docker
usermod -aG docker ubuntu
USERDATA

step "create VM with docker userdata"
$SLICER vm add "$HOST_GROUP" --cpus 2 --ram-gb 4 \
  --userdata-file "$TMP/userdata.sh" \
  --wait-userdata --timeout 15m | tail -8

VM=$($SLICER vm list "$HOST_GROUP" | awk 'NR>2 && $1 ~ /^'"$HOST_GROUP"'-/ {print $1; exit}')
echo "vm=$VM"

SUFFIX=$(head -c 4 /dev/urandom | xxd -p)
IMAGE="ttl.sh/slicer-mixctl-${SUFFIX}:1h"

step "clone inlets/mixctl (foreground)"
$SLICER vm exec "$VM" --uid 1000 -- bash -c \
  "rm -rf ~/mixctl && git clone --depth 1 https://github.com/inlets/mixctl ~/mixctl"

step "slicer vm bg exec -- docker buildx build --push (background, 4M ring)"
RUN_OUT=$($SLICER vm bg exec "$VM" --uid 1000 --cwd /home/ubuntu/mixctl \
  --ring-bytes 4M -- docker buildx build --push -t "$IMAGE" .)
echo "$RUN_OUT"
EX=$(echo "$RUN_OUT" | awk -F'[= ]' '/exec_id=/ {for(i=1;i<=NF;i++) if($i=="exec_id") {print $(i+1); exit}}')
echo "exec_id=$EX image=$IMAGE"

step "detach 5s"
sleep 5

step "slicer vm bg info (mid-run — prove the build is still going while we are not listening)"
$SLICER vm bg info "$VM" "$EX"

step "slicer vm bg logs --follow --from-id 0 (reattach + stream to exit)"
$SLICER vm bg logs "$VM" "$EX" --follow --from-id 0

step "slicer vm bg remove (reap ring + registry entry)"
$SLICER vm bg remove "$VM" "$EX"

step "slicer vm bg info after remove (expect 410 Gone)"
$SLICER vm bg info "$VM" "$EX" 2>&1 || echo "got expected 410"

step "slicer vm delete"
$SLICER vm delete "$VM" | tail -3

step "DONE image=$IMAGE"
