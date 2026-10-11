# Changes

## 1.0.1

Fix the frontend crash when Send opens a file picker and a presence update
replaces the clicked peer's QML row. The old picker ran QDialog.exec() inside
the row's active signal handler, and Qt aborted when that row was deleted.

All file/folder selectors now return immediately, open natively on the next
event-loop turn, and deliver results through finished callbacks. This also
covers folder sends, custom receive destinations, settings, and first-launch
setup. Selection context survives peer-list changes; cancellation makes no
transfer or approval request. New dialog lifecycle logs correlate selection
requests, opening, cancellation, and acceptance without recording paths.

Regression coverage reproduces the old abort with changing peer latency and
checks the repaired click path, selection, cancellation, recipient preservation,
and per-transfer approval. Device identity, peers, protocol and durable state
are compatible with 1.0.0. The Fedora RPM is upgraded in place.
