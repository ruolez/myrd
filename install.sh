#!/usr/bin/env bash
# myrd (remotectl) — production installer for a dedicated Ubuntu 24.04 LTS server.
#
#   curl -fsSL https://raw.githubusercontent.com/ruolez/myrd/main/install.sh | sudo bash
#   sudo bash /opt/myrd/install.sh [install|update|ssl|remove]
#
#   install  clone to /opt/myrd, generate secrets, start the stack, obtain a Let's Encrypt
#            certificate (certbot webroot through Caddy) and enable automatic renewal
#   update   back up, pull from GitHub and rebuild; keeps data, .env and the certificate,
#            prunes unused images
#   ssl      check / fix / renew the certificate, or move to a new domain
#   remove   back up the database to /root, then delete the stack
#
# Unattended: UNATTENDED=1 takes each prompt's default; set DOMAIN, LE_EMAIL, PUBLIC_IP,
# ADMIN_EMAIL, and CONFIRM_REMOVE=yes for `remove`.
# Testing: ACME_SERVER=<ACME directory URL>, REPO_URL, BRANCH, INSTALL_DIR.
set -euo pipefail

REPO_URL="${REPO_URL:-https://github.com/ruolez/myrd.git}"
BRANCH="${BRANCH:-main}"
DIR="${INSTALL_DIR:-/opt/myrd}"
HOOK=/etc/letsencrypt/renewal-hooks/deploy/myrd.sh
TLS_DIR="$DIR/data/tls"
ACME_DIR="$DIR/data/acme"
CADDY_TLS_ON="tls /tls/fullchain.pem /tls/privkey.pem"
COTURN_GID=65534 # coturn runs as nobody:nogroup and must read the private key
KEEP_BACKUPS=10

BLUE='\033[1;34m' GREEN='\033[1;32m' YELLOW='\033[1;33m' RED='\033[1;31m' NC='\033[0m'
say() { echo -e "${BLUE}==>${NC} $*"; }
ok() { echo -e "${GREEN} ok${NC} $*"; }
warn() { echo -e "${YELLOW}!! ${NC} $*" >&2; }
die() {
  echo -e "${RED}xx ${NC} $*" >&2
  exit 1
}

# ask "prompt" [default] — reads the terminal even under `curl | bash`.
ask() {
  local a=""
  if [ -z "${UNATTENDED:-}" ] && (: </dev/tty) 2>/dev/null; then
    read -r -p "$1${2:+ [$2]}: " a </dev/tty || true
  fi
  echo "${a:-${2:-}}"
}
confirm() { [[ "$(ask "$1 (y/n)" "$2")" =~ ^[Yy] ]]; }

# ---------------------------------------------------------------- .env helpers
get_env() { grep -E "^$1=" "$DIR/.env" 2>/dev/null | tail -1 | cut -d= -f2- || true; }

set_env() {
  local tmp
  tmp="$(mktemp)"
  awk -v k="$1" -v v="$2" 'index($0, k "=") == 1 { if (!done) print k "=" v; done = 1; next }
    { print } END { if (!done) print k "=" v }' "$DIR/.env" >"$tmp"
  cat "$tmp" >"$DIR/.env" && rm -f "$tmp"
}

compose() { (cd "$DIR" && docker compose "$@"); }

# ---------------------------------------------------------------- host setup
require_root() { [ "$(id -u)" -eq 0 ] || die "run as root: sudo bash install.sh"; }

check_os() {
  # shellcheck disable=SC1091
  . /etc/os-release
  [ "${ID:-}" = ubuntu ] && [ "${VERSION_ID:-}" = "24.04" ] || warn "tested on Ubuntu 24.04; this is ${PRETTY_NAME:-unknown}"
}

install_packages() {
  say "Installing packages (git, certbot, ufw)"
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ca-certificates curl git openssl ufw certbot iproute2 >/dev/null
}

