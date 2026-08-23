#!/usr/bin/env bash
# Push a new version to a VPS that has already been deployed.
#
#   ./deploy/update.sh root@your-vps
#
# Builds, backs up the database, swaps the binary and restarts. Settings and
# data are left completely alone. If the new version fails to come up, the
# previous binary is put back automatically.
set -euo pipefail

# Fail fast on an unreachable host instead of hanging on the default TCP retry.
SSH_OPTS=(-o ConnectTimeout=10 -o BatchMode=yes)
ssh()  { command ssh  "${SSH_OPTS[@]}" "$@"; }
scp()  { command scp  "${SSH_OPTS[@]}" "$@"; }

TARGET="${1:-}"
[ -z "$TARGET" ] && { echo "usage: $0 user@host"; exit 1; }

ARCH="${ARCH:-amd64}"
REMOTE_DIR="${REMOTE_DIR:-/opt/goldstar}"
HERE="$(cd "$(dirname "$0")/.." && pwd)"

echo "==> Checking the build is sound before shipping it"
cd "$HERE"
export CGO_ENABLED=1
go vet ./...
go test ./... >/dev/null
command -v node >/dev/null && node tools/ui-check.cjs >/dev/null

# internal/store uses SQLCipher (github.com/mutecomm/go-sqlcipher/v4) for the
# database's encryption at rest, which needs cgo and a C compiler — unlike
# the pure-Go driver this replaced, so CGO_ENABLED=1 is no longer optional.
# GOOS/GOARCH=linux/amd64 is a native build, not a cross-compile, as long as
# this script itself runs on a linux/amd64 machine (it does here); running
# it from a different OS/arch would need an actual cross-compiler toolchain
# for cgo, which a pure-Go build never required.
echo "==> Building for linux/$ARCH"
GOOS=linux GOARCH="$ARCH" \
  go build -trimpath -ldflags="-s -w" -o /tmp/goldstar-update .

echo "==> Uploading"
scp -q /tmp/goldstar-update "$TARGET:$REMOTE_DIR/goldstar.new"
rm -f /tmp/goldstar-update

echo "==> Swapping over"
ssh "$TARGET" "
  set -e
  cd '$REMOTE_DIR'

  # A snapshot before the swap: the point of a backup is to predate the change.
  ./goldstar backup >/dev/null 2>&1 || echo "    warning: pre-update backup failed"

  cp -f goldstar goldstar.prev 2>/dev/null || true
  mv -f goldstar.new goldstar && chmod +x goldstar
  chown \$(stat -c %U .env) goldstar 2>/dev/null || true

  systemctl restart goldstar
  sleep 3

  if systemctl is-active --quiet goldstar; then
    echo '    running'
    rm -f goldstar.prev
  else
    echo '    FAILED to start — rolling back'
    [ -f goldstar.prev ] && mv -f goldstar.prev goldstar
    systemctl restart goldstar
    journalctl -u goldstar -n 20 --no-pager
    exit 1
  fi
"
echo "==> Done"
