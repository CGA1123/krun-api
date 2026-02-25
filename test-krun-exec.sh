#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

API_URL="${API_URL:-http://localhost:9191}"

# Build everything
echo "==> Building..."
make build

# Create test Dockerfile
TEST_DIR=$(mktemp -d)
trap 'rm -rf "$TEST_DIR"' EXIT

cat > "$TEST_DIR/Dockerfile" <<'EOF'
FROM alpine:latest
RUN apk add --no-cache curl shadow
CMD ["/bin/sh", "-l"]
EOF

echo "==> Test Dockerfile created at $TEST_DIR/Dockerfile"

# Check if krun-api is running
if ! curl -sf "$API_URL/v1/machines" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
  echo ""
  echo "ERROR: krun-api server is not responding at $API_URL"
  echo ""
  echo "Start it in another terminal first:"
  echo "  cd $(pwd) && make build && ./krun-api --libkrun-path /opt/homebrew/lib/libkrun.dylib --vmm-path ./krun-vmm --listen :9090"
  echo ""
  echo "Or set API_URL to point to your running instance:"
  echo "  API_URL=http://localhost:XXXX ./test-krun-exec.sh"
  exit 1
fi

echo "==> krun-api is running at $API_URL"

# Create and start a VM
echo "==> Creating VM..."
./krun-run --api "$API_URL" --init ./vminit -n test-exec "$TEST_DIR"

# Find the machine ID
MACHINE_ID=$(curl -s "$API_URL/v1/machines" | python3 -c "
import sys, json
machines = json.load(sys.stdin)
for m in machines:
    if m['name'] == 'test-exec' and m['state'] == 'running':
        print(m['id'])
        break
")

if [ -z "$MACHINE_ID" ]; then
  echo "ERROR: could not find running test-exec machine"
  exit 1
fi

echo "==> Machine ID: $MACHINE_ID"
echo "==> Waiting 2s for VM to boot..."
sleep 2

# Exec into the VM
echo "==> Exec into VM as root..."
echo "    (type 'exit' to disconnect)"
echo ""
./krun-exec --api "$API_URL" "root@$MACHINE_ID"

echo ""
echo "==> Session ended."
echo ""
echo "==> To stop the VM:"
echo "    curl -X POST $API_URL/v1/machines/$MACHINE_ID/stop"
