#!/usr/bin/env bash
# Fedora 44: build Windows x64 binaries, portable ZIP, and NSIS installer.
set -euo pipefail
cd "$(dirname "$0")/.."
export GO="${GO:-go}"
export GOCACHE="${GOCACHE:-$PWD/.build/go-cache}"
mkdir -p .build/windows-wheels .build/nsis-rpms .build/nsis
python3 -m pip download --only-binary=:all: --platform win_amd64 --python-version 313 --implementation cp --abi cp313 --dest .build/windows-wheels 'PySide6==6.11.2'
python3 scripts/assemble-windows.py
if ! command -v makensis >/dev/null; then
    dnf download --destdir .build/nsis-rpms mingw-nsis-base-3.11-3.fc44 mingw32-nsis-3.11-3.fc44 mingw64-nsis-3.11-3.fc44
    for rpm in "$PWD"/.build/nsis-rpms/*.rpm; do
        (cd .build/nsis && rpm2cpio "$rpm" | cpio -idmu --quiet)
    done
    export NSISDIR="$PWD/.build/nsis/usr/share/nsis"
    compiler="$PWD/.build/nsis/usr/bin/makensis"
else
    compiler=makensis
fi
"$compiler" packaging/handoff.nsi
sha256sum dist/*windows*.exe dist/*windows*.zip > dist/SHA256SUMS-windows.txt
