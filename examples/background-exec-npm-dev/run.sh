#!/bin/bash
# CLI-only equivalent of background-exec-npm-dev/main.go.
#
# Runs against a Slicer daemon via the `slicer` CLI only (no SDK code),
# exercising: vm add (wait-userdata) → vm bg exec `npm run dev` → wait for
# "Ready" in logs → port-forward 3000 → curl → disconnect 5s → reattach
# from_id=<last+1> → vm bg kill → vm bg wait → vm bg remove → vm delete.
#
# Env:
#   SLICER_URL          required
#   SLICER_TOKEN_FILE   path to bearer token file (preferred)
#   SLICER_HOST_GROUP   default: demo
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

# Opinionated stack: arkade installs node/npm into /usr/local, much faster
# than going through nodesource + apt.
arkade system install node
export PATH=/usr/local/bin:$PATH
node --version
npm --version

install -d -o ubuntu -g ubuntu /home/ubuntu/app
cat > /home/ubuntu/app/package.json <<'EOF'
{
  "name": "slicer-bgexec-demo",
  "version": "0.1.0",
  "private": true,
  "scripts": { "dev": "next dev -p 3000 -H 0.0.0.0" },
  "dependencies": { "next": "14.2.15", "react": "18.3.1", "react-dom": "18.3.1" }
}
EOF
mkdir -p /home/ubuntu/app/pages
cat > /home/ubuntu/app/pages/index.js <<'EOF'
export default function Home() {
  return <main><h1>slicer bg-exec demo</h1><p>alive</p></main>;
}
EOF
chown -R ubuntu:ubuntu /home/ubuntu/app
su - ubuntu -c 'cd /home/ubuntu/app && npm install --no-audit --no-fund --loglevel=error'
USERDATA

step "create VM with node userdata"
$SLICER vm add "$HOST_GROUP" --cpus 2 --ram-gb 2 \
  --userdata-file "$TMP/userdata.sh" \
  --wait-userdata --timeout 10m | tail -8

VM=$($SLICER vm list "$HOST_GROUP" | awk 'NR>2 && $1 ~ /^'"$HOST_GROUP"'-/ {print $1; exit}')
echo "vm=$VM"

step "slicer vm bg exec -- npm run dev (background)"
RUN_OUT=$($SLICER vm bg exec "$VM" --uid 1000 --gid 1000 --cwd /home/ubuntu/app -- npm run dev)
echo "$RUN_OUT"
EX=$(echo "$RUN_OUT" | awk -F'[= ]' '/exec_id=/ {for(i=1;i<=NF;i++) if($i=="exec_id") {print $(i+1); exit}}')
echo "exec_id=$EX"

step "wait for \"Local:\" marker in ring (snapshot loop)"
LAST_ID=0
for i in $(seq 1 60); do
  LOGS=$($SLICER vm bg logs "$VM" "$EX" 2>&1 || true)
  if echo "$LOGS" | grep -q "Local:\|Ready in"; then
    LAST_ID=$($SLICER vm bg info "$VM" "$EX" | awk -F'[:, ]+' '/"next_id"/ {print $4-1}')
    echo "ready marker seen, last_id=$LAST_ID"
    echo "$LOGS" | tail -8
    break
  fi
  sleep 2
done

step "port-forward 127.0.0.1:3000 -> VM:3000, curl"
$SLICER vm forward "$VM" -L 127.0.0.1:3000:127.0.0.1:3000 &
FWD=$!
trap 'kill $FWD 2>/dev/null || true; rm -rf "$TMP"' EXIT
sleep 2
curl -sSf http://127.0.0.1:3000/ | head -c 200
echo

step "disconnect 5s, then reattach from_id=$((LAST_ID+1))"
kill $FWD 2>/dev/null || true
sleep 5
timeout 15 $SLICER vm bg logs "$VM" "$EX" --follow --from-id $((LAST_ID+1)) 2>&1 | head -40 || true

step "slicer vm bg kill (TERM, 3s grace)"
$SLICER vm bg kill "$VM" "$EX" --signal TERM --grace 3s

step "slicer vm bg wait (block until exit)"
$SLICER vm bg wait "$VM" "$EX" --timeout 15s || true

step "slicer vm bg remove"
$SLICER vm bg remove "$VM" "$EX"

step "slicer vm delete"
$SLICER vm delete "$VM" | tail -3

step "DONE"
