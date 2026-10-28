# Architecture and invariants

## Process and transport boundaries

PySide6 owns the native pickers, tray, notifications, startup registration,
single-instance activation and QML. A supervised Go child owns keys, networking,
approvals and file I/O. Commands and public snapshots use JSON lines over
inherited stdin/stdout pipes. The control interface binds no TCP port and returns
no private keys or saved tailcat addresses. Native file and folder pickers use deferred `QFileDialog.open()` calls and
finished callbacks, so background snapshots cannot destroy a QML delegate while
a blocking picker handler is on its stack. Recipients, request IDs and directory
selectors are captured before opening the dialog. Canceling sends no command.

Local activation uses a user-scoped
Qt local socket plus QLockFile; it only opens the interface or a transfer ID.

The Go engine directly imports the pinned tailcat library. `Server.Listen` and
`Client.DialTCPPort` carry the application protocol through userspace WireGuard;
there is no direct-IP or existing-Tailscale transport fallback. Per-peer TCP
streams use a small HTTP protocol solely inside these tunnels. `DiscoPing`
reports the actual path; direct endpoints are classified as loopback, local
network or internet, without exporting the endpoint IP in sanitized diagnostics.

## Pairing and revocation

A persistent server identity consists of a node private key, independent PSK
and fixed DERP region. Each pairing also creates its own persistent outbound
client identity. This prevents several independent tailcat engines from sharing
a client node key on the same DERP connection. Names never identify devices.

A separate ephemeral, PSK-protected tailcat listener accepts only the pairing
protocol for 15 minutes. A base64url invitation embeds its real tailcat address,
256-bit token, name, version and expiration; it is a secret, not a short lookup
code. The first authenticated client claims it, and the owner must approve.
Entering the invitation explicitly approves the owner. Both sides exchange
persistent server addresses and client public keys inside the encrypted tunnel.
The approved recipient alone can retrieve the result until its acknowledgement
closes the invitation. Restarting invalidates incomplete invitations.

Saved addresses use official upstream region IDs, not arbitrary embedded relay
hosts. The trusted listener's `AllowClient` is an initially empty `KeySet`.
Incoming HTTP connections obtain their authenticated tunnel key with `PeerKey`;
every request also checks the current trust store. Revocation commits under the
receiver lock, removes admission, calls `DisconnectClient`, closes outgoing
clients and cancels unfinished requests. Old addresses do not restore access.

## Durable transfers

A request binds a random transfer ID, authenticated peer, complete ordered
manifest digest, size, per-file SHA-256 and per-block SHA-256. Receiver approval
is durable before bytes can be accepted. Both sides retain pending requests.
Recipient decisions cover the entire manifest. Dismissing a notification changes
no transfer state. No automatic acceptance setting exists.

The sender hashes source files once, rejects links/special files, and verifies
file identity, size and the requested block before sending it. Manifest/source
substitution cannot resume the old request. A sequential sending worker and a
receiver grant serialize files; pausing preserves checkpoints. Control/status
traffic continues independently so approvals, cancellations and reconnection
remain possible. Transfer deadlines allow slow relayed blocks without applying
an upload bandwidth limiter.

Approved files are created through Go `os.Root` in
`destination/.handoff-partials/<transfer-id>/`. Writes are flushed before the
checkpoint transaction and acknowledgement. Restart re-hashes acknowledged
blocks, truncates unverified tails and retains approval. An unavailable or unsafe
receive destination pauses that transfer with a visible failure. Retry repeats
verification. Existing files and links are never opened as receive targets.

Finalization verifies complete file hashes and moves each top-level selected
file or folder atomically under a conflict-free name. Linux uses anchored
`renameat2(RENAME_NOREPLACE)`; Windows uses `MoveFile`, which refuses replacement.
The finalization journal survives a crash between rename and state commit.
Recovery verifies the finalized tree before trusting that journal. A transfer
containing multiple selected roots may expose already verified roots before all
roots finish; unfinished roots remain in staging and the overall request stays
unfinished. Windows root path stability assumes no malicious process running
as the same user concurrently replaces the selected destination directory.

State updates clone the current state, write a restricted temporary file, flush,
atomically replace, and publish only after success. Fedora additionally flushes
the directory; Windows requests write-through replacement. Windows state has a
protected user-only DACL, and Linux state uses 0700/0600 permissions. Corrupt
identity/state never silently generates a replacement identity. Source paths are
stored locally, never sent to peers. Clearing visible history retains compact
terminal receipts to prevent replay and report outcomes to offline senders.

