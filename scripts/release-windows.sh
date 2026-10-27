#!/usr/bin/env bash
# Builds the Windows release installers in CI and collects them next to the
# macOS one: dist/releases/<VERSION>/RemoteAgent-<VERSION>-x64.msi and
# -x64-win7.msi, each with .sig and .sha256.
#   scripts/release-windows.sh             # start agent-windows.yml (release=true), wait, collect
#   scripts/release-windows.sh <run-id>    # collect the artifacts of a finished run
#   SERVER_URL   address each MSI must pre-fill (default https://myrd.site)
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WF=agent-windows.yml
SERVER_URL="${SERVER_URL:-https://myrd.site}"

run="${1:-}"
if [ -z "$run" ]; then
  [ -z "$(git -C "$ROOT" status --porcelain)" ] || echo "warning: the working tree has uncommitted changes" >&2
  head="$(git -C "$ROOT" rev-parse HEAD)"
  [ "$(git -C "$ROOT" rev-parse '@{u}' 2>/dev/null)" = "$head" ] ||
    { echo "push first: CI builds what is on the remote" >&2; exit 1; }
  before="$(gh run list --workflow "$WF" --event workflow_dispatch --limit 1 --json databaseId --jq '.[0].databaseId // 0')"
  gh workflow run "$WF" -f release=true -f server_url="$SERVER_URL"
  for _ in $(seq 1 30); do
    run="$(gh run list --workflow "$WF" --event workflow_dispatch --limit 1 --json databaseId,headSha \
      --jq ".[] | select(.databaseId != $before and .headSha == \"$head\") | .databaseId")"
    [ -n "$run" ] && break
    sleep 2
  done
  [ -n "$run" ] || { echo "the release run did not show up" >&2; exit 1; }
  echo "run $run: https://github.com/$(gh repo view --json nameWithOwner --jq .nameWithOwner)/actions/runs/$run"
  gh run watch "$run" --exit-status >/dev/null
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
gh run download "$run" -D "$tmp"
msis="$(find "$tmp" -name '*.msi' | sort)"
[ -n "$msis" ] || { echo "run $run has no MSI artifacts" >&2; exit 1; }
for msi in $msis; do
  (cd "$(dirname "$msi")" && shasum -a 256 -c "$(basename "$msi").sha256" >/dev/null) ||
    { echo "checksum mismatch: $msi" >&2; exit 1; }
  [ -f "$msi.sig" ] || { echo "missing release signature: $msi.sig" >&2; exit 1; }
  # The MSI string pool keeps property values as plain bytes.
  grep -qaF "$SERVER_URL" "$msi" || { echo "$msi does not pre-fill $SERVER_URL" >&2; exit 1; }
  version="$(python3 -I -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"])' "$msi.sig")"
  rel="$ROOT/dist/releases/$version"
  mkdir -p "$rel"
  cp -f "$msi" "$msi.sig" "$msi.sha256" "$rel/"
  echo "  $rel/$(basename "$msi")"
  echo "  $rel/$(basename "$msi").sig"
done
