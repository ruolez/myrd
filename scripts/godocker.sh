#!/usr/bin/env bash
# Runs `go <args>` inside golang:1.27-bookworm with the repo mounted at /src and
# the module cache shared with the host (.tools/gomod), so modules downloaded
# here are usable by host builds (`scripts/gohost.sh`). Run from any subdir:
#   scripts/godocker.sh -C agent get github.com/foo/bar@latest
#   (cd server && ../scripts/godocker.sh test ./...)
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REL="${PWD#"$ROOT"}"
exec docker run --rm ${GODOCKER_OPTS:-} \
  -v "$ROOT":/src -v "$ROOT/.tools/gomod":/go/pkg/mod -v remotectl-gocache:/root/.cache/go-build \
  -w "/src$REL" -e GOFLAGS="${GOFLAGS:-}" -e CGO_ENABLED="${CGO_ENABLED:-0}" \
  golang:1.27-bookworm go "$@"
