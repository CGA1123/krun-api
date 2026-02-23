#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

API_URL="${API_URL:-http://localhost:9090}"

# Build krun-run, krun-vmm, and vminit-agent
echo "==> Building krun-run, krun-vmm, and vminit-agent..."
go build -o krun-run ./cmd/krun-run/
go build -o krun-vmm ./cmd/krun-vmm/
codesign --sign - --entitlements entitlements.plist --force krun-vmm
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o vminit-agent ./cmd/vminit-agent/

# Create test Dockerfile
TEST_DIR=$(mktemp -d)
trap 'rm -rf "$TEST_DIR"' EXIT

cat > "$TEST_DIR/Dockerfile" <<'EOF'
FROM alpine:latest
RUN apk add --no-cache curl
CMD ["/bin/sh", "-l"]
EOF

echo "==> Test Dockerfile created at $TEST_DIR/Dockerfile"

# Check if krun-api is actually running (verify we get valid JSON back)
if ! curl -sf "$API_URL/v1/machines" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
  echo ""
  echo "ERROR: krun-api server is not responding at $API_URL"
  echo ""
  echo "Start it in another terminal first:"
  echo "  cd $(pwd) && make && ./krun-api --libkrun-path /opt/homebrew/lib/libkrun.dylib --vmm-path ./krun-vmm --listen :9090"
  echo ""
  echo "Or set API_URL to point to your running instance:"
  echo "  API_URL=http://localhost:XXXX ./test-krun-run.sh"
  echo ""
  echo "Then re-run this script."
  exit 1
fi

echo "==> krun-api is running at $API_URL"

# Run krun-run
echo "==> Running: ./krun-run --api $API_URL --agent ./vminit-agent -n test-alpine $TEST_DIR"
./krun-run --api "$API_URL" --agent ./vminit-agent -n test-alpine "$TEST_DIR"

# Verify
echo ""
echo "==> Machines:"
curl -s "$API_URL/v1/machines" | python3 -m json.tool
