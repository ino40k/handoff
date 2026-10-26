#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
GO=${GO:-go}
export GOCACHE="${GOCACHE:-$PWD/.build/go-cache}"
version=$(sed -n 's/^const Version = "\([^"]*\)"/\1/p' backend/internal/headless/daemon_linux.go)
[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Cannot determine CLI version' >&2; exit 1; }
stage="$PWD/.build/cli-package/handoff-cli"
rm -rf "$stage"
mkdir -p "$stage" dist
(cd backend && "$GO" test ./... && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$GO" build -trimpath -buildvcs=false -ldflags='-s -w' -o "$stage/handoff-cli" ./cmd/handoff-cli)
cp packaging/cli/handoff-cli.service scripts/uninstall-cli.sh "$stage/"
cp LICENSE docs/CLI.md docs/THIRD-PARTY.md "$stage/"
# Development-only Python collects licenses; the shipped application is Go-only.
GO="$GO" python3 scripts/collect-cli-licenses.py "$stage/licenses"
rm -rf "$stage/licenses"
chmod 0755 "$stage/handoff-cli" "$stage/uninstall-cli.sh"
asset="handoff-cli-$version-linux-amd64.tar.gz"
# Stable archive metadata: no build account, host paths, or timestamps.
tar --sort=name --mtime='UTC 2026-01-01' --owner=0 --group=0 --numeric-owner -C .build/cli-package -czf "dist/$asset" handoff-cli
python3 scripts/check-cli-package.py "dist/$asset"
(cd dist && sha256sum "$asset" > SHA256SUMS-cli.txt)
sed "s/^version=cli-v.*/version=cli-v$version/" scripts/install-cli.sh > dist/install-cli.sh
chmod 0755 dist/install-cli.sh
printf 'Built dist/%s and dist/SHA256SUMS-cli.txt\n' "$asset"