install_docker() {
  if command -v docker >/dev/null && docker compose version >/dev/null 2>&1; then
    ok "Docker $(docker version --format '{{.Server.Version}}' 2>/dev/null || echo present)"
    return
  fi
  say "Installing Docker Engine"
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc
  # shellcheck disable=SC1091
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
    >/etc/apt/sources.list.d/docker.list
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin >/dev/null
  systemctl enable --now docker
}

firewall() {
  say "Firewall: ssh, 80/443 tcp+udp, 3478 tcp+udp, 5349/tcp, 49152-65535/udp"
  ufw default deny incoming >/dev/null
  ufw default allow outgoing >/dev/null
  ufw allow OpenSSH >/dev/null
  for rule in 80/tcp 443/tcp 443/udp 3478 5349/tcp 49152:65535/udp; do ufw allow "$rule" >/dev/null; done
  ufw --force enable >/dev/null
}

port_holder() { ss -ltnpH "sport = :$1" 2>/dev/null | grep -o 'users:(("[^"]*"' | head -1 | cut -d'"' -f2 || true; }

check_ports_free() {
  local p holder
  for p in 80 443; do
    holder="$(port_holder "$p")"
    [ -z "$holder" ] || [ "$holder" = docker-proxy ] || die "port $p is used by '$holder'; this installer needs a dedicated server"
  done
}

