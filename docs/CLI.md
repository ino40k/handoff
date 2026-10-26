# handoff cli

A Go-only Linux x86-64 service and terminal client. Ubuntu 24.04 and Debian 13
are the initial packaging targets. No graphical session, Python runtime, TUN
device, Tailscale daemon, account, tailnet or port forwarding is required. It
uses exactly the desktop engine's tailcat transport and protocol 1.

## Install and upgrade

Install the [CLI release](https://github.com/ino40k/handoff/releases/tag/cli-v1.0.0)
on a Linux x86-64 server with systemd:

```sh
curl --proto '=https' --tlsv1.2 -fsSL https://github.com/ino40k/handoff/releases/download/cli-v1.0.0/install-cli.sh | sudo sh
```

For offline installation, copy `install-cli.sh`,
`handoff-cli-1.0.0-linux-amd64.tar.gz`, and `SHA256SUMS-cli.txt` together to the
server, then run this one installation command from that directory:

```sh
sudo sh install-cli.sh --archive ./handoff-cli-1.0.0-linux-amd64.tar.gz --checksums ./SHA256SUMS-cli.txt
```

The interactive installer reads prompts from the controlling terminal. It
verifies the archive's SHA-256 before installing anything executable, selects
a non-root account, asks for a device name and receiving directory, configures
a systemd boot service, and starts it. It needs standard `curl` (online mode),
`tar`, `sha256sum`, `util-linux`/`runuser`, `passwd`/account utilities and systemd.
Ubuntu/Debian normally provide these; a minimal container may need
`apt-get install curl ca-certificates systemd systemd-sysv util-linux passwd`.

The release page's **install-cli.sh** asset is the online installer. Running it
with sudo and no `--archive` argument downloads and verifies that release
automatically. CLI release tags start with `cli-v`; desktop releases contain
the graphical application and use separate installers.

Run the installer again with a new `--version cli-vX.Y.Z` (or a verified local
archive/checksum pair) to upgrade. It stops the service only after validating the
new binary, retains the service user and all state, atomically replaces the
binary, and restarts. The previous binary is restored if service startup fails.
Keep backups of `/var/lib/handoff-cli` securely; they contain private keys.
Never put that directory or diagnostic logs in Git or a release.

To uninstall without deleting data:

```sh
sudo sh /usr/local/lib/handoff-cli/uninstall-cli.sh
```

This removes the executable and boot unit. The service account, configuration,
identity, peers, approvals, queue, history, staged data and received files remain.

## Accounts and file permissions

The default account is `handoff-cli`, without an interactive login. An existing
non-root account can be selected during setup when it already has the necessary
filesystem access. The Go service never runs as root. A short-lived `setpriv` launcher drops UID,
GID and the entire capability bounding set before executing it. This retains
systemd syscall restrictions while avoiding an older systemd CAP_SYS_ADMIN
setup failure in restricted containers. The installer does not
recursively change ownership or permissions on existing directories.

Run control commands with `sudo handoff-cli ...`, or explicitly add a trusted
local administrator to the `handoff-cli` group and log in again. Group members
can approve transfers, access any sources readable by the service, select any
writable destination, and administer peers. Treat that group as full control
of the service account, not read-only monitoring access.

For outgoing sources, the service account needs directory traversal and file
read permission. For receiving it needs read/traversal, creation, rename and write
permission. Use your normal ownership, groups or ACLs; do not make directories
world-writable. Receiving files use 0600 and directories 0700. A media server
running as another user will need explicitly granted access after reception,
or select its appropriate non-root account for the handoff service. Destination
overrides do not bypass filesystem permissions. SSH users' relative source paths
are converted to absolute paths before sending the request to the service.

The system unit avoids mount namespaces, privileged networking and device
access, so a normal unprivileged LXC with a running systemd is sufficient in
principle. Outbound HTTPS/TCP to public DERP services and UDP for direct paths
must be permitted by the VM/container host and firewall. An LXC that does not run
systemd cannot use the boot installer: run `handoff-cli serve` under its existing
supervisor instead. Real VM-to-LXC internet tests remain a deployment check.

## Pair two machines

On the invitation owner:

```sh
sudo handoff-cli invite
```

Share the entire secret invitation privately. On the other machine:

```sh
sudo handoff-cli join
```

Paste at the hidden prompt. It is intentionally not a command argument, so it
is not saved in shell history or process listings. Entering the invitation
approves its owner. The owner must separately review the request/fingerprint:

```sh
sudo handoff-cli pairings
sudo handoff-cli approve PAIRING_ID
sudo handoff-cli peers
```

Invitations expire after 15 minutes, are single-use, and incomplete pairing does
not survive a service restart. Paired identities and authenticated addresses
persist. `rename PEER 'Server name'` changes the local label; `revoke PEER`
removes trust, disconnects authenticated streams and cancels unfinished requests.
Knowing an old address does not restore access. Peer names and unambiguous ID
prefixes work; duplicate names require an ID.

## Send, inspect, approve and monitor

```sh
sudo handoff-cli send 'Storage server' '/srv/files/Example collection'
sudo handoff-cli jobs
sudo handoff-cli status --watch
```

`send` durably records a preparation job before returning. A sequential worker
hashes files in bounded buffers and creates the ordinary desktop-compatible
request. Closing SSH does not interrupt hashing or transfers. A restart during
hashing restarts that scan; an idempotent job identifier prevents duplicate
requests after a crash at the commit boundary. `job retry JOB_ID` retries a
failed scan after permissions/source problems are fixed. `job cancel JOB_ID`
stops an unfinished scan durably; `job remove JOB_ID` removes a finished scan record.

On the receiver:

```sh
sudo handoff-cli pending
sudo handoff-cli inspect TRANSFER_ID
sudo handoff-cli inspect TRANSFER_ID --conflicts --destination /srv/incoming
sudo handoff-cli accept TRANSFER_ID --destination /srv/incoming
```

Every incoming request requires explicit whole-manifest approval, including
trusted peers. Inspection lists the sender, every path and size, file/directory
counts, timestamps and destination. Pending requests survive restarts. `deny ID`
rejects the entire request. No automatic acceptance setting exists.

Conflict inspection hashes existing regular files. Identical files are verified
and skipped without network retransmission or duplicate disk storage. Different
files require a reviewed batch decision:

```sh
sudo handoff-cli inspect TRANSFER_ID --conflicts
sudo handoff-cli accept TRANSFER_ID --overwrite --review REVIEW_TOKEN
```

Copy the token from inspection. It binds permission to the immutable manifest,
destination and existing file hashes; changed files invalidate it. Later changes
stop finalization. Existing directories merge, preserving unrelated contents.
Symlinks, special files and file/directory type conflicts are rejected. Changed
originals are retained beneath
`DESTINATION/.handoff-partials/TRANSFER_ID-replaced/`, including after successful
replacement. Review and remove those backups manually when no longer needed.
Never delete active partials. The transfer is completed only after content
verification and safe per-file finalization. A directory may contain already
verified completed files while other files remain staged.

`status --watch` updates the same lines in a terminal, without a full-screen
interface. It shows direction, peer, route, file and overall progress, smoothed
speed, coarse ETA, pending/queue/preparation counts. Ctrl-C leaves the service
running. Redirected monitoring emits changes at most every ten seconds;
`--json --watch` emits one JSON document per five seconds for automation.
`queue`, `history`, `jobs`, `peers`, `pending`, `inspect`, `settings` and `status`
also support `--json`.

## Queue and recovery

```sh
sudo handoff-cli pause TRANSFER_ID
sudo handoff-cli resume TRANSFER_ID
sudo handoff-cli move TRANSFER_ID up
sudo handoff-cli cancel TRANSFER_ID
sudo handoff-cli history
sudo handoff-cli remove TRANSFER_ID
```

Sending and receiving each process one file at a time. New requests can be added
while another transfer runs. Queue order, approvals, pauses and verified 4 MiB
checkpoints persist. Reconnect/restart resumes accepted unchanged requests
without another approval. Temporary failures back off automatically; missing or
changed sources and disk, permission or integrity errors require intervention
and `retry ID`/`resume ID`. Source changes need a new request when their approved
hashes no longer match. `remove` only removes terminal records, retains incoming
replay receipts, and waits for outgoing cancellation acknowledgement.

Identical destination files are reverified after restart. If one changes after
being skipped, the transfer pauses; restore it to its approved contents and
retry, or cancel and create a new request. Approved overwrites use an exclusive
backup move and exclusive install, with recovery across either move. They never
silently destroy a newly created destination file.

## Settings, diagnostics and service

```sh
sudo handoff-cli settings --name 'Storage server' --receive-dir /srv/incoming
sudo handoff-cli logs
sudo handoff-cli diagnostics
sudo systemctl status handoff-cli
sudo journalctl -u handoff-cli -f
```

State and preparation jobs: `/var/lib/handoff-cli/` (0700; files 0600).
Administrative account record: `/etc/handoff-cli/service-user`.
Control socket: `/run/handoff-cli/control.sock` (0660, protected 0750 parent).
Rotating application logs: `/var/lib/handoff-cli/logs/backend.jsonl`, five 2 MiB
backups. Fatal service startup errors also appear in the systemd journal.
Sanitized diagnostics use the existing allowlisted ZIP exporter and omit state,
private keys, invitation/address secrets, paths, filenames and detailed errors.
Ordinary private logs can contain filesystem errors and paths: inspect before
sharing. The service does not send desktop notifications.

For an isolated development profile without system installation:

```sh
mkdir -p /tmp/handoff-example/received
./handoff-cli init --data-dir /tmp/handoff-example/data --name Example --receive-dir /tmp/handoff-example/received
./handoff-cli serve --data-dir /tmp/handoff-example/data --socket /tmp/handoff-example/control.sock
# In another terminal:
./handoff-cli --socket /tmp/handoff-example/control.sock status
```

## Build and release

Only the pinned Go toolchain is needed to build the binary. Packaging additionally
uses Python 3 to collect license text; Python is not shipped or needed at runtime.

```sh
(cd backend && CGO_ENABLED=0 go build -trimpath -buildvcs=false -o ../bin/handoff-cli ./cmd/handoff-cli)
GO=go bash scripts/build-cli.sh
```

The package has a static Linux amd64 executable, unit template, uninstaller,
license texts and this guide. The installer retains documentation and complete
third-party notices under `/usr/local/share/doc/handoff-cli/`. Asset names and `cli-v1.0.0` tags are distinct from
the desktop's `v1.0.7` assets. Desktop packaging and its version are unchanged.

`.github/workflows/cli.yml` validates Go/race checks, installer syntax, real tailcat
interoperability and restart tests; builds a deterministic archive/checksums;
and uploads CI artifacts. A matching CLI tag additionally creates a **draft**
release. Review code/artifact privacy, checksums and test results before explicitly
publishing it. Keep CLI releases marked `--latest=false` so the desktop download
link continues to point to desktop installers. No release or commit is pushed
automatically by local build scripts.

Existing protocol compatibility retains the 100,000-entry / 32 MiB manifest
bounds, cross-platform filename restrictions, and 4 MiB blocks. A many-hundred-GB
collection fits when its metadata fits; larger manifests must be split into
multiple requests. A single file may be tens of GB; hashing is streaming.
Headless progress uses a checksummed, fsynced write-ahead journal, compacted
into the atomic state snapshot after 256 updates or 8 MiB. Manifest/identity/
approval changes use a full atomic checkpoint. Routine monitoring returns compact
summaries; full contents are retrieved only for inspection. Large histories still
consume memory and can be removed when no longer needed. Back up the entire state
directory, including `progress.wal`, while the service is stopped; copying only
`state.json` can lose newer progress.

Tailcat remains experimental. Public DERP throughput and availability have no
application guarantee. `peers` reports `direct` with local/loopback/internet scope
or `relayed` with public relay scope using actual tailcat disco results. A local
scope still means encrypted tailcat traffic; it is not an IP transport bypass.
See [upstream security](https://github.com/tailscale/tailcat/blob/52fbad323e9d352f1febb623e0975368e9a8cbdd/SECURITY.md)
and [tailcat documentation](https://github.com/tailscale/tailcat/blob/52fbad323e9d352f1febb623e0975368e9a8cbdd/README.md).
