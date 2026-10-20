#!/bin/sh
# Renders turnserver.conf from the environment, then runs coturn.
set -eu
CONF=/tmp/turnserver.conf

cat > "$CONF" <<EOF
listening-port=3478
tls-listening-port=5349
min-port=${TURN_MIN_PORT:-49160}
max-port=${TURN_MAX_PORT:-49200}
use-auth-secret
static-auth-secret=${TURN_SECRET}
realm=${TURN_REALM:-remotectl.local}
fingerprint
no-cli
no-multicast-peers
no-software-attribute
stale-nonce=600
user-quota=4
total-quota=200
max-bps=12000000
log-file=stdout
simple-log
prometheus
EOF

if [ -n "${TURN_EXTERNAL_IP:-}" ]; then
  echo "external-ip=${TURN_EXTERNAL_IP}" >> "$CONF"
fi

if [ -n "${TURN_TLS_CERT:-}" ] && [ ! -r "$TURN_TLS_CERT" ]; then
  # Before the first certificate; install.sh restarts coturn once it exists.
  echo "coturn: $TURN_TLS_CERT not readable, TURNS disabled"
  TURN_TLS_CERT=
fi

if [ -n "${TURN_TLS_CERT:-}" ] && [ -n "${TURN_TLS_KEY:-}" ]; then
  echo "cert=${TURN_TLS_CERT}" >> "$CONF"
  echo "pkey=${TURN_TLS_KEY}" >> "$CONF"
else
  echo "no-tls" >> "$CONF"
  echo "no-dtls" >> "$CONF"
fi

if [ "${TURN_ALLOW_PRIVATE_PEERS:-false}" != "true" ]; then
  # The relay must never reach the box's own services or private networks.
  cat >> "$CONF" <<EOF
no-loopback-peers
denied-peer-ip=0.0.0.0-0.255.255.255
denied-peer-ip=10.0.0.0-10.255.255.255
denied-peer-ip=100.64.0.0-100.127.255.255
denied-peer-ip=127.0.0.0-127.255.255.255
denied-peer-ip=169.254.0.0-169.254.255.255
denied-peer-ip=172.16.0.0-172.31.255.255
denied-peer-ip=192.168.0.0-192.168.255.255
denied-peer-ip=::1
denied-peer-ip=fc00::-fdff:ffff:ffff:ffff:ffff:ffff:ffff:ffff
denied-peer-ip=fe80::-febf:ffff:ffff:ffff:ffff:ffff:ffff:ffff
EOF
else
  echo "allow-loopback-peers" >> "$CONF"
fi

exec turnserver -c "$CONF"
