from __future__ import annotations
import json
import logging
from logging.handlers import RotatingFileHandler
import os
from pathlib import Path
import re
import sys
import time
import traceback
import uuid
from PySide6.QtCore import QObject, Property, Signal, Slot, QProcess, QProcessEnvironment, QTimer, QUrl, Qt, QStandardPaths
from PySide6.QtGui import QDesktopServices, QGuiApplication
from PySide6.QtWidgets import QFileDialog, QDialog
from . import platform

SECRET = re.compile(r"tc[A-Za-z0-9_-]{24,}|(?:privkey:|nodekey:|psk:)[A-Za-z0-9]+|handoff1\.[A-Za-z0-9_-]+")
def redact(value):
    return SECRET.sub("[redacted]", str(value))

def setup_logging(data_dir):
    directory = Path(data_dir) / "logs"
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    logger = logging.getLogger("handoff")
    logger.setLevel(logging.INFO)
    handler = RotatingFileHandler(directory / "frontend.jsonl", maxBytes=2 << 20, backupCount=5, encoding="utf-8")
    handler.setFormatter(logging.Formatter("%(message)s"))
    logger.addHandler(handler)
    return logger

class Bridge(QObject):
    changed = Signal()
    error = Signal(str)
    notice = Signal(str)
    invitationReady = Signal(str)
    navigate = Signal(str)
    directoryChosen = Signal(str, str)
    busyChanged = Signal()
    def __init__(self, data_dir, receive_dir, engine_path, logger, parent=None):
        super().__init__(parent)
        self.data_dir, self.receive_dir, self.engine_path, self.logger = str(data_dir), str(receive_dir), str(engine_path), logger
        self.state = {"settings": {"name": "", "receiveDir": str(receive_dir), "theme": "system", "autostart": False, "configured": True}, "peers": [], "transfers": [], "pairings": [], "ready": False, "networkError": "Starting engine…"}
        self.pending = {}
        self.buffer = bytearray()
        self.notifications = None
        self.seen_requests = set()
        self.stopping = False
        self.window = None
        self._picker = None
        self._picker_pending = False
        self.startup_restored = False
        self.retry_delay = 1000
        self.process = QProcess(self)
        self.process.readyReadStandardOutput.connect(self.read)
        self.process.readyReadStandardError.connect(self.read_error)
        self.process.finished.connect(self.finished)
        self.process.errorOccurred.connect(self.process_error)
        self.process.started.connect(lambda: self.call("snapshot"))
        self.poll = QTimer(self)
        self.poll.setInterval(1000)
        self.poll.timeout.connect(self.snapshot)
        self.poll.start()
        self.start()

    def log(self, category, event, correlation="", detail=""):
        record = {"time": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "component": "frontend", "level": "error" if detail else "info", "category": category, "event": event, "id": correlation}
        if detail:
            record["error"] = redact(detail)
        self.logger.info(json.dumps(record, ensure_ascii=False))

    @Property("QVariantMap", notify=changed)
    def snapshotData(self):
        return self.state

    @Property(bool, notify=busyChanged)
    def busy(self):
        return any(v[0] == "send" for v in self.pending.values())

    def start(self):
        if self.stopping:
            return
        self.buffer.clear()
        env = QProcessEnvironment.systemEnvironment()
        env.insert("HANDOFF_DATA_DIR", self.data_dir)
        env.insert("HANDOFF_RECEIVE_DIR", self.receive_dir)
        # Never allow upstream debug knobs to print full tailcat credentials.
        env.remove("TS_DEBUG_ADDR")
        self.process.setProcessEnvironment(env)
        self.process.setProgram(self.engine_path)
        self.process.start()
        self.log("startup", "engine_launch")

    def process_error(self, code):
        self.log("exception", "engine_process_error", detail=self.process.errorString())
        if code == QProcess.ProcessError.FailedToStart:
            self.state["networkError"] = "Cannot start engine: " + self.process.errorString()
            self.changed.emit()
            self.error.emit(self.state["networkError"])

    def finished(self, code, status):
        self.log("shutdown", "engine_exited", detail=f"code={code} status={status}")
        self.pending.clear()
        self.busyChanged.emit()
        if not self.stopping:
            self.state["ready"] = False
            self.state["networkError"] = "Engine restarting; saved requests and progress are retained"
            self.changed.emit()
            QTimer.singleShot(self.retry_delay, self.start)
            self.retry_delay = min(30000, self.retry_delay * 2)

    def read_error(self):
        self.log("backend", "engine_stderr", detail=bytes(self.process.readAllStandardError()).decode(errors="replace"))

    def call(self, method, params=None, callback=None, failure=None):
        if self.process.state() != QProcess.ProcessState.Running:
            return
        correlation = uuid.uuid4().hex
        self.pending[correlation] = (method, callback, failure)
        message = {"id": correlation, "method": method, "params": params or {}}
        self.process.write((json.dumps(message) + "\n").encode())
        if method != "snapshot":
            self.log("ipc", method, correlation)
        self.busyChanged.emit()

    @Slot()
    def snapshot(self):
        if not any(v[0] == "snapshot" for v in self.pending.values()):
            self.call("snapshot")

    def read(self):
        self.buffer.extend(bytes(self.process.readAllStandardOutput()))
        while b"\n" in self.buffer:
            line, _, remaining = self.buffer.partition(b"\n")
            self.buffer = bytearray(remaining)
            try:
                response = json.loads(line)
                method, callback, failure = self.pending.pop(response.get("id"), ("", None, None))
                self.busyChanged.emit()
                if response.get("error"):
                    self.log("ipc", method + "_failed", response.get("id", ""), response["error"])
                    if failure:
                        failure()
                    self.error.emit(response["error"])
                    continue
                result = response.get("result")
                if method == "snapshot":
                    self.state = result
                    if not self.startup_restored:
                        self.startup_restored = True
                        if result["settings"]["configured"] and result["settings"]["autostart"]:
                            try:
                                platform.autostart(True)
                            except OSError as exc:
                                self.log("settings", "startup_restore_failed", detail=str(exc))
                                self.error.emit("Automatic startup registration could not be restored; see logs")
                    self.retry_delay = 1000
                    self.changed.emit()
                    if self.notifications:
                        for transfer in result["transfers"]:
                            if transfer["direction"] == "incoming" and transfer["status"] == "pending" and transfer["id"] not in self.seen_requests:
                                self.seen_requests.add(transfer["id"])
                                self.notifications.incoming(transfer)
                elif method == "invite":
                    self.invitationReady.emit(result["code"])
                elif method == "diagnostics":
                    QDesktopServices.openUrl(QUrl.fromLocalFile(str(Path(result["path"]).parent)))
                    self.notice.emit("Sanitized diagnostics saved: " + result["path"])
                if callback:
                    callback(result)
                if method != "snapshot":
                    self.snapshot()
            except Exception:
                self.log("exception", "ipc_response_exception", detail=traceback.format_exc())
                self.error.emit("Could not process the engine response; see logs")

    @Slot()
    def invite(self):
        self.call("invite")

    @Slot(str)
    def join(self, code):
        self.call("join", {"code": code})

    @Slot(str, bool)
    def pairDecision(self, pairing_id, approve):
        self.call("pairDecision", {"id": pairing_id, "approve": approve})

    @Slot(str, str)
    def renamePeer(self, peer_id, name):
        self.call("renamePeer", {"id": peer_id, "name": name})

    @Slot(str)
    def removePeer(self, peer_id):
        self.call("removePeer", {"id": peer_id})

    def open_picker(self, title, folder, current, callback):
        """Return to QML before opening a native dialog; never run exec().

        A nested dialog event loop can process a snapshot that destroys the
        ListView delegate whose clicked handler is still on the stack. Keep
        selection context in Python values and deliver it from finished instead.
        """
        if self.stopping or self._picker_pending:
            return
        self._picker_pending = True
        correlation = uuid.uuid4().hex
        self.log("dialog", "picker_requested", correlation)

        def show():
            if self.stopping:
                self._picker_pending = False
                return
            dialog = None
            try:
                dialog = QFileDialog(None, title)
                dialog.setAcceptMode(QFileDialog.AcceptMode.AcceptOpen)
                dialog.setFileMode(QFileDialog.FileMode.Directory if folder else QFileDialog.FileMode.ExistingFiles)
                if folder:
                    dialog.setOption(QFileDialog.Option.ShowDirsOnly, True)
                if current:
                    dialog.setDirectory(current)
                dialog.setWindowModality(Qt.WindowModality.ApplicationModal)
                if self.window is not None:
                    dialog.winId()
                    dialog.windowHandle().setTransientParent(self.window)
                self._picker = dialog

                def finished(result):
                    try:
                        paths = list(dialog.selectedFiles()) if result == QDialog.DialogCode.Accepted else []
                        self.log("dialog", "picker_accepted" if paths else "picker_canceled", correlation)
                        self._picker = None
                        self._picker_pending = False
                        if paths and not self.stopping:
                            callback(paths)
                    except Exception:
                        self.log("exception", "picker_callback_failed", correlation, traceback.format_exc())
                        self.error.emit("Could not use the selected path; see logs")
                    finally:
                        self._picker = None
                        self._picker_pending = False
                        dialog.deleteLater()

                dialog.finished.connect(finished)
                dialog.open()
                self.log("dialog", "picker_opened", correlation)
            except Exception:
                self._picker = None
                self._picker_pending = False
                if dialog is not None:
                    dialog.deleteLater()
                self.log("exception", "picker_open_failed", correlation, traceback.format_exc())
                self.error.emit("Could not open the file picker; see logs")

        QTimer.singleShot(0, show)

    @Slot("QVariantList", bool)
    def pickSend(self, peers, folder=False):
        peer_ids = [str(peer) for peer in peers]
        if not peer_ids:
            self.error.emit("Select a trusted peer first")
            return
        current = QStandardPaths.writableLocation(QStandardPaths.StandardLocation.HomeLocation)
        self.open_picker("Send folder" if folder else "Send files", folder, current,
                         lambda paths: self.call("send", {"paths": paths, "peers": peer_ids}))

    @Slot(str, "QVariantList")
    def dropSend(self, peer_id, urls):
        paths = []
        for url in urls:
            # QML DropEvent.urls reaches PySide as QUrl objects. str(QUrl)
            # is a Python representation, not the URL's encoded text.
            qurl = QUrl(url) if isinstance(url, QUrl) else QUrl(str(url))
            if not qurl.isValid() or not qurl.isLocalFile() or not qurl.toLocalFile():
                self.log("drag_drop", "drop_rejected", detail="non_local_or_invalid_url")
                self.error.emit("Drop local files or folders")
                return
            paths.append(qurl.toLocalFile())
        if paths:
            self.log("drag_drop", "drop_send_requested")
            self.call("send", {"paths": paths, "peers": [peer_id]})
        else:
            self.log("drag_drop", "drop_rejected", detail="empty_drop")
            self.error.emit("Drop local files or folders")

    @Slot(str, str)
    def transfer(self, transfer_id, action):
        self.call("transfer", {"id": transfer_id, "action": action})

    @Slot(str, bool)
    def accept(self, transfer_id, custom):
        if custom:
            self.open_picker("Receive this transfer into…", True, self.state["settings"]["receiveDir"],
                             lambda paths: self.call("transfer", {"id": transfer_id, "action": "accept", "destination": paths[0]}))
        else:
            self.call("transfer", {"id": transfer_id, "action": "accept", "destination": ""})

    @Slot(str, int)
    def reorder(self, transfer_id, delta):
        self.call("reorder", {"id": transfer_id, "delta": delta})

    @Slot()
    def clearHistory(self):
        self.call("clearHistory")

    @Slot(str, str)
    def pickDirectory(self, current, target):
        if target not in ("settings", "wizard"):
            self.error.emit("Unknown receiving directory selector")
            return
        self.open_picker("Receiving directory", True, current,
                         lambda paths: self.directoryChosen.emit(target, paths[0]))

    @Slot(str, str, str, bool, bool)
    def saveSettings(self, name, directory, theme, startup, configured):
        if self.process.state() != QProcess.ProcessState.Running:
            self.error.emit("The engine is unavailable; preferences were not saved")
            return
        previous = self.state["settings"]["autostart"]
        try:
            platform.autostart(startup)
        except OSError as exc:
            self.log("settings", "autostart_failed", detail=str(exc))
            self.error.emit("Could not configure automatic startup: " + str(exc))
            return
        def rollback():
            try:
                platform.autostart(previous)
            except OSError as exc:
                self.log("settings", "autostart_rollback_failed", detail=str(exc))
        self.call("settings", {"settings": {"name": name, "receiveDir": directory, "theme": theme, "autostart": startup, "configured": configured}}, lambda _: self.notice.emit("Settings saved"), rollback)

    @Slot()
    def openLogs(self):
        QDesktopServices.openUrl(QUrl.fromLocalFile(str(Path(self.data_dir) / "logs")))

    @Slot()
    def diagnostics(self):
        self.call("diagnostics")

    @Slot(str)
    def copy(self, text):
        QGuiApplication.clipboard().setText(text)

    @Slot(str)
    def openFolder(self, path):
        QDesktopServices.openUrl(QUrl.fromLocalFile(path))

    def close(self):
        self.stopping = True
        self.poll.stop()
        if self._picker is not None:
            self._picker.reject()
        self.log("shutdown", "frontend_quit")
        if self.process.state() == QProcess.ProcessState.Running:
            self.process.write(b'{"id":"shutdown","method":"quit"}\n')
            self.process.closeWriteChannel()
            if not self.process.waitForFinished(10000):
                self.log("shutdown", "engine_forced_stop")
                self.process.kill()
                self.process.waitForFinished(3000)
