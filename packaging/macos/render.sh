#!/usr/bin/env bash
# render.sh <template> <out> — substitutes @VAR@ from product.env + env (VERSION, SERVER_URL).
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
# shellcheck disable=SC1091
source "$HERE/product.env"
VERSION="${VERSION:-0.0.0}"
SERVER_URL="${SERVER_URL:-https://myrd.site}"
sed -e "s|@PRODUCT_NAME@|$PRODUCT_NAME|g" -e "s|@APP_NAME@|$APP_NAME|g" -e "s|@BUNDLE_ID@|$BUNDLE_ID|g" \
    -e "s|@EXECUTABLE@|$EXECUTABLE|g" -e "s|@MIN_MACOS@|$MIN_MACOS|g" -e "s|@VERSION@|$VERSION|g" \
    -e "s|@SERVER_URL@|$SERVER_URL|g" "$1" > "$2"
