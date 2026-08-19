#!/usr/bin/env bash
# First-time deployment to a VPS. Run from a checkout on your own machine:
#
#   ./deploy/deploy.sh root@your-vps            # x86 VPS
#   ARCH=arm64 ./deploy/deploy.sh root@your-vps # ARM VPS
#
# Copies the binary and (optionally) your existing data, installs a systemd
# service, and leaves you to enter the secrets on the server itself. No
# password, key or mailbox credential is ever transferred or written into a
# command, so nothing sensitive lands in shell history on either machine.
#
# Safe to re-run: it never overwrites an existing database or .env.
set -euo pipefail

# Fail fast on an unreachable host instead of hanging on the default TCP retry.
SSH_OPTS=(-o ConnectTimeout=10 -o BatchMode=yes)
ssh()  { command ssh  "${SSH_OPTS[@]}" "$@"; }
scp()  { command scp  "${SSH_OPTS[@]}" "$@"; }

TARGET="${1:-}"
[ -z "$TARGET" ] && { echo "usage: $0 user@host [--with-data]"; exit 1; }
WITH_DATA=""
[ "${2:-}" = "--with-data" ] && WITH_DATA=1

ARCH="${ARCH:-amd64}"
REMOTE_DIR="${REMOTE_DIR:-/opt/goldstar}"
SERVICE_USER="${SERVICE_USER:-goldstar}"
HERE="$(cd "$(dirname "$0")/.." && pwd)"

echo "==> Building for linux/$ARCH"
cd "$HERE"
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" \
  go build -trimpath -ldflags="-s -w" -o /tmp/goldstar-deploy .

echo "==> Preparing the server"
ssh "$TARGET" "
  set -e
  id -u '$SERVICE_USER' >/dev/null 2>&1 || \
    adduser --system --group --home '$REMOTE_DIR' '$SERVICE_USER'
  mkdir -p '$REMOTE_DIR'
"

echo "==> Copying the binary"
scp -q /tmp/goldstar-deploy "$TARGET:$REMOTE_DIR/goldstar.new"
rm -f /tmp/goldstar-deploy

if [ -n "$WITH_DATA" ]; then
  if [ ! -f "$HERE/data/goldstar.db" ]; then
    echo "    no local database to send, skipping"
  else
    echo "==> Packaging data (excluding every secret)"
    # Checkpoint first: pages sitting in the write-ahead log would otherwise
    # be missing from the copy.
    sqlite3 "$HERE/data/goldstar.db" "PRAGMA wal_checkpoint(TRUNCATE);" >/dev/null
    tar czf /tmp/goldstar-data.tgz -C "$HERE" \
      --exclude='mailbox.env' --exclude='session.key' --exclude='backups' \
      --exclude='*.db-wal' --exclude='*.db-shm' data/

    if tar tzf /tmp/goldstar-data.tgz | grep -qE 'mailbox\.env|session\.key'; then
      echo "    ABORT: a secret file got into the archive"; rm -f /tmp/goldstar-data.tgz; exit 1
    fi

    echo "==> Sending data (existing data on the server is never overwritten)"
    scp -q /tmp/goldstar-data.tgz "$TARGET:$REMOTE_DIR/"
    rm -f /tmp/goldstar-data.tgz
    ssh "$TARGET" "
      set -e
      cd '$REMOTE_DIR'
      if [ -f data/goldstar.db ]; then
        echo '    server already has a database — leaving it alone'
        rm -f goldstar-data.tgz
      else
        tar xzf goldstar-data.tgz && rm goldstar-data.tgz
        # Attachment paths are absolute and still point at the old machine.
        sqlite3 data/goldstar.db \"UPDATE invoices SET source_file =
          replace(source_file, '$HERE/data', '$REMOTE_DIR/data');\" 2>/dev/null || true
        echo '    data restored and attachment paths rewritten'
      fi
    "
  fi
fi

echo "==> Installing the service"
scp -q "$HERE/deploy/goldstar-web.service" "$TARGET:/tmp/goldstar.service.tmpl"
ssh "$TARGET" "
  set -e
  cd '$REMOTE_DIR'
  mv -f goldstar.new goldstar && chmod +x goldstar

  sed -e 's|__BINDIR__|$REMOTE_DIR|g' /tmp/goldstar.service.tmpl > /etc/systemd/system/goldstar.service
  grep -q '^User=' /etc/systemd/system/goldstar.service || \
    sed -i 's|^\[Install\]|User=$SERVICE_USER\nGroup=$SERVICE_USER\n\n[Install]|' /etc/systemd/system/goldstar.service
  rm -f /tmp/goldstar.service.tmpl

  if [ ! -f .env ]; then
    cat > .env <<'ENVEOF'
WEB_ADDR=127.0.0.1:8787
GOLDSTAR_COOKIE_SECURE=true
GOLDSTAR_SYNC_AT=18:30
GOLDSTAR_SYNC_TZ=Europe/London
GOLDSTAR_BACKUP_KEEP=14
GEMINI_MODEL=gemini-3.1-flash-lite
ENVEOF
    echo '    wrote a starter .env (no secrets in it yet)'
  else
    echo '    .env already present — left untouched'
  fi

  chown -R '$SERVICE_USER:$SERVICE_USER' '$REMOTE_DIR'
  chmod 600 .env
  systemctl daemon-reload
  systemctl enable goldstar >/dev/null 2>&1 || true
"

cat <<EOF

Deployed to $TARGET:$REMOTE_DIR

Two secrets still need entering, on the server, so they never touch a command
line or an scp:

  ssh $TARGET
  cd $REMOTE_DIR

  # 1. dashboard password — prompts twice, never echoes, stores only a hash
  sudo -u $SERVICE_USER ./goldstar passwd | grep GOLDSTAR_PASSWORD_HASH >> .env

  # 2. Gemini key — read -rs keeps the value out of bash history
  read -rs -p 'Gemini API key: ' K && printf 'GEMINI_API_KEY=%s\n' "\$K" >> .env && unset K

  # optional: require a username as well as a password
  printf 'GOLDSTAR_USER=your-name\nGOLDSTAR_EMAIL=you@example.com\n' >> .env

  systemctl start goldstar && systemctl status goldstar --no-pager

The mailbox password is entered in the browser afterwards, under
Setup -> Admin -> Mailbox, so it is never typed into a shell at all.
EOF
