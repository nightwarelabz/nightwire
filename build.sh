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
TOOLS_DIR="${SPARTA_TOOLS_DIR:-$HOME/workspace/.tools}"

if [ -n "${GO:-}" ]; then
    command -v "$GO" >/dev/null 2>&1 || { echo "build.sh: GO is not executable: $GO" >&2; exit 1; }
elif command -v go >/dev/null 2>&1; then
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
    case "$OS-$ARCH" in
        linux-amd64) EXPECTED_SHA256=65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40 ;;
        linux-arm64) EXPECTED_SHA256=a06e99309dbcbdca1a8ccd2282c3ee146e55930886e26b4f1193269587165f49 ;;
        darwin-amd64) EXPECTED_SHA256=b6f8e931e4ab497fcde6da8ae4499a58f9d258e55c8a80824a516eae5950a750 ;;
        darwin-arm64) EXPECTED_SHA256=dd3908cf20959d798c1e06ebe5b72213f0505ce01373b40ce3110975a6cd99c2 ;;
        *) echo "build.sh: install Go manually for $OS-$ARCH" >&2; exit 1 ;;
    esac
    curl -fsSL "https://go.dev/dl/$TARBALL" -o "$TOOLS_DIR/$TARBALL"
    if command -v sha256sum >/dev/null 2>&1; then
        ACTUAL_SHA256=$(sha256sum "$TOOLS_DIR/$TARBALL")
    else
        ACTUAL_SHA256=$(shasum -a 256 "$TOOLS_DIR/$TARBALL")
    fi
    [ "${ACTUAL_SHA256%% *}" = "$EXPECTED_SHA256" ] || { echo "build.sh: Go archive checksum mismatch" >&2; exit 1; }
    tar -xzf "$TOOLS_DIR/$TARBALL" -C "$TOOLS_DIR"
    GO="$TOOLS_DIR/go/bin/go"
fi
echo "build.sh: using $("$GO" version)"

export CGO_ENABLED=0
export GOPATH="${GOPATH:-$TOOLS_DIR/gopath}"
export GOCACHE="${GOCACHE:-$TOOLS_DIR/gocache}"

"$GO" mod download
mkdir -p dist
GOOS=linux GOARCH=amd64 "$GO" build -trimpath -buildvcs=false -ldflags='-s -w' -o dist/sparta-rtc-linux-amd64 .
GOOS=darwin GOARCH=arm64 "$GO" build -trimpath -buildvcs=false -ldflags='-s -w' -o dist/sparta-rtc-darwin-arm64 .

echo "build.sh: built:"
ls -l dist/
(cd dist && { sha256sum sparta-rtc-* 2>/dev/null || shasum -a 256 sparta-rtc-*; } > SHA256SUMS)
cat dist/SHA256SUMS