## Diagnostics and upstream boundary

Structured logs carry UTC timestamps, component, level, category, event and
request/transfer/peer correlation ID. Tailcat details are redacted before logging;
private keys, PSKs, complete addresses and invitation strings are excluded.
Sanitized ZIPs use an allowlist of fields and never include state.json. Qt and
Python exceptions, filesystem failures and backend supervision events are logged.

The design assumes the OS account and other processes running as that account
are trusted. It does not protect against local administrators or malicious
same-user processes. It serves no shell, proxy, exit node or generic file share.
Upstream's wrapper is experimental and its public relays are rate-limited:
https://github.com/tailscale/tailcat/blob/52fbad323e9d352f1febb623e0975368e9a8cbdd/SECURITY.md
https://github.com/tailscale/tailcat/tree/52fbad323e9d352f1febb623e0975368e9a8cbdd#key-management


## Headless Linux host

`internal/control` shares the original desktop command/snapshot/diagnostic
implementation. The desktop keeps its inherited stdio transport. The Linux
`handoff-cli` binary adds a Unix-socket service host with exclusive state lock,
protected socket directory, group-restricted control and no TCP control port.
Its separate profile keeps desktop identity/state untouched. All control users
are trusted administrators of the non-root service account. Secret invitations
are read privately; logs record command categories/IDs, not parameters.

Durable source-preparation jobs are committed before acknowledging `send`.
Replayed preparation binds stable transfer IDs to job/peer IDs, preventing
requests from duplicating after a crash at the job/transfer transaction boundary.
Workers use daemon context rather than connection lifetime, so SSH disconnects
cannot cancel transfers. Incoming offers and the wire protocol are unchanged.

Linux CLI receiving adds a local reviewed merge policy. It hashes existing
regular files, binds overwrite approval to a token over the immutable manifest,
absolute destination and current hashes, and records that decision durably.
Identical files consume no network/staging space and are reverified at recovery
and finish. Incoming bytes still use the original chunk validator/checkpoints.
Finalization opens anchored parent directories, rejects symlinks, moves approved
originals exclusively into persistent backups, verifies them again, and installs
verified staged files with RENAME_NOREPLACE. Crashes before/after either move
are recoverable from the durable target hashes, staged files and backups.
Concurrent edits stop finalization rather than silently overwriting new data.
Backups remain for administrator review. Desktop receiving keeps its existing
conflict-free top-level rename policy; local merge decisions never enter the
network manifest or alter desktop approval behavior.


Headless state additionally uses a checksummed write-ahead progress journal.
Every frame is fsynced before publishing state/acknowledging bytes, carries a
strict revision and sparse changed offsets, and omits immutable manifests.
Recovery rejects corrupt complete frames or revision gaps; an unacknowledged
torn final frame is truncated only by the exclusively locked writer. An atomic
full snapshot records its revision before truncating the older WAL, making a
crash during compaction safe. Manifest, trust, approval-policy and identity changes
force a full checkpoint. Desktop keeps its full-state writing policy, and both
new hosts can read journals. Partial-file hash verification remains authoritative
after journal replay. Snapshots deep-copy mutable slices/maps directly, avoiding
JSON roundtrips, and authentication reads only the trust map.


## Incoming resource limits (desktop 1.0.8 / CLI 1.0.1)

New incoming requests account for retained manifests in every state and compact
replay receipts. Conservative metadata budgets are 64 MiB per peer and 128 MiB
per profile; request/receipt counts are limited to 65,536 per peer and 262,144 per
profile. Clearing finished history frees full-manifest storage while retaining
replay receipts and their accounting. At the replay-record limit, new requests
are refused; existing requests, approvals, retries and authenticated replays
continue. Upgrades preserve existing state even if it exceeds the new limits.

Peer JSON responses, including chunk acknowledgements, are limited to 4 MiB
before decoding; this accommodates the maximum 100,000-entry progress vector.
Oversized, concatenated or otherwise malformed replies are rejected.

Linux finalization uses separate opened staging and destination directory
handles with single-component names. Renaming or replacing a staging ancestor
does not redirect the source move. Linux staging parents and transfer roots
must be private and owned by the receiving account. Receiving directories can
remain shared; the reserved staging directories must be private. Windows retains
its documented trusted-account pathname-stability assumption.
