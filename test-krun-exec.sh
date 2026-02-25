#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

API_URL="${API_URL:-http://localhost:9191}"
BASE_IMAGE="${BASE_IMAGE:-alpine:latest}"

# Build everything
echo "==> Building..."
make build

# Check if krun-api is running
if ! curl -sf "$API_URL/v1/machines" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
  echo ""
  echo "ERROR: krun-api server is not responding at $API_URL"
  echo ""
  echo "Start it in another terminal first:"
  echo "  cd $(pwd) && make build && ./krun-api --vmm-path ./krun-vmm --vminit-path ./vminit --listen :9191"
  echo ""
  echo "Or set API_URL to point to your running instance:"
  echo "  API_URL=http://localhost:XXXX ./test-krun-exec.sh"
  exit 1
fi

echo "==> krun-api is running at $API_URL"
echo "==> Using base image: $BASE_IMAGE"

# Create and start a VM via --image (pulls config via crane, server handles rootfs caching)
echo "==> Creating VM from $BASE_IMAGE..."
./krun-run --api "$API_URL" --image "$BASE_IMAGE" --init ./vminit -n test-exec --cpus 2

# Find the machine ID
MACHINE_ID=$(curl -s "$API_URL/v1/machines" | python3 -c "
import sys, json
machines = json.load(sys.stdin)
for m in machines:
    if m['name'] == 'test-exec' and m['state'] == 'running':
        print(m['id'])
        break
")

echo "==> Waiting 2s for VM to boot..."
sleep 2

# Exec into the VM — pass any trailing script arguments as the command
echo "==> Exec into VM as root..."
if [ $# -gt 0 ]; then
  echo "    (running: $*)"
else
  echo "    (type 'exit' to disconnect)"
fi
echo ""
./krun-exec --api "$API_URL" "root@$MACHINE_ID" "$@"

echo ""
echo "==> Session ended."
echo ""
echo "==> To stop the VM and clean up the cloned rootfs:"
echo "    curl -X POST $API_URL/v1/machines/$MACHINE_ID/stop"
echo "    curl -X DELETE '$API_URL/v1/machines/$MACHINE_ID?delete_rootfs=true'"
