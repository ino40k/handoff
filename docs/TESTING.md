# Validation

The top-level `tests/` directory is kept locally and excluded from the GitHub
repository at the owner’s request. Commands below that refer to it apply to the
full development workspace. The Go tests under `backend/` remain in the source.

## Automated checks

```sh
(cd backend && go test ./... && go test -race ./internal/... && go vet ./...)
(cd backend && HANDOFF_NETWORK_TEST=1 go test -race -count=1 -run TestLive -v ./internal/network)
(cd backend && TS_DEBUG_ALWAYS_USE_DERP=true HANDOFF_NETWORK_TEST=1 go test -count=1 -run TestLiveTailcatPairingAndTransfer -v ./internal/network)
.venv/bin/python tests/ui_smoke.py
.venv/bin/python tests/picker_regression.py
.build/venv/bin/python tests/picker_regression.py --bundle dist/handoff
.build/venv/bin/python tests/drop_regression.py
.build/venv/bin/python tests/drop_regression.py --bundle dist/handoff
dbus-run-session -- .venv/bin/python tests/notification_smoke.py
.venv/bin/python tests/instance_smoke.py
```

Live tests require outbound internet access and local UDP sockets. Ordinary unit
tests require neither. UI/notification/instance checks use isolated profiles;
the notification test serves an isolated fake D-Bus desktop service to check the
real wire signature and exact activation ID without sending desktop notifications.
The picker regression also accepts `--bundle dist/handoff` to execute the compiled
bridge and QML from the frozen artifact. For desktop-level verification, set
`QT_QPA_PLATFORM=wayland QT_QUICK_BACKEND=software`; its isolated fixture window
selects temporary files and never uses real peers or transfers.

UI screenshots go to `tests/artifacts/` and are inspection aids, not fake app data.

Validated on Fedora 44 with Go 1.27.1, Python 3.14 and PySide6 6.11.2:

* Pairing and both approvals over public DERP, then authenticated file transfer.
* No data before acceptance; full received content equals the source.
* Direct path reported on one host; forced public relay path also transfers.
* Receiver restart triggers automatic client re-registration; sender restart
  resumes verified checkpoints without renewed approval.
* Integrity rejection, modified-source rejection, corrupt-progress truncation,
  interrupted finalization, file/folder conflicts, empty files/folders, traversal,
  links, reserved names, authentication, revocation, pause, cancel and ordering.
* Peer-row Send with changing snapshots while a picker is open, cancellation,
  file/folder selection, retained recipients, and explicit custom-destination approval.
* Native QtQuick peer-row drops of files, folders and multiple selections with
  spaces, Unicode and encoded filename characters; remote and mixed URL rejection.
* Race detector for core and live networking, QML load and both themes,
  notification activation, and frozen frontend/embedded engine startup.

These same-host live tests prove actual tailcat transport and relay fallback;
they do not certify NAT behavior across the Fedora/Windows networks. Test the
following on the VM before relying on Windows integration.

The Windows payload can also be checked on Fedora or Windows with
`python scripts/check-windows-runtime.py dist/handoff-windows`. It verifies the
transitive PySide binding dependencies and the Fusion style module, without
claiming a Windows runtime test. Launcher log tests cover fragmented secrets,
final traceback lines, oversized output and retention independently of Windows.

## Windows VM acceptance

1. Copy the setup EXE or portable ZIP, extract/install, and open `handoff.exe`.
   For an import/Qt startup failure, run `runtime\python.exe frontend\main.py`
   from the portable folder and collect the displayed error. No developer runtime
   should otherwise be needed. Launcher errors are also saved automatically to
   `%LOCALAPPDATA%\handoff\logs\launcher.jsonl`, including Qt import failures.
2. Give Fedora and Windows different names. Pair with an invitation and approve
   on both sides. Their device IDs must differ. Disable the VM's Tailscale client
   during the actual transfer test to confirm it is not the application transport.
3. Send files and nested folders in both directions, with existing filename
   conflicts, empty folders/files and Unicode names. Review individual sizes and
   paths. Notification clicks must select the corresponding request. Dismiss one
   notification, restart, and confirm that its unanswered request is still pending.
4. Accept a large unchanged file. Pause and resume, interrupt network access,
   close both applications or reboot, and verify automatic recovery without another
   approval. Compare SHA-256 hashes (PowerShell `Get-FileHash`; Fedora `sha256sum`).
5. Queue transfers, rearrange them, cancel a pending transfer while offline, then
   reconnect. The recipient must see the cancellation. Remove a peer and verify
   its cached old address cannot reconnect; re-pair only after removal on both sides.
6. Close the window: the tray remains. Launch from Start again: one existing
   instance opens. Test enabled/disabled startup after logging out/in. Test dark,
   light and OS-following appearance and Windows native pickers.
7. Quit from the tray, install a newer build, and verify identities/peers/history
   persist. Uninstall: launcher, shortcut, startup and protocol entries disappear,
   while durable data stays available for reinstallation.

Use Settings → Collect sanitized diagnostics on both devices. Compare shared
transfer IDs, `peer_direct` / `peer_relayed`, reconnect and resume events.
Transport scope distinguishes an encrypted direct local endpoint from a public
internet endpoint or a DERP relay. Public relay performance is not a speed guarantee.
