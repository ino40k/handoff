#!/bin/sh
# Official release installer. State and receiving files are never replaced.
set -eu
umask 077
repo=https://github.com/ino40k/handoff
version=cli-v1.0.0
archive=
checksums=
while [ "$#" -gt 0 ]; do
 case "$1" in
  --version|--archive|--checksums) key=$1; shift; [ "$#" -gt 0 ] || { echo "Missing value for $key" >&2; exit 1; }; case "$key" in --version) version=$1;; --archive) archive=$1;; --checksums) checksums=$1;; esac;;
  --help) echo 'sudo sh install-cli.sh [--version cli-vX.Y.Z] [--archive LOCAL.tar.gz --checksums LOCAL.txt]'; exit 0;;
  *) echo "Unknown option: $1" >&2; exit 1;;
 esac
 shift
done
[ "$(id -u)" -eq 0 ] || { echo 'Run this installer with sudo (or as root).' >&2; exit 1; }
[ "$(uname -s)" = Linux ] || { echo 'Linux is required.' >&2; exit 1; }
case "$(uname -m)" in x86_64|amd64) arch=amd64;; *) echo 'This release supports Linux x86-64 only.' >&2; exit 1;; esac
printf '%s\n' "$version" | LC_ALL=C grep -Eq '^cli-v[0-9]+\.[0-9]+\.[0-9]+$' || { echo 'Invalid CLI release tag.' >&2; exit 1; }
[ -d /run/systemd/system ] || { echo 'A running systemd is required, including inside LXC.' >&2; exit 1; }
for command in systemctl systemd-analyze sha256sum tar install runuser setpriv getent useradd groupadd id stat sed awk grep cut basename cp mv rm sleep; do command -v "$command" >/dev/null || { echo "Missing system utility: $command" >&2; exit 1; }; done
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
asset=handoff-cli-${version#cli-v}-linux-$arch.tar.gz
if [ -n "$archive" ]; then
 [ -n "$checksums" ] || { echo '--checksums is required with a local archive.' >&2; exit 1; }
 cp -- "$archive" "$work/$asset"
 cp -- "$checksums" "$work/SHA256SUMS-cli.txt"
else
 command -v curl >/dev/null || { echo 'Install curl first.' >&2; exit 1; }
 base=$repo/releases/download/$version
 curl --proto '=https' --tlsv1.2 -fsSL --retry 2 "$base/$asset" -o "$work/$asset" || { echo "Release $version is not available. No files were installed." >&2; exit 1; }
 curl --proto '=https' --tlsv1.2 -fsSL --retry 2 "$base/SHA256SUMS-cli.txt" -o "$work/SHA256SUMS-cli.txt"
fi
# Select exactly one fixed filename. Never pass untrusted checksum filenames to -c.
expected=$(awk -v name="$asset" '$2==name && length($1)==64 && $1 !~ /[^0-9a-f]/ {print $1}' "$work/SHA256SUMS-cli.txt")
[ "${#expected}" -eq 64 ] || { echo 'Missing or ambiguous release checksum.' >&2; exit 1; }
actual=$(sha256sum "$work/$asset"); actual=${actual%% *}
[ "$actual" = "$expected" ] || { echo 'Checksum mismatch. Nothing installed.' >&2; exit 1; }
# Extract only fixed regular files, excluding links, traversal and device nodes.
for item in handoff-cli/handoff-cli handoff-cli/handoff-cli.service handoff-cli/uninstall-cli.sh handoff-cli/LICENSE handoff-cli/CLI.md handoff-cli/THIRD-PARTY.md handoff-cli/THIRD-PARTY-LICENSES.txt; do
 count=$(tar -tzf "$work/$asset" | awk -v name="$item" '$0==name{n++} END{print n+0}')
 [ "$count" -eq 1 ] || { echo "Invalid archive entry: $item" >&2; exit 1; }
 type=$(tar -tvzf "$work/$asset" "$item" | cut -c1)
 [ "$type" = - ] || { echo "Archive entry is not a regular file: $item" >&2; exit 1; }
 tar -xOzf "$work/$asset" "$item" > "$work/$(basename "$item")"
done
chmod 0755 "$work/handoff-cli"
"$work/handoff-cli" version
config=/etc/handoff-cli
state=/var/lib/handoff-cli
if [ -f "$config/service-user" ]; then
 user=$(cat "$config/service-user")
 echo "Upgrade: preserving service account, identity, peers, settings and transfers."
else
 [ -r /dev/tty ] || { echo 'First installation needs an interactive terminal.' >&2; exit 1; }
 printf 'Service account [handoff-cli; or an existing non-root account with file access]: ' >/dev/tty
 IFS= read -r user </dev/tty; user=${user:-handoff-cli}
fi
printf '%s\n' "$user" | LC_ALL=C grep -Eq '^[a-z_][a-z0-9_-]{0,31}$' || { echo 'Invalid service account name.' >&2; exit 1; }
getent group handoff-cli >/dev/null || groupadd --system handoff-cli
if ! id "$user" >/dev/null 2>&1; then
 [ "$user" = handoff-cli ] || { echo 'Choose an existing account or the default handoff-cli account.' >&2; exit 1; }
 useradd --system --gid handoff-cli --home-dir "$state" --no-create-home --shell /usr/sbin/nologin handoff-cli
fi
[ "$(id -u "$user")" -ne 0 ] || { echo 'The transfer service must not run as root.' >&2; exit 1; }
primary=$(id -gn "$user")
# Leave existing directory permissions intact. Never recursively chown user data.
if [ ! -e "$state" ]; then install -d -o "$user" -g handoff-cli -m 0700 "$state"; fi
if [ ! -d "$state" ] || [ -L "$state" ]; then echo 'Unsafe state directory.' >&2; exit 1; fi
[ "$(stat -c %u "$state")" = "$(id -u "$user")" ] || { echo 'State directory owner does not match the service account.' >&2; exit 1; }
if [ ! -f "$config/service-user" ]; then
 printf 'Device name [Server]: ' >/dev/tty; IFS= read -r name </dev/tty; name=${name:-Server}
 printf 'Receiving directory [/var/lib/handoff-cli/received]: ' >/dev/tty; IFS= read -r receive </dev/tty; receive=${receive:-/var/lib/handoff-cli/received}
 case "$receive" in /*) ;; *) echo 'Receiving path must be absolute.' >&2; exit 1;; esac
 if [ ! -e "$receive" ]; then install -d -o "$user" -g handoff-cli -m 0700 "$receive"; fi
 printf 'Install boot service as %s, receiving in %s? [y/N] ' "$user" "$receive" >/dev/tty
 IFS= read -r confirm </dev/tty; case "$confirm" in y|Y|yes) ;; *) echo 'Installation canceled.'; exit 1;; esac
 # mktemp directory is private to root. Stage the verified executable for runuser.
 install -d -m 0755 /usr/local/lib/handoff-cli
 install -m 0755 "$work/handoff-cli" /usr/local/lib/handoff-cli/setup
 runuser -u "$user" -- /usr/local/lib/handoff-cli/setup init --data-dir "$state" --name "$name" --receive-dir "$receive"
 rm -f /usr/local/lib/handoff-cli/setup
fi
# Prepare everything before stopping an existing service. Preserve rollback binary.
sed -e "s/@USER@/$user/g" -e "s/@PRIMARY_GROUP@/$primary/g" "$work/handoff-cli.service" > "$work/unit"
# Verify against the staged executable, which already exists on a fresh install.
sed "s| /usr/local/bin/handoff-cli serve| $work/handoff-cli serve|" "$work/unit" > "$work/handoff-cli.service"
systemd-analyze verify "$work/handoff-cli.service"
install -d -m 0755 /usr/local/lib/handoff-cli
install -d -m 0700 "$config"
printf '%s\n' "$user" > "$config/service-user"
install -m 0755 "$work/uninstall-cli.sh" /usr/local/lib/handoff-cli/uninstall-cli.sh
if [ -f /usr/local/bin/handoff-cli ]; then cp /usr/local/bin/handoff-cli "$work/previous"; fi
if [ -f /etc/systemd/system/handoff-cli.service ]; then cp /etc/systemd/system/handoff-cli.service "$work/previous-unit"; fi
# Copy first, then atomically switch both installation files during the stop.
install -m 0755 "$work/handoff-cli" /usr/local/bin/handoff-cli.new
install -m 0644 "$work/unit" /etc/systemd/system/handoff-cli.service.new
rollback() {
 systemctl stop handoff-cli 2>/dev/null || true
 if [ -f "$work/previous" ]; then
  install -m 0755 "$work/previous" /usr/local/bin/handoff-cli
  if [ -f "$work/previous-unit" ]; then install -m 0644 "$work/previous-unit" /etc/systemd/system/handoff-cli.service; fi
  systemctl daemon-reload
  systemctl start handoff-cli || true
 fi
 echo 'Service failed; previous binary/unit restored when available. Check journalctl -u handoff-cli.' >&2
 exit 1
}
systemctl stop handoff-cli 2>/dev/null || true
mv -f /usr/local/bin/handoff-cli.new /usr/local/bin/handoff-cli
mv -f /etc/systemd/system/handoff-cli.service.new /etc/systemd/system/handoff-cli.service
systemctl daemon-reload || rollback
systemctl enable handoff-cli || rollback
systemctl start handoff-cli || rollback
sleep 2
systemctl is-active --quiet handoff-cli || rollback
install -d -m 0755 /usr/local/share/doc/handoff-cli
for notice in LICENSE CLI.md THIRD-PARTY.md THIRD-PARTY-LICENSES.txt; do
 install -m 0644 "$work/$notice" "/usr/local/share/doc/handoff-cli/$notice"
done
printf '\nInstalled. Use sudo handoff-cli status, peers, invite and --help.\n'
printf 'Service account: %s. Grant it source read and destination write access explicitly.\n' "$user"
printf 'Control is restricted to root, the service account and handoff-cli group members.\n'
printf 'Add only trusted administrators: sudo usermod -aG handoff-cli USER (then log in again).\n'
printf 'Upgrade by running this installer again. Uninstall: sudo sh /usr/local/lib/handoff-cli/uninstall-cli.sh\n'
