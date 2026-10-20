#!/usr/bin/env bash
# remotectl VPS installer for Ubuntu 24.04 LTS — Install / Update / Remove.
#   curl -fsSL https://raw.githubusercontent.com/ruolez/remotectl/main/install.sh | sudo bash
set -euo pipefail

REPO_URL="${REPO_URL:-https://github.com/ruolez/remotectl.git}"
BRANCH="${BRANCH:-main}"
DIR="${INSTALL_DIR:-/opt/remotectl}"
COMPOSE=(docker compose -f docker-compose.yml -f docker-compose.prod.yml)

say() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31mxx\033[0m %s\n' "$*" >&2; exit 1; }
ask() { local p="$1" d="${2:-}" a; read -r -p "$p${d:+ [$d]}: " a </dev/tty; echo "${a:-$d}"; }

[ "$(id -u)" -eq 0 ] || die "run as root (sudo)"

install_docker() {
  if command -v docker >/dev/null && docker compose version >/dev/null 2>&1; then return; fi
  say "Installing Docker Engine"
  apt-get update -qq
  apt-get install -y -qq ca-certificates curl gnupg git openssl ufw
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
    >/etc/apt/sources.list.d/docker.list
  apt-get update -qq
  apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  systemctl enable --now docker
}

firewall() {
  command -v ufw >/dev/null || apt-get install -y -qq ufw
  say "Configuring ufw (default deny; ssh, 80, 443 tcp+udp, 3478 tcp+udp, 5349 tcp, 49152-65535 udp)"
  ufw default deny incoming >/dev/null
  ufw default allow outgoing >/dev/null
  ufw allow OpenSSH >/dev/null
  ufw allow 80/tcp >/dev/null
  ufw allow 443/tcp >/dev/null
  ufw allow 443/udp >/dev/null
  ufw allow 3478 >/dev/null
  ufw allow 5349/tcp >/dev/null
  ufw allow 49152:65535/udp >/dev/null
  ufw --force enable >/dev/null
}

configure() {
  local domain email ip
  domain="$(ask "Domain for the web app (DNS A record must point here)")"
  [ -n "$domain" ] || die "domain is required"
  email="$(ask "Admin email" "admin@$domain")"
  ip="$(curl -fsS4 https://api.ipify.org || true)"
  ip="$(ask "Public IPv4 of this server" "$ip")"
  ADMIN_EMAIL="$email" scripts/gen-env.sh
  cat >>.env <<CONF
PUBLIC_URL=https://$domain
SITE_ADDRESS=$domain
TURN_DOMAIN=$domain
TURN_HOSTS=$domain:3478
TURN_REALM=$domain
TURN_URLS=stun:$domain:3478,turn:$domain:3478?transport=udp,turn:$domain:3478?transport=tcp,turns:$domain:5349?transport=tcp
TURN_URLS_AGENT=
TURN_EXTERNAL_IP=$ip
TURN_ALLOW_PRIVATE_PEERS=false
CONF
}

do_install() {
  install_docker
  if [ -d "$DIR/.git" ]; then die "$DIR already exists — choose Update"; fi
  say "Cloning $REPO_URL into $DIR"
  git clone -q --branch "$BRANCH" "$REPO_URL" "$DIR"
  cd "$DIR"
  configure
  firewall
  say "Building and starting containers"
  "${COMPOSE[@]}" up -d --build
  say "Done. Open $(grep ^PUBLIC_URL .env | cut -d= -f2) — admin credentials are in $DIR/.env"
}

do_update() {
  [ -d "$DIR/.git" ] || die "$DIR not found — choose Install"
  cd "$DIR"
  local ts; ts="$(date +%Y%m%d-%H%M%S)"
  mkdir -p backups
  say "Backing up database to backups/remotectl-$ts.sql.gz"
  "${COMPOSE[@]}" exec -T postgres pg_dump -U remotectl remotectl | gzip >"backups/remotectl-$ts.sql.gz"
  cp .env "backups/env-$ts"
  say "Pulling $BRANCH"
  git fetch -q origin "$BRANCH" && git reset -q --hard "origin/$BRANCH"
  "${COMPOSE[@]}" up -d --build
  say "Updated. Migrations run at API boot."
}

do_remove() {
  [ -d "$DIR" ] || die "$DIR not found"
  cd "$DIR"
  local keep; keep="$(ask "Keep database volume? (y/n)" "y")"
  if [ "$keep" = "y" ]; then "${COMPOSE[@]}" down; else "${COMPOSE[@]}" down -v; fi
  cd / && rm -rf "$DIR"
  say "Removed remotectl"
}

echo "remotectl installer"
echo "  1) Install"
echo "  2) Update"
echo "  3) Remove"
case "$(ask "Choose" "1")" in
  1) do_install ;;
  2) do_update ;;
  3) do_remove ;;
  *) die "unknown choice" ;;
esac
