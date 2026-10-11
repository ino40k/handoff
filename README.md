# handoff

A Qt 6 Quick / PySide6 desktop file-transfer utility with a Go engine that uses
tailcat directly. No Tailscale installation, account, shared tailnet, port
forwarding, or handoff coordination server is used. Both devices need outbound
internet connectivity to the public tailcat DERP service for discovery; file
traffic upgrades to encrypted direct UDP when possible.

## Install and use

Download installers and portable builds from [GitHub Releases](https://github.com/ino40k/handoff/releases/latest). Local builds write their artifacts to `dist/`:

* **Fedora:** `handoff-1.0.2-1.fc44.x86_64.rpm` — install with
  `sudo dnf install ./dist/handoff-1.0.2-1.fc44.x86_64.rpm`, then open **handoff**
  from the application launcher.
* **Windows 11:** `handoff-1.0.2-windows-x64-setup.exe` — per-user installer,
  Start menu integration, notification shortcut, protocol handler, and uninstaller.
* **Windows portable:** `handoff-1.0.2-windows-x64-portable.zip` — extract the
  **entire folder**, then run `handoff.exe`. Keep the runtime and frontend folders
  alongside it. Python, Go, Qt, and Tailscale are not installation prerequisites.
* **Linux portable:** `handoff-1.0.2-linux-x86_64.tar.gz` — extract and run
  `handoff/handoff` on Fedora with the normal desktop system libraries installed.

Choose a device name and receiving folder in the first-launch wizard. Add a
peer by sharing an invitation privately. The recipient's **Connect and approve**
action approves the invitation owner; the owner must approve the recipient in
Peers. Invitations expire after 15 minutes and permit one recipient identity.

Drop files or folders onto a peer, or use Files / Folder. Select several peers
with the checkboxes for independent requests to each device. The recipient
reviews the whole manifest before accepting or declining. Pending requests
survive restarts and notification dismissal. Approved, unchanged transfers
resume automatically; altered or missing source files require a new request.
Use the transfer menu to reorder the send queue. Receiving files are also
serialized through a receiver grant so multiple senders cannot compete for
its next file. Sending and receiving are independently sequential.

Closing the window hides it. Transfers continue in the tray; use its **Quit
handoff** action to stop. Launching again activates the existing instance.

## Source and builds

`backend/` contains the durable state, transfer protocol, tailcat integration,
and Windows launcher. `frontend/` contains the PySide6 host and QML interface.
`assets/` contains original SVG, PNG and ICO artwork. `packaging/` and `scripts/`
contain the self-contained RPM and Windows build procedures.

Development requires Go **1.27.1** and Python with PySide6 **6.11.2**.
The tailcat pin is
`v0.7.1-0.20261007201807-52fbad323e9d` (full commit
`52fbad323e9d352f1febb623e0975368e9a8cbdd`), with checksums in `backend/go.sum`.

```sh
python3 -m venv .venv
.venv/bin/python -m pip install -r requirements.txt
(cd backend && go build -trimpath -o ../bin/handoff-engine ./cmd/handoff-engine)
.venv/bin/python frontend/main.py
```

`bash scripts/build-fedora.sh` builds the frozen Qt/Python distribution and RPM.
It requires `rpmbuild`, the Go toolchain and Python venv support. Set `GO` to a
nonstandard Go executable if necessary.

`bash scripts/build-windows-cross.sh` builds the actual Windows portable ZIP and
NSIS installer **on Fedora 44**. It downloads pinned Windows Qt wheels, verifies
the SHA-256 of embedded Python **3.13.16**, cross-compiles Go, and extracts the
Fedora NSIS **3.11** compiler into the build directory. It does not install
system packages. `scripts/build-windows.ps1` produces the same layout on Windows
with Python 3.13, Go 1.27.1 and NSIS 3.11 installed for building.

The launcher resource is checked in. Regenerate it after changing the icon or
manifest with `go run github.com/akavel/rsrc@v0.10.2 -arch amd64 -ico
assets/handoff.ico -manifest packaging/handoff.manifest -o
backend/cmd/handoff-launcher/resource_windows.syso`.

## State, logs and upgrades

Fedora state: `~/.local/share/handoff/state.json`.
Windows state: `%LOCALAPPDATA%\handoff\state.json`.
Logs: the `logs` folder next to state. Settings provides **Open logs folder** and
**Collect sanitized diagnostics**. Both components rotate five 2 MiB backups.
Bundles retain event categories, correlation IDs, counts and transport scope;
they remove detailed errors, paths, filenames, names and credentials.

Quit handoff before manually upgrading. Installation files are separate from
identity, peers, preferences, approvals and history, which upgrades and
uninstallation preserve. Windows uninstall removes managed startup and protocol
registration. Fedora's retained per-user autostart entry has `TryExec`, so an
uninstalled executable is not started; disabling startup before uninstall also
removes that entry. Clear history removes visible terminal records while keeping
small authenticated receipts so an offline sender can still learn the outcome.

## Validation and limits

See [docs/TESTING.md](docs/TESTING.md) for actual checks and the Windows VM test
procedure. Fedora tests cover live public-DERP transport, direct paths, forced
relay fallback, pairing, explicit transfer approvals, automatic reconnection,
restart resume, integrity, conflicts, revocation, notifications and frozen-app
single-instance activation. **Windows artifacts are cross-built; Windows runtime
behavior has not been tested in the VM.**

Symbolic links, special files, traversal paths, Windows reserved filenames,
case-conflicting manifest entries and unsupported filename characters are
rejected. Directory trees include empty folders and zero-byte files. Manifest
limits are 100,000 entries / 32 MiB of metadata; progress uses 4 MiB verified
blocks. A running application's source files must remain unchanged. Disk or
integrity failures pause the affected download and surface its error; Retry
verifies storage again before resuming.

Public relay service availability and bandwidth are upstream constraints. A
fixed relay region persists across restarts; its endpoints refresh from the
upstream map. If a region is permanently retired, re-pairing may be necessary.
Tailcat currently describes its wrapper as experimental; the integration's
security boundaries and scope are documented in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).
