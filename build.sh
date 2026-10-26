#!/bin/sh
# build.sh — build sparta-rtc (WebRTC DataChannel <-> UDP gateway) for
# macOS/arm64 (the deployable) and linux/amd64 (the lab proof binary).
#
# Prerequisite: a Go toolchain (>= the version in go.mod). If one is
# not on PATH, this script uses the user-local install at
# ~/workspace/.tools/go if present; otherwise it downloads the pinned
# toolchain tarball from https://go.dev/dl/ into ~/workspace/.tools/
# (user-local, nothing system-wide, no sudo).
#
# Dependency versions are pinned by go.mod/go.sum in this directory.
# Output: dist/sparta-rtc-darwin-arm64 and dist/sparta-rtc-linux-amd64,
# both static (CGO_ENABLED=0).
set -eu
cd "$(dirname "$0")"

GO_VERSION="go1.27.3"
TOOLS_DIR="$HOME/workspace/.tools"

if command -v go >/dev/null 2>&1; then
    GO=go
elif [ -x "$TOOLS_DIR/go/bin/go" ]; then
    GO="$TOOLS_DIR/go/bin/go"
else
    echo "build.sh: no Go toolchain found; installing $GO_VERSION user-locally into $TOOLS_DIR"
    mkdir -p "$TOOLS_DIR"
    OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
    ARCH="$(uname -m)"
    case "$ARCH" in
        x86_64) ARCH=amd64 ;;
        aarch64 | arm64) ARCH=arm64 ;;
    esac
    TARBALL="$GO_VERSION.$OS-$ARCH.tar.gz"
    curl -fsSLO "https://go.dev/dl/$TARBALL" --output-dir "$TOOLS_DIR"
    tar -xzf "$TOOLS_DIR/$TARBALL" -C "$TOOLS_DIR"
    GO="$TOOLS_DIR/go/bin/go"
fi
echo "build.sh: using $("$GO" version)"

export CGO_ENABLED=0
export GOPATH="${GOPATH:-$TOOLS_DIR/gopath}"
export GOCACHE="${GOCACHE:-$TOOLS_DIR/gocache}"

"$GO" mod download
mkdir -p dist
"$GO" build -trimpath -ldflags='-s -w' -o dist/sparta-rtc-linux-amd64 .
GOOS=darwin GOARCH=arm64 "$GO" build -trimpath -ldflags='-s -w' -o dist/sparta-rtc-darwin-arm64 .

echo "build.sh: built:"
ls -l dist/
sha256sum dist/* 2>/dev/null || shasum -a 256 dist/*
