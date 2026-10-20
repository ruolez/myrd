#!/usr/bin/env bash
# Cross-compiles the Windows agent from macOS/Linux with mingw-w64 in Docker:
#   native/windows/xbuild.sh            # native lib + agent/dist/windows/remote-agent.exe
#   native/windows/xbuild.sh lib        # native lib only
#   native/windows/xbuild.sh shell      # interactive shell in the toolchain
# The MSVC build (CMakeLists.txt, build.ps1) is the release path; this one
# exists so the code compiles and links on every commit without a Windows box.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
IMAGE=remotectl-xbuild-win
docker build -q -t "$IMAGE" -f "$ROOT/native/windows/Dockerfile.xbuild" "$ROOT/native/windows" >/dev/null
RUN=(docker run --rm -v "$ROOT":/src -v "$ROOT/.tools/gomod":/go/pkg/mod
     -v remotectl-gocache-win:/root/.cache/go-build -w /src "$IMAGE")
case "${1:-all}" in
  lib)   "${RUN[@]}" make -C native/windows -f Makefile.mingw ;;
  shell) docker run --rm -it -v "$ROOT":/src -v "$ROOT/.tools/gomod":/go/pkg/mod -w /src "$IMAGE" bash ;;
  all)
    "${RUN[@]}" sh -c 'make -C native/windows -f Makefile.mingw && cd agent && \
      GOOS=windows GOARCH=amd64 CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc CXX=x86_64-w64-mingw32-g++ \
      GOFLAGS=-mod=mod go build -trimpath -ldflags "-s -w" -o dist/windows/remote-agent.exe ./cmd/remote-agent && \
      ../native/windows/check-imports.sh dist/windows/remote-agent.exe' ;;
  *) echo "usage: $0 [all|lib|shell]" >&2; exit 2 ;;
esac
