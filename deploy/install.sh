#!/usr/bin/env bash
# Installs goldstar and its systemd user units. Identical on Debian and
# CachyOS — the only requirement is systemd, which both use.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_DIR="${BIN_DIR:-$HOME/.local/bin}"
UNIT_DIR="$HOME/.config/systemd/user"
CONF_DIR="$BIN_DIR"   # settings live beside the binary

echo "==> Building"
cd "$REPO_DIR"
# CGO_ENABLED=0 keeps the binary static: the SQLite driver is pure Go, so the
# result runs on any glibc or musl system without matching library versions.
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o goldstar .

echo "==> Installing binary to $BIN_DIR"
mkdir -p "$BIN_DIR"
install -m 0755 goldstar "$BIN_DIR/goldstar"

echo "==> Installing config to $CONF_DIR"
mkdir -p "$CONF_DIR"
chmod 700 "$CONF_DIR"
if [[ ! -f "$CONF_DIR/.env" ]]; then
  install -m 0600 "$REPO_DIR/.env.example" "$CONF_DIR/.env"
  echo "    created $CONF_DIR/.env — fill in your credentials"
else
  echo "    $CONF_DIR/.env already exists, left untouched"
fi

echo "==> Installing systemd user units to $UNIT_DIR"
mkdir -p "$UNIT_DIR"
for unit in goldstar.service goldstar.timer goldstar-web.service; do
  sed "s|__BINDIR__|$BIN_DIR|g" "$REPO_DIR/deploy/$unit" > "$UNIT_DIR/$unit"
done

systemctl --user daemon-reload
systemctl --user enable --now goldstar.timer

# The dashboard will not start without a password. Offer to set one now
# rather than letting the first `serve` fail.
if ! grep -q '^GOLDSTAR_PASSWORD_HASH=.\+' "$CONF_DIR/.env" 2>/dev/null; then
  echo
  echo "==> The dashboard needs a password before it will start."
  read -r -p "    Set one now? [Y/n] " reply
  if [[ ! "$reply" =~ ^[Nn] ]]; then
    if hash_line=$("$BIN_DIR/goldstar" passwd); then
      # Replace the empty placeholder rather than appending a duplicate key.
      if grep -q '^GOLDSTAR_PASSWORD_HASH=' "$CONF_DIR/.env"; then
        sed -i "s|^GOLDSTAR_PASSWORD_HASH=.*|$hash_line|" "$CONF_DIR/.env"
      else
        printf '%s\n' "$hash_line" >> "$CONF_DIR/.env"
      fi
      echo "    password set"
    else
      echo "    skipped — run \`goldstar passwd\` later"
    fi
  fi
fi

# User timers only fire while a session exists unless lingering is enabled.
# Without this the daily job silently does not run on a headless box.
if ! loginctl show-user "$USER" -p Linger --value 2>/dev/null | grep -q yes; then
  echo "==> Enabling linger so the timer runs without an active login"
  loginctl enable-linger "$USER" || echo "    could not enable linger; run: sudo loginctl enable-linger $USER"
fi

cat <<EOF

Installed.

  Config      $CONF_DIR/.env      <- add IMAP_USER, IMAP_PASS, GEMINI_API_KEY
  Data        \$BIN_DIR/data
  Binary      $BIN_DIR/goldstar

Next:
  goldstar doctor                 check config and mailbox connectivity
  goldstar run                    fetch + export once, right now
  systemctl --user list-timers goldstar.timer
  systemctl --user enable --now goldstar-web.service   # dashboard

Dashboard:  http://127.0.0.1:8787
Publishing it through a Cloudflare tunnel: see deploy/TUNNEL.md

Make sure $BIN_DIR is on your PATH.
EOF
