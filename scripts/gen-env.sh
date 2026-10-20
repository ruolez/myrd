#!/usr/bin/env bash
# Creates .env with fresh random secrets (never overwrites an existing one).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ENV="$ROOT/.env"
if [ -f "$ENV" ]; then
  echo ".env already exists — leaving it alone"
  exit 0
fi
rnd() { openssl rand -base64 "$1" | tr -d '\n'; }
alnum() { openssl rand -base64 48 | tr -dc 'A-Za-z0-9' | head -c "$1"; }
ADMIN_PW="$(alnum 16)"
cat > "$ENV" <<CONF
# remotectl secrets — generated $(date -u +%Y-%m-%dT%H:%M:%SZ). Keep out of git.
POSTGRES_PASSWORD=$(alnum 32)
JWT_SECRET=$(rnd 48)
TURN_SECRET=$(alnum 40)
TOTP_ENC_KEY=$(rnd 32)
ADMIN_EMAIL=${ADMIN_EMAIL:-admin@example.com}
ADMIN_INITIAL_PASSWORD=${ADMIN_PW}

# --- public endpoints (dev defaults; set these for a VPS) ---
# PUBLIC_URL=https://remote.example.com
# SITE_ADDRESS=remote.example.com
# TURN_URLS=stun:remote.example.com:3478,turn:remote.example.com:3478?transport=udp,turn:remote.example.com:3478?transport=tcp,turns:remote.example.com:5349?transport=tcp
# TURN_URLS_AGENT=   (empty = same as TURN_URLS)
# TURN_EXTERNAL_IP=<public ip>
# TURN_ALLOW_PRIVATE_PEERS=false
# RELEASE_PUBLIC_KEY=<base64 ed25519; overrides the key in proto/release_key.go, for testing only>
CONF
chmod 600 "$ENV"
echo "wrote .env — admin login: ${ADMIN_EMAIL:-admin@example.com} / ${ADMIN_PW}"
