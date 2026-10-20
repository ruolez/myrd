#!/usr/bin/env bash
# Host Go (.tools/go, darwin) using the module cache filled by godocker.sh.
# The host Go binary cannot reach the module proxy (local firewall), so it
# runs offline: download modules with scripts/godocker.sh first.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
export PATH="$ROOT/.tools/go/bin:$PATH"
export GOMODCACHE="$ROOT/.tools/gomod" GOPROXY=off GOFLAGS="${GOFLAGS:--mod=mod}" GOTOOLCHAIN=local
exec go "$@"
