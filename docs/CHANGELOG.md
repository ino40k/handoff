# Changes

## 1.0.4

Give navigation a separate inset strip below the device header with restrained
selected and hover states. Correct the custom tabs' implicit height, which Qt
used to shift them upward over the logo and title. Reserve stable header and
navigation heights so resizing cannot cause overlap. The request badge and
keyboard navigation remain available in both themes.

## 1.0.3

Show a compact circled count of incoming requests awaiting approval beside the
Transfers tab. It follows persisted transfer state, stays visible when viewing
other tabs or after reopening the app, and clears only as requests are accepted,
declined or canceled. Outgoing and already-approved transfers do not contribute.
Both themes use the same layout; counts above 99 display as 99+ with the full
count available to accessibility tools.

## 1.0.2

Fix drag-and-drop file and folder sends being rejected as non-local files.
QML supplies QUrl objects; the frontend now preserves them instead of parsing
their Python representation. Spaces, Unicode and encoded filename characters
are preserved. Mixed local/remote drops are rejected as a whole. Drop decisions
are logged without recording paths. Identity, peers, protocol and saved
transfers remain compatible with 1.0.1.

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
