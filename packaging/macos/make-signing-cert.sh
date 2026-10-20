#!/usr/bin/env bash
# One-time: creates the self-signed code-signing identity every RemoteAgent
# release is signed with, in the login keychain of this Mac. A stable identity
# lets macOS keep Screen Recording / Accessibility grants and the device-key
# Keychain access across updates; never replace it once devices run builds
# signed with it (they would lose both on the next update).
#
# Writes ~/.config/remotectl/codesign-identity (SHA-1, read by build-app.sh)
# and a backup ~/.config/remotectl/codesign.p12 (+ .password). Keep the backup
# safe: building releases on another Mac needs it (security import).
# macOS asks for your login password to trust the certificate for code signing.
set -euo pipefail
NAME="${CERT_NAME:-RemoteAgent Release Signing}"
DIR="$HOME/.config/remotectl"
KC="$HOME/Library/Keychains/login.keychain-db"
OPENSSL=/usr/bin/openssl # LibreSSL: its PKCS#12 files import into the keychain as-is

if [[ -e "$DIR/codesign-identity" ]]; then
  echo "$DIR/codesign-identity exists; the release identity is already set up:" >&2
  security find-identity -v -p codesigning | grep -F "$(cat "$DIR/codesign-identity")" >&2 || true
  exit 1
fi
mkdir -p "$DIR" && chmod 700 "$DIR"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

cat > "$WORK/cert.cnf" <<CNF
[req]
distinguished_name = dn
x509_extensions = ext
prompt = no
[dn]
CN = $NAME
[ext]
basicConstraints = critical,CA:false
keyUsage = critical,digitalSignature
extendedKeyUsage = critical,codeSigning
subjectKeyIdentifier = hash
CNF
"$OPENSSL" req -x509 -newkey rsa:2048 -nodes -days 7300 -config "$WORK/cert.cnf" \
  -keyout "$WORK/key.pem" -out "$WORK/cert.pem" 2>/dev/null
PASS="$("$OPENSSL" rand -hex 16)"
"$OPENSSL" pkcs12 -export -inkey "$WORK/key.pem" -in "$WORK/cert.pem" -name "$NAME" \
  -passout "pass:$PASS" -out "$WORK/codesign.p12"

security import "$WORK/codesign.p12" -k "$KC" -P "$PASS" -T /usr/bin/codesign >/dev/null
security add-trusted-cert -r trustRoot -p codeSign -k "$KC" "$WORK/cert.pem"

SHA1="$("$OPENSSL" x509 -in "$WORK/cert.pem" -noout -fingerprint -sha1 | sed 's/.*=//; s/://g')"
security find-identity -v -p codesigning | grep -q "$SHA1" ||
  { echo "the identity was imported but codesign does not list it as valid" >&2; exit 1; }

install -m 600 "$WORK/codesign.p12" "$DIR/codesign.p12"
(umask 077 && printf '%s\n' "$PASS" > "$DIR/codesign.p12.password")
printf '%s\n' "$SHA1" > "$DIR/codesign-identity"
echo "Release signing identity \"$NAME\" ($SHA1) is ready."
echo "Back up $DIR/codesign.p12 and its .password file."
