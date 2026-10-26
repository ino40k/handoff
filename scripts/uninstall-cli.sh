#!/bin/sh
set -eu
[ "$(id -u)" -eq 0 ] || { echo 'Run with sudo.' >&2; exit 1; }
systemctl disable --now handoff-cli || true
rm -f /etc/systemd/system/handoff-cli.service /usr/local/bin/handoff-cli
systemctl daemon-reload
rm -f /usr/local/lib/handoff-cli/uninstall-cli.sh
rmdir /usr/local/lib/handoff-cli 2>/dev/null || true
for notice in LICENSE CLI.md THIRD-PARTY.md THIRD-PARTY-LICENSES.txt; do
 rm -f "/usr/local/share/doc/handoff-cli/$notice"
done
rmdir /usr/local/share/doc/handoff-cli 2>/dev/null || true
echo 'Removed executable and boot service. Retained account/group, /etc/handoff-cli, /var/lib/handoff-cli, receiving files, approvals, history, backups and partial downloads.'
echo 'Reinstallation with the same account preserves identity. No user data was deleted.'
