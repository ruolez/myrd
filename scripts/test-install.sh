#!/usr/bin/env bash
# End-to-end test of install.sh in a throwaway privileged Ubuntu 24.04 container
# running its own Docker, against Pebble (Let's Encrypt's test ACME server) doing real
# HTTP-01 validation of "myrd.test". Covers install, renewal + deploy hook, update,
# ssl (check and recovery from a broken TLS state) and remove. Nothing on this machine
# is touched except the test containers, network and volume, removed at the end.
#
#   scripts/test-install.sh          (~10 min: the stack is built from scratch inside)
#   KEEP=1 scripts/test-install.sh   leave the containers running for inspection
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
NET=myrd-e2e HOST=myrd-e2e-host PEBBLE=myrd-e2e-pebble VOL=myrd-e2e-docker
PEBBLE_IMAGE=ghcr.io/letsencrypt/pebble:2.6.0
WORK="$(mktemp -d)"

remove_containers() {
  docker rm -f "$HOST" "$PEBBLE" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  docker volume rm "$VOL" >/dev/null 2>&1 || true
}
cleanup() {
  rm -rf "$WORK"
  [ -n "${KEEP:-}" ] && { echo "KEEP=1: docker rm -f $HOST $PEBBLE; docker network rm $NET; docker volume rm $VOL"; return; }
  remove_containers
}
remove_containers
trap cleanup EXIT

# The repo as it would be pushed: tracked + untracked-but-not-ignored files.
git -C "$ROOT" ls-files -co --exclude-standard -z | (cd "$ROOT" && tar --null -T - -czf "$WORK/src.tgz")
cat >"$WORK/pebble.json" <<'EOF'
{"pebble": {"listenAddress": "0.0.0.0:14000", "managementListenAddress": "0.0.0.0:15000",
  "certificate": "test/certs/localhost/cert.pem", "privateKey": "test/certs/localhost/key.pem",
  "httpPort": 80, "tlsPort": 443, "ocspResponderURL": "", "externalAccountBindingRequired": false}}
EOF

docker network create "$NET" >/dev/null
docker run -d --name "$PEBBLE" --network "$NET" --network-alias pebble -e PEBBLE_VA_NOSLEEP=1 \
  -v "$WORK/pebble.json:/pebble.json:ro" "$PEBBLE_IMAGE" -config /pebble.json >/dev/null
docker cp "$PEBBLE:/test/certs/pebble.minica.pem" "$WORK/pebble-ca.pem"
docker run -d --name "$HOST" --privileged --network "$NET" --network-alias myrd.test --hostname myrd \
  -v "$VOL:/var/lib/docker" -v "$WORK:/e2e:ro" ubuntu:24.04 sleep infinity >/dev/null

# Written to a file: run from stdin, any command that reads stdin would eat the script.
cat >"$WORK/inner.sh" <<'CONTAINER'
set -uo pipefail
export DEBIAN_FRONTEND=noninteractive
PASS=0 FAIL=0
check() { # check "description" command... (bash -c sees the exported helpers)
  local desc="$1" out; shift
  if out="$("$@" 2>&1)"; then PASS=$((PASS + 1)); echo "  ok    $desc"
  else FAIL=$((FAIL + 1)); echo "  FAIL  $desc"; echo "$out" | tail -8 | sed 's/^/        /'; fi
}
section() { echo; echo "== $*"; }
run() { # run log-name command... — installer output goes to a log, shown on failure
  local name="$1"; shift
  "$@" >"/tmp/$name.log" 2>&1
}

apt-get update -qq >/dev/null && apt-get install -y -qq git curl ca-certificates openssl iproute2 >/dev/null 2>&1
git config --global user.email e2e@myrd.test && git config --global user.name e2e
mkdir /work && tar -xzf /e2e/src.tgz -C /work && git -C /work init -q -b main && git -C /work add -A && git -C /work commit -qm "e2e: initial"