public_ip() { curl -fsS4 --max-time 5 https://api.ipify.org 2>/dev/null || hostname -I | awk '{print $1}'; }

valid_domain() { [[ "$1" =~ ^([a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$ ]]; }

# check_dns domain ip — Let's Encrypt validates over HTTP, so the A record must point here.
check_dns() {
  local addrs
  addrs="$(getent ahostsv4 "$1" | awk '{print $1}' | sort -u | tr '\n' ' ' || true)"
  if [[ " $addrs " == *" $2 "* ]]; then
    ok "DNS: $1 → $2"
    return
  fi
  warn "DNS: $1 resolves to '${addrs% }', not this server ($2). Certificate issuance will fail until the A record points here."
  confirm "Continue anyway?" n || die "fix DNS, then run the installer again"
}

prompt_domain() { # prompt_domain [current]
  local d
  d="$(ask "Domain for myrd (DNS A record must point to this server)" "${DOMAIN:-${1:-}}")"
  d="$(echo "$d" | tr '[:upper:]' '[:lower:]')"
  valid_domain "$d" || die "invalid domain '$d'"
  echo "$d"
}

tune_postgres() {
  local mb
  mb="$(awk '/MemTotal/ {print int($2 / 1024)}' /proc/meminfo)"
  set_env PG_SHARED_BUFFERS "$((mb / 4 > 128 ? mb / 4 : 128))MB"
  set_env PG_EFFECTIVE_CACHE_SIZE "$((mb / 2 > 256 ? mb / 2 : 256))MB"
}

# set_domain_env domain ip — everything in .env derived from the public name.
set_domain_env() {
  local d="$1" turn
  turn="stun:$1:3478,turn:$1:3478?transport=udp,turn:$1:3478?transport=tcp,turns:$1:5349?transport=tcp"
  set_env DOMAIN "$d"
  set_env PUBLIC_URL "https://$d"
  set_env TURN_REALM "$d"
  set_env TURN_URLS "$turn"
  set_env TURN_URLS_AGENT "$turn"
  set_env TURN_EXTERNAL_IP "$2"
  set_env TURN_ALLOW_PRIVATE_PEERS false
}

# ---------------------------------------------------------------- health / backups
# probe_url path — fetches through Caddy on this host, following the HTTPS redirect.
probe_url() {
  local d
  d="$(get_env DOMAIN)"
  curl -fsSkL --max-time 5 --resolve "$d:80:127.0.0.1" --resolve "$d:443:127.0.0.1" "http://$d$1"
}

wait_healthy() {
  for _ in $(seq 1 90); do
    if probe_url /healthz 2>/dev/null | grep -q '"ok":true'; then
      ok "myrd is healthy"
      return 0
    fi
    sleep 2
  done
  warn "no healthy response from /healthz after 3 minutes; check: cd $DIR && docker compose logs api web"
  return 1
}

backup() { # backup dest_dir — database dump + .env
  local ts dest="$1"
  ts="$(date +%Y%m%d-%H%M%S)"
  mkdir -p "$dest"
  chmod 700 "$dest"
  if compose ps --status running --services 2>/dev/null | grep -qx postgres; then
    compose exec -T postgres pg_dump -U remotectl remotectl | gzip >"$dest/db-$ts.sql.gz"
    ok "database backup: $dest/db-$ts.sql.gz"
  else
    warn "postgres is not running; skipping the database dump"
  fi
  cp "$DIR/.env" "$dest/env-$ts"
  chmod 600 "$dest"/*
}

prune_backups() { # keeps the newest KEEP_BACKUPS of each kind; names are ours (no spaces)
  local kind
  for kind in 'db-*.sql.gz' 'env-*'; do
    # shellcheck disable=SC2012,SC2086 # $kind is a glob on purpose
    ls -1t "$DIR"/backups/$kind 2>/dev/null | tail -n +$((KEEP_BACKUPS + 1)) | xargs -r rm -f
  done
}

# ---------------------------------------------------------------- TLS
certbot_args() {
  local email
  email="$(get_env LE_EMAIL)"
  CERTBOT_ARGS=(--non-interactive --agree-tos)
  if [ -n "$email" ]; then CERTBOT_ARGS+=(--email "$email"); else CERTBOT_ARGS+=(--register-unsafely-without-email); fi
  if [ -n "${ACME_SERVER:-}" ]; then CERTBOT_ARGS+=(--server "$ACME_SERVER"); fi
}

# deployed_cert_ok — data/tls holds an unexpired certificate for DOMAIN.
deployed_cert_ok() {
  local f="$TLS_DIR/fullchain.pem"
  [ -s "$f" ] && [ -s "$TLS_DIR/privkey.pem" ] &&
    openssl x509 -in "$f" -noout -checkend 0 >/dev/null 2>&1 &&
    openssl x509 -in "$f" -noout -checkhost "$(get_env DOMAIN)" 2>/dev/null | grep -q 'does match'
}

http_mode() {
  set_env SITE_ADDRESS "http://$(get_env DOMAIN)"
  set_env CADDY_TLS ""
  compose up -d web >/dev/null
}

https_mode() {
  set_env SITE_ADDRESS "$(get_env DOMAIN)"
  set_env CADDY_TLS "$CADDY_TLS_ON"
  compose up -d --force-recreate web coturn >/dev/null
}

wait_challenge_path() {
  local token="myrd-probe-$RANDOM"
  mkdir -p "$ACME_DIR/.well-known/acme-challenge"
  echo "$token" >"$ACME_DIR/.well-known/acme-challenge/$token"
  for _ in $(seq 1 30); do
    if [ "$(probe_url "/.well-known/acme-challenge/$token" 2>/dev/null)" = "$token" ]; then
      rm -f "$ACME_DIR/.well-known/acme-challenge/$token"
      return 0
    fi
    sleep 2
  done
  rm -f "$ACME_DIR/.well-known/acme-challenge/$token"
  warn "Caddy does not serve /.well-known/acme-challenge/ yet"
  return 1
}

# obtain_cert — issue (or keep, when not due) the certificate for DOMAIN.
obtain_cert() {
  local d
  d="$(get_env DOMAIN)"
  deployed_cert_ok || http_mode
  wait_challenge_path || return 1
  certbot_args
  say "Requesting a Let's Encrypt certificate for $d"
  certbot certonly --webroot -w "$ACME_DIR" -d "$d" --cert-name "$d" --keep-until-expiring "${CERTBOT_ARGS[@]}"
}

# deploy_cert [lineage] — copy the live certificate to data/tls and restart Caddy + coturn.
# The certbot deploy hook calls this after every renewal.
deploy_cert() {
  local d live
  d="$(get_env DOMAIN)"
  live="/etc/letsencrypt/live/$d"
  if [ -n "${1:-}" ] && [ "$1" != "$live" ]; then return 0; fi
  [ -s "$live/fullchain.pem" ] || die "no certificate at $live"
  install -d -m 0750 -o root -g "$COTURN_GID" "$TLS_DIR"
  install -m 0644 -o root -g root "$live/fullchain.pem" "$TLS_DIR/fullchain.pem"
  install -m 0640 -o root -g "$COTURN_GID" "$live/privkey.pem" "$TLS_DIR/privkey.pem"
  https_mode
  ok "certificate deployed to Caddy and coturn (expires $(openssl x509 -in "$TLS_DIR/fullchain.pem" -noout -enddate | cut -d= -f2))"
  wait_turns
}

# wait_turns — coturn serves the deployed certificate on 5349 (TURNS).
wait_turns() {
  local want got
  want="$(openssl x509 -in "$TLS_DIR/fullchain.pem" -noout -fingerprint -sha256)"
  for _ in $(seq 1 20); do
    got="$(openssl s_client -connect 127.0.0.1:5349 -servername "$(get_env DOMAIN)" </dev/null 2>/dev/null |
      openssl x509 -noout -fingerprint -sha256 2>/dev/null || true)"
    if [ "$got" = "$want" ]; then
      ok "coturn serves TURNS on 5349"
      return 0
    fi
    sleep 1
  done
  warn "coturn is not serving the certificate on 5349; check: cd $DIR && docker compose logs coturn"
}

enable_renewal() {
  mkdir -p "$(dirname "$HOOK")"
  cat >"$HOOK" <<EOF
#!/bin/sh
# myrd: hand a renewed certificate to Caddy and coturn (installed by $DIR/install.sh).
exec bash "$DIR/install.sh" deploy-cert "\$RENEWED_LINEAGE"
EOF
  chmod 755 "$HOOK"
  systemctl enable --now certbot.timer >/dev/null 2>&1 || warn "could not enable certbot.timer"
  ok "automatic renewal: certbot.timer (twice daily) + deploy hook $HOOK"
}

ssl_status() {
  local d f="$TLS_DIR/fullchain.pem" end days
  d="$(get_env DOMAIN)"
  echo "  domain:   $d"
  if [ -s "$f" ]; then
    end="$(openssl x509 -in "$f" -noout -enddate | cut -d= -f2)"
    days=$((($(date -d "$end" +%s) - $(date +%s)) / 86400))
    echo "  cert:     $(openssl x509 -in "$f" -noout -issuer | sed 's/^issuer=//'), expires $end ($days days)"
  else
    echo "  cert:     none deployed"
  fi
  echo "  serving:  $(get_env SITE_ADDRESS) $([ -n "$(get_env CADDY_TLS)" ] && echo '(HTTPS)' || echo '(HTTP only)')"
  echo "  renewal:  certbot.timer $(systemctl is-active certbot.timer 2>/dev/null || echo unknown), hook $([ -x "$HOOK" ] && echo present || echo missing)"
}

# ---------------------------------------------------------------- commands
do_install() {
  local domain email ip admin
  if [ -f "$DIR/.env" ]; then
    warn "myrd is already installed in $DIR"
    if confirm "Run an update instead?" y; then
      do_update
      return
    fi
    die "nothing changed"
  fi
  check_os
  install_packages
  install_docker
  domain="$(prompt_domain)"
  email="$(ask "Email for Let's Encrypt expiry notices (optional)" "${LE_EMAIL:-}")"
  admin="$(ask "Admin login (username or email)" "${ADMIN_EMAIL:-admin}")"
  ip="$(ask "Public IPv4 of this server" "${PUBLIC_IP:-$(public_ip)}")"
  check_dns "$domain" "$ip"
  check_ports_free

  if [ -e "$DIR" ] && [ ! -d "$DIR/.git" ]; then die "$DIR exists but is not a myrd checkout"; fi
  if [ ! -d "$DIR/.git" ]; then
    say "Cloning $REPO_URL ($BRANCH) into $DIR"
    git clone -q --branch "$BRANCH" "$REPO_URL" "$DIR"
  fi
  ADMIN_EMAIL="$admin" "$DIR/scripts/gen-env.sh" >/dev/null
  set_env COMPOSE_FILE docker-compose.yml:docker-compose.prod.yml
  set_env LE_EMAIL "$email"
  set_domain_env "$domain" "$ip"
  tune_postgres
  set_env SITE_ADDRESS "http://$domain"
  set_env CADDY_TLS ""
  mkdir -p "$ACME_DIR" "$DIR/backups"

  firewall
  say "Building and starting containers (first build takes a few minutes)"
  compose up -d --build
  wait_healthy || true

  if obtain_cert; then
    deploy_cert
    enable_renewal
    wait_healthy || true
  else
    enable_renewal
    warn "No certificate yet; myrd is serving plain HTTP, where passkeys and WebCrypto do not work."
    warn "Fix DNS / ports 80+443, then run: sudo bash $DIR/install.sh ssl"
  fi

  echo
  ok "myrd is installed at $DIR"
  echo "  URL:       https://$domain"
  echo "  login:     $(get_env ADMIN_EMAIL) / $(get_env ADMIN_INITIAL_PASSWORD)   (change it under Settings → Password)"
  echo "  agents:    build the macOS app with SERVER_URL=https://$domain; enroll from Devices → Add device"
  echo "  manage:    sudo bash $DIR/install.sh [update|ssl|remove]"
}

do_update() {
  [ -d "$DIR/.git" ] && [ -f "$DIR/.env" ] || die "myrd is not installed in $DIR; choose Install"
  check_os
  say "Backing up to $DIR/backups"
  backup "$DIR/backups"
  prune_backups

  say "Pulling $BRANCH from $REPO_URL"
  git -C "$DIR" remote set-url origin "$REPO_URL"
  git -C "$DIR" fetch -q origin "$BRANCH"
  local before after
  before="$(git -C "$DIR" rev-parse --short HEAD)"
  git -C "$DIR" reset -q --hard "origin/$BRANCH"
  after="$(git -C "$DIR" rev-parse --short HEAD)"
  ok "code: $before → $after"

  # Settings added after the first install; existing values are kept.
  [ -n "$(get_env COMPOSE_FILE)" ] || set_env COMPOSE_FILE docker-compose.yml:docker-compose.prod.yml
  [ -n "$(get_env PG_SHARED_BUFFERS)" ] || tune_postgres
  [ -n "$(get_env TURN_URLS_AGENT)" ] || set_env TURN_URLS_AGENT "$(get_env TURN_URLS)"

  say "Rebuilding and restarting (SSL certificate and data are left as they are)"
  compose pull -q --ignore-buildable
  compose up -d --build --remove-orphans
  wait_healthy || true

  say "Removing unused images and old build cache"
  docker image prune -f >/dev/null
  docker builder prune -f --filter until=24h >/dev/null
  ok "updated to $after"
  ssl_status
}

do_ssl() {
  [ -f "$DIR/.env" ] || die "myrd is not installed in $DIR; choose Install"
  local cur d ip email
  say "Current SSL status"
  ssl_status
  cur="$(get_env DOMAIN)"
  d="$(prompt_domain "$cur")"
  email="$(ask "Email for Let's Encrypt expiry notices (optional)" "${LE_EMAIL:-$(get_env LE_EMAIL)}")"
  set_env LE_EMAIL "$email"
  ip="$(get_env TURN_EXTERNAL_IP)"
  ip="${PUBLIC_IP:-${ip:-$(public_ip)}}"
  check_dns "$d" "$ip"
  command -v certbot >/dev/null || install_packages

  if [ "$d" != "$cur" ]; then
    warn "Moving from $cur to $d: passkeys registered on $cur stop working (sign in with the password),"
    warn "and agents must be rebuilt/re-enrolled with SERVER_URL=https://$d."
    confirm "Change the domain to $d?" n || die "nothing changed"
    set_domain_env "$d" "$ip"
    compose up -d api >/dev/null
  fi

  if [ "$d" = "$cur" ] && [ -f "/etc/letsencrypt/renewal/$d.conf" ] && deployed_cert_ok &&
    confirm "A valid certificate for $d is deployed. Force a renewal now?" n; then
    say "Renewing $d"
    certbot renew --cert-name "$d" --force-renewal --no-random-sleep-on-renew || die "renewal failed; the current certificate stays in place"
  else
    obtain_cert || die "could not obtain a certificate for $d (check DNS and that ports 80/443 are reachable)"
  fi
  deploy_cert
  enable_renewal
  say "Testing renewal (certbot renew --dry-run)"
  if certbot renew --cert-name "$d" --dry-run --no-random-sleep-on-renew ${ACME_SERVER:+--server "$ACME_SERVER"} >/dev/null 2>&1; then ok "renewal dry run passed"; else warn "renewal dry run failed: run 'certbot renew --dry-run' for details"; fi
  wait_healthy || true
  ssl_status
}

do_remove() {
  [ -d "$DIR" ] || die "myrd is not installed in $DIR"
  local d ts
  d="$(get_env DOMAIN)"
  ts="$(date +%Y%m%d-%H%M%S)"
  warn "This stops myrd and deletes $DIR, its containers, images and data volumes."
  [ "${CONFIRM_REMOVE:-}" = yes ] || confirm "Remove myrd?" n || die "nothing changed"
  if [ -f "$DIR/.env" ]; then backup "/root/myrd-backup-$ts"; fi
  if [ -d "$DIR/backups" ] && [ -n "$(ls -A "$DIR/backups" 2>/dev/null)" ]; then
    cp -a "$DIR/backups" "/root/myrd-backup-$ts/older"
  fi
  compose down -v --remove-orphans --rmi local >/dev/null 2>&1 || true
  rm -f "$HOOK"
  if [ -n "$d" ] && [ -d "/etc/letsencrypt/live/$d" ] && confirm "Delete the Let's Encrypt certificate for $d?" n; then
    certbot delete --cert-name "$d" --non-interactive >/dev/null && ok "certificate deleted"
  fi
  cd /
  rm -rf "$DIR"
  docker image prune -f >/dev/null 2>&1 || true
  ok "myrd removed. Backups: /root/myrd-backup-$ts. Firewall rules and Docker are left installed."
}

main() {
  require_root
  local cmd="${1:-}"
  if [ -z "$cmd" ]; then
    echo "myrd installer ($DIR)"
    echo "  1) Install"
    echo "  2) Update from GitHub (keeps data, settings and SSL)"
    echo "  3) SSL: check / fix / renew / change domain"
    echo "  4) Remove"
    case "$(ask "Choose" 1)" in
      1) cmd=install ;; 2) cmd=update ;; 3) cmd=ssl ;; 4) cmd=remove ;; *) die "unknown choice" ;;
    esac
  fi
  case "$cmd" in
    install) do_install ;;
    update) do_update ;;
    ssl) do_ssl ;;
    remove) do_remove ;;
    deploy-cert) deploy_cert "${2:-}" ;;
    *) die "usage: install.sh [install|update|ssl|remove]" ;;
  esac
}

main "$@"
