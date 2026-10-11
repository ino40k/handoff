#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
root="$PWD"
export GOCACHE="${GOCACHE:-$root/.build/go-cache}"
export GO="${GO:-go}"
mkdir -p bin .build
(cd backend && "$GO" test ./... && "$GO" build -trimpath -ldflags='-s -w' -o ../bin/handoff-engine ./cmd/handoff-engine)
python3 -m venv .build/venv
.build/venv/bin/python -m pip install -r requirements-build.txt
.build/venv/bin/python scripts/render-icons.py
.build/venv/bin/python -m PyInstaller --noconfirm --clean packaging/handoff.spec
.build/venv/bin/python scripts/collect-licenses.py dist/handoff/_internal
rpmbuild -bb --define "_topdir $root/.build/rpm" --define "bundle_dir $root/dist/handoff" --define "source_dir $root" --define '__brp_strip /bin/true' --define '__brp_strip_comment_note /bin/true' --define '__brp_python_bytecompile /bin/true' packaging/handoff.rpm.spec
cp .build/rpm/RPMS/x86_64/handoff-*.rpm dist/
tar -C dist -czf dist/handoff-1.0.2-linux-x86_64.tar.gz handoff
sha256sum dist/*.rpm dist/*.tar.gz > dist/SHA256SUMS-linux.txt