# No systemd in a container: start dockerd ourselves, record certbot.timer, no-op ufw.
cat >/usr/local/bin/systemctl <<'EOF'
#!/bin/sh
cmd="$1"; for unit; do :; done
case "$unit" in
  docker) [ "$cmd" = is-active ] && { [ -S /var/run/docker.sock ]; exit; }
          [ -S /var/run/docker.sock ] || { dockerd >/var/log/dockerd.log 2>&1 & for i in $(seq 1 30); do [ -S /var/run/docker.sock ] && break; sleep 1; done; } ;;
  certbot.timer) case "$cmd" in is-active) [ -f /tmp/certbot-timer ] && echo active || { echo inactive; exit 3; } ;; *) touch /tmp/certbot-timer ;; esac ;;
esac
exit 0
EOF
printf '#!/bin/sh\necho "ufw $*" >>/tmp/ufw.log\n' >/usr/local/bin/ufw
chmod +x /usr/local/bin/systemctl /usr/local/bin/ufw

export UNATTENDED=1 REPO_URL=file:///work DOMAIN=myrd.test LE_EMAIL=ops@myrd.test ADMIN_EMAIL=admin
export ACME_SERVER=https://pebble:14000/dir REQUESTS_CA_BUNDLE=/e2e/pebble-ca.pem
export PUBLIC_IP="$(getent ahostsv4 myrd.test | awk '{print $1; exit}')"
D=/opt/myrd
env_of() { grep -E "^$1=" $D/.env | tail -1 | cut -d= -f2-; }
https() { curl -fsSk --max-time 5 --resolve myrd.test:443:127.0.0.1 "https://myrd.test$1"; }
serial() { echo | openssl s_client -connect "127.0.0.1:$1" -servername myrd.test 2>/dev/null | openssl x509 -noout -serial; }
issuer() { echo | openssl s_client -connect 127.0.0.1:443 -servername myrd.test 2>/dev/null | openssl x509 -noout -issuer; }
login() { # login password → cookie jar /tmp/cj
  curl -fsSk --resolve myrd.test:443:127.0.0.1 -c /tmp/cj -H 'Origin: https://myrd.test' -H 'Content-Type: application/json' \
    -d "{\"email\":\"admin\",\"password\":\"$1\"}" https://myrd.test/v1/auth/login
}
create_user() {
  curl -fsSk --resolve myrd.test:443:127.0.0.1 -b /tmp/cj -H 'Origin: https://myrd.test' -H 'Content-Type: application/json' \
    -d '{"email":"kept@myrd.test","password":"kept-password-123"}' https://myrd.test/v1/admin/users
}
has_user() { curl -fsSk --resolve myrd.test:443:127.0.0.1 -b /tmp/cj https://myrd.test/v1/admin/users | grep -q kept@myrd.test; }
export D
export -f env_of https serial issuer

section "install"
run install bash /work/install.sh install; rc=$?
check "install exits 0" [ "$rc" -eq 0 ]
[ "$rc" -eq 0 ] || tail -40 /tmp/install.log
check "HTTPS /healthz through Caddy" bash -c "https /healthz | grep -q '\"ok\":true'"
check "certificate issued by Pebble via HTTP-01" bash -c "issuer | grep -qi pebble"
check "HTTP redirects to HTTPS" bash -c "curl -sI --resolve myrd.test:80:127.0.0.1 http://myrd.test/ | grep -qi '^location: https://myrd.test/'"
check "coturn TURNS (5349) serves the same certificate" bash -c 's="$(serial 443)"; [ -n "$s" ] && [ "$(serial 5349)" = "$s" ]'
check "private key readable by coturn only (root:65534 0640)" bash -c "[ \"\$(stat -c '%U:%g %a' $D/data/tls/privkey.pem)\" = 'root:65534 640' ]"
check "deploy hook installed" [ -x /etc/letsencrypt/renewal-hooks/deploy/myrd.sh ]
check "certbot.timer enabled" [ -f /tmp/certbot-timer ]
check "renewal uses the webroot" grep -q '^authenticator = webroot' /etc/letsencrypt/renewal/myrd.test.conf
check ".env: HTTPS mode" bash -c "[ \"\$(env_of SITE_ADDRESS)\" = myrd.test ] && [ -n \"\$(env_of CADDY_TLS)\" ]"
check ".env: agents get the public TURN URLs" bash -c "env_of TURN_URLS_AGENT | grep -q 'turns:myrd.test:5349'"
check ".env: postgres sized from RAM" bash -c "env_of PG_SHARED_BUFFERS | grep -Eq '^[0-9]+MB$'"
check ".env is 0600" bash -c "[ \"\$(stat -c %a $D/.env)\" = 600 ]"
check "firewall opens TURN relay range" grep -q '49152:65535/udp' /tmp/ufw.log
check "admin login with the generated password" login "$(env_of ADMIN_INITIAL_PASSWORD)"
check "create a user (data to keep)" create_user

section "renewal (certbot renew → deploy hook)"
before="$(serial 443)"
[ -n "$before" ] || { echo "no certificate served; stopping"; exit 1; }
run renew certbot renew --force-renewal --no-random-sleep-on-renew; rc=$?
check "certbot renew exits 0" [ "$rc" -eq 0 ]
check "Caddy serves the renewed certificate" bash -c "[ \"\$(serial 443)\" != '$before' ]"
check "coturn serves the renewed certificate" bash -c 's="$(serial 443)"; [ -n "$s" ] && [ "$(serial 5349)" = "$s" ]'
check "renewal dry run passes" certbot renew --dry-run --no-random-sleep-on-renew --server "$ACME_SERVER"

section "update"
echo "e2e change" >>/work/README.md && git -C /work commit -qam "e2e: second commit"
cert_before="$(serial 443)" head_after="$(git -C /work rev-parse --short HEAD)"
run update bash $D/install.sh update; rc=$?
check "update exits 0" [ "$rc" -eq 0 ]
[ "$rc" -eq 0 ] || tail -40 /tmp/update.log
check "code moved to the new commit" bash -c "[ \"\$(git -C $D rev-parse --short HEAD)\" = '$head_after' ]"
check "database backup written" bash -c "ls $D/backups/db-*.sql.gz"
check "data kept (user still exists)" has_user
check "SSL left alone (same certificate)" bash -c "[ \"\$(serial 443)\" = '$cert_before' ]"
check "no dangling images left" bash -c '[ -z "$(docker images -qf dangling=true)" ]'
check "HTTPS healthy after update" bash -c "https /healthz | grep -q '\"ok\":true'"

section "ssl: check / fix"
run ssl1 bash $D/install.sh ssl; rc=$?
check "ssl on a healthy install exits 0" [ "$rc" -eq 0 ]
check "valid certificate kept (no forced renewal)" bash -c "[ \"\$(serial 443)\" = '$cert_before' ]"
rm -f $D/data/tls/*.pem && (cd $D && docker compose up -d --force-recreate web >/dev/null 2>&1)
check "broken state: HTTPS is down" bash -c "! https /healthz"
run ssl2 bash $D/install.sh ssl; rc=$?
check "ssl repairs it (exit 0)" [ "$rc" -eq 0 ]
[ "$rc" -eq 0 ] || tail -40 /tmp/ssl2.log
check "HTTPS healthy after the fix" bash -c "https /healthz | grep -q '\"ok\":true'"
check "data kept through the fix" has_user

section "remove"
CONFIRM_REMOVE=yes run remove bash $D/install.sh remove; rc=$?
check "remove exits 0" [ "$rc" -eq 0 ]
check "install dir deleted" [ ! -e $D ]
check "containers gone" bash -c '[ -z "$(docker ps -aq)" ]'
check "deploy hook removed" [ ! -e /etc/letsencrypt/renewal-hooks/deploy/myrd.sh ]
check "database backup kept in /root" bash -c "ls /root/myrd-backup-*/db-*.sql.gz"
check "certificate kept by default" [ -d /etc/letsencrypt/live/myrd.test ]

echo; echo "passed $PASS, failed $FAIL"
[ "$FAIL" -eq 0 ]
CONTAINER
docker exec "$HOST" bash /e2e/inner.sh
