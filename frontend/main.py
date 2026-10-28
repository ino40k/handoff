#!/usr/bin/env python3
from __future__ import annotations
import hashlib
import json
import os
from pathlib import Path
import sys
import traceback
from PySide6.QtCore import QLockFile, QStandardPaths, QTimer, QUrl, Qt, qInstallMessageHandler
from PySide6.QtNetwork import QLocalServer, QLocalSocket
from PySide6.QtGui import QIcon
from PySide6.QtQml import QQmlApplicationEngine
from PySide6.QtQuickControls2 import QQuickStyle
from PySide6.QtWidgets import QApplication, QSystemTrayIcon, QMenu
from handoff.bridge import Bridge, setup_logging, redact
from handoff.platform import Notifications, windows_identity, windows_protocol

def main():
    # The native Windows Quick style adds its own button hover overlay and
    # ignores custom ComboBox backgrounds. QWidget's setStyle below does not
    # select a Qt Quick style. Choose Fusion before loading any QML controls.
    if sys.platform == "win32":
        QQuickStyle.setStyle("Fusion")
    app = QApplication(sys.argv)
    app.setApplicationName("handoff")
    app.setApplicationVersion("1.0.8")
    app.setQuitOnLastWindowClosed(False)
    app.setStyle("Fusion")
    windows_identity()
    data = Path(os.environ.get("HANDOFF_DATA_DIR") or QStandardPaths.writableLocation(QStandardPaths.AppLocalDataLocation))
    data.mkdir(parents=True, exist_ok=True, mode=0o700)
    if sys.platform != "win32":
        data.chmod(0o700)
    name = "handoff-" + hashlib.sha256(str(data.resolve()).encode()).hexdigest()[:24]
    if sys.platform != "win32":
        name = str(Path(QStandardPaths.writableLocation(QStandardPaths.TempLocation)) / (name + ".sock"))
    activation = next((arg for arg in sys.argv[1:] if arg.startswith("handoff://transfer/")), "")
    client = QLocalSocket()
    client.connectToServer(name)
    if client.waitForConnected(700):
        client.write((activation or "show").encode())
        client.waitForBytesWritten(700)
        return 0
    lock = QLockFile(str(data / "instance.lock"))
    lock.setStaleLockTime(0)
    if not lock.tryLock(500):
        # A first instance may still be opening its activation socket.
        client.connectToServer(name)
        if client.waitForConnected(3000):
            client.write((activation or "show").encode()); client.waitForBytesWritten(700)
        return 0
    QLocalServer.removeServer(name)
    server = QLocalServer()
    server.setSocketOptions(QLocalServer.SocketOption.UserAccessOption)
    if not server.listen(name):
        print("Cannot open handoff activation socket: " + server.errorString(), file=sys.stderr)
        return 1
    logger = setup_logging(data)
    root_dir = Path(getattr(sys, "_MEIPASS", Path(__file__).resolve().parent.parent))
    assets = root_dir / "assets"
    qml = root_dir / "frontend" / "qml" / "Main.qml"
    engine_binary = os.environ.get("HANDOFF_ENGINE") or str(root_dir / "bin" / ("handoff-engine.exe" if sys.platform == "win32" else "handoff-engine"))
    receive = Path(QStandardPaths.writableLocation(QStandardPaths.DownloadLocation)) / "handoff"
    receive.mkdir(parents=True, exist_ok=True)
    bridge = Bridge(data, receive, engine_binary, logger)
    sys.excepthook = lambda typ, value, tb: bridge.log("exception", "uncaught_exception", detail="".join(traceback.format_exception(typ, value, tb)))
    qInstallMessageHandler(lambda typ, context, message: bridge.log("qt", "qt_message", detail=redact(message)))
    icon = QIcon(str(assets / "handoff.svg"))
    app.setWindowIcon(icon)
    tray = QSystemTrayIcon(QIcon(str(assets / "tray.svg")), app)
    tray.setToolTip("handoff · trusted file transfers")
    engine = QQmlApplicationEngine()
    engine.rootContext().setContextProperty("handoff", bridge)
    engine.rootContext().setContextProperty("assetPath", QUrl.fromLocalFile(str(assets) + "/").toString())
    engine.load(QUrl.fromLocalFile(str(qml)))
    if not engine.rootObjects():
        bridge.close(); return 1
    window = engine.rootObjects()[0]
    bridge.window = window
    def show(transfer_id=""):
        window.show(); window.raise_(); window.requestActivate()
        if transfer_id:
            bridge.navigate.emit(transfer_id)
    def activate_payload(payload):
        if payload.startswith("handoff://transfer/"):
            transfer_id = payload.removeprefix("handoff://transfer/").strip("/")
            if len(transfer_id) == 32 and all(c in "0123456789abcdef" for c in transfer_id):
                show(transfer_id); return
        show()
    def incoming_connection():
        socket = server.nextPendingConnection()
        socket.readyRead.connect(lambda: activate_payload(bytes(socket.readAll()).decode(errors="replace")[:256]))
        socket.disconnected.connect(socket.deleteLater)
        if socket.bytesAvailable():
            activate_payload(bytes(socket.readAll()).decode(errors="replace")[:256])
    server.newConnection.connect(incoming_connection)
    tray.activated.connect(lambda reason: show() if reason in (QSystemTrayIcon.ActivationReason.Trigger, QSystemTrayIcon.ActivationReason.DoubleClick) else None)
    menu = QMenu()
    menu.addAction("Open handoff", show)
    menu.addSeparator()
    menu.addAction("Quit handoff", app.quit)
    tray.setContextMenu(menu)
    tray.show()
    bridge.notifications = Notifications(tray, show, bridge.log, app)
    if sys.platform == "win32":
        try:
            windows_protocol()
        except (OSError, __import__("subprocess").CalledProcessError) as exc:
            bridge.log("platform", "protocol_registration_failed", detail=str(exc))
    if "--tray" in sys.argv and QSystemTrayIcon.isSystemTrayAvailable():
        window.hide()
    else:
        show()
    if activation:
        QTimer.singleShot(1000, lambda: activate_payload(activation))
    if not QSystemTrayIcon.isSystemTrayAvailable():
        bridge.log("platform", "system_tray_unavailable")
    app.aboutToQuit.connect(bridge.notifications.close)
    app.aboutToQuit.connect(bridge.close)
    bridge.log("startup", "frontend_start", os.environ.get("HANDOFF_STARTUP_ID", ""))
    bridge.log("startup", "quick_style_" + QQuickStyle.name().lower())
    code = app.exec()
    server.close(); lock.unlock()
    return code

if __name__ == "__main__":
    raise SystemExit(main())
