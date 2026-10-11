"""OS integration, deliberately separate from the networking engine."""
from __future__ import annotations
import ctypes
import json
import os
from pathlib import Path
import subprocess
import sys
from PySide6.QtCore import QObject, Slot, Signal, QStandardPaths

APP_ID = "handoff.desktop"

def launch_command() -> list[str]:
    if os.environ.get("HANDOFF_LAUNCHER"):
        return [os.environ["HANDOFF_LAUNCHER"]]
    if getattr(sys, "frozen", False):
        return [sys.executable]
    return [sys.executable, str(Path(__file__).resolve().parents[1] / "main.py")]

def autostart(enabled: bool) -> None:
    command = launch_command() + ["--tray"]
    if sys.platform == "win32":
        import winreg
        with winreg.CreateKey(winreg.HKEY_CURRENT_USER, r"Software\Microsoft\Windows\CurrentVersion\Run") as key:
            if enabled:
                winreg.SetValueEx(key, "handoff", 0, winreg.REG_SZ, subprocess.list2cmdline(command))
            else:
                try:
                    winreg.DeleteValue(key, "handoff")
                except FileNotFoundError:
                    pass
    else:
        directory = Path(QStandardPaths.writableLocation(QStandardPaths.ConfigLocation)) / "autostart"
        target = directory / "handoff.desktop"
        if enabled:
            directory.mkdir(parents=True, exist_ok=True)
            # Desktop Entry Exec quoting is not shell quoting. Escape reserved
            # characters and percent field codes inside double quotes.
            def quote(arg: str) -> str:
                for ch in ("\\", '"', "`", "$"):
                    arg = arg.replace(ch, "\\" + ch)
                return '"' + arg.replace("%", "%%") + '"'
            temporary = target.with_suffix(".tmp")
            temporary.write_text("[Desktop Entry]\nType=Application\nName=handoff\nExec=" + " ".join(map(quote, command)) + "\nTryExec="+command[0]+"\nIcon=handoff\nTerminal=false\nX-GNOME-Autostart-enabled=true\n", encoding="utf-8")
            temporary.replace(target)
        else:
            target.unlink(missing_ok=True)

def windows_identity() -> None:
    if sys.platform == "win32":
        ctypes.windll.shell32.SetCurrentProcessExplicitAppUserModelID(APP_ID)
        ctypes.windll.kernel32.CreateMutexW.restype = ctypes.c_void_p
        global _running_mutex
        _running_mutex = ctypes.windll.kernel32.CreateMutexW(None, False, r"Local\handoff-is-running")

def windows_protocol(enabled: bool = True) -> None:
    """Per-user protocol registration used by actionable Windows toasts."""
    if sys.platform != "win32":
        return
    import winreg
    launcher = os.environ.get("HANDOFF_LAUNCHER")
    if launcher:
        subprocess.run([launcher, "--register-shortcut"], check=True, creationflags=subprocess.CREATE_NO_WINDOW)
    with winreg.CreateKey(winreg.HKEY_CURRENT_USER, r"Software\Classes\handoff") as key:
        winreg.SetValueEx(key, "", 0, winreg.REG_SZ, "URL:handoff transfer request")
        winreg.SetValueEx(key, "URL Protocol", 0, winreg.REG_SZ, "")
    with winreg.CreateKey(winreg.HKEY_CURRENT_USER, r"Software\Classes\handoff\shell\open\command") as key:
        winreg.SetValueEx(key, "", 0, winreg.REG_SZ, subprocess.list2cmdline(launch_command()) + ' "%1"')

class Notifications(QObject):
    requestOpen = Signal(str)
    fallback = Signal(str, str)
    def __init__(self, tray, open_transfer, log, parent=None):
        super().__init__(parent)
        self.tray, self.open_transfer, self.log = tray, open_transfer, log
        self.ids = {}
        self.last = ""
        self.loop = None
        self.requestOpen.connect(self.open_transfer)
        self.fallback.connect(lambda title, body: self.tray.showMessage(title, body, self.tray.MessageIcon.Information, 10000))
        self.tray.messageClicked.connect(lambda: self.open_transfer(self.last))
        if sys.platform == "win32":
            from concurrent.futures import ThreadPoolExecutor
            self.executor = ThreadPoolExecutor(max_workers=1, thread_name_prefix="handoff-toasts")
        if sys.platform != "win32":
            import asyncio
            import threading
            def worker():
                from dbus_next.aio import MessageBus
                from dbus_next import Message, MessageType
                self.loop = asyncio.new_event_loop()
                asyncio.set_event_loop(self.loop)
                async def connect():
                    self.bus = await MessageBus().connect()
                    def message(msg):
                        if msg.message_type == MessageType.SIGNAL and msg.interface == "org.freedesktop.Notifications" and msg.member == "ActionInvoked":
                            notification_id, action = msg.body
                            if notification_id in self.ids and action in ("default", "open"):
                                self.requestOpen.emit(self.ids[notification_id])
                    self.bus.add_message_handler(message)
                    await self.bus.call(Message(destination="org.freedesktop.DBus", path="/org/freedesktop/DBus", interface="org.freedesktop.DBus", member="AddMatch", signature="s", body=["type='signal',interface='org.freedesktop.Notifications',member='ActionInvoked'"]))
                try:
                    self.loop.run_until_complete(connect())
                    self.loop.run_forever()
                    self.bus.disconnect()
                except Exception as exc:
                    self.log("notification", "dbus_connection_failed", detail=str(exc))
                finally:
                    self.loop.close()
                    self.loop = None
            threading.Thread(target=worker, daemon=True, name="handoff-notifications").start()

    def close(self):
        if sys.platform == "win32":
            self.executor.shutdown(wait=False, cancel_futures=True)
        if self.loop and self.loop.is_running():
            self.loop.call_soon_threadsafe(self.loop.stop)

    def incoming(self, transfer):
        self.last = transfer["id"]
        title = "Incoming transfer · " + transfer["peer"]
        body = transfer["label"] + "\nApproval required in handoff."
        if sys.platform == "win32":
            # PowerShell's native WinRT bridge needs no additional Python runtime.
            # User text travels through stdin JSON, never through command syntax.
            script = r'''
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
[Windows.UI.Notifications.ToastNotification, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] > $null
$d = [Console]::In.ReadToEnd() | ConvertFrom-Json
$xml = New-Object Windows.Data.Xml.Dom.XmlDocument
$xml.LoadXml('<toast activationType="protocol" launch="handoff://transfer/' + $d.id + '"><visual><binding template="ToastGeneric"><text></text><text></text></binding></visual></toast>')
$nodes = $xml.GetElementsByTagName('text'); $nodes.Item(0).InnerText = $d.title; $nodes.Item(1).InnerText = $d.body
$toast = [Windows.UI.Notifications.ToastNotification]::new($xml)
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('handoff.desktop').Show($toast)
'''
            def send_windows():
                try:
                    process = subprocess.Popen(["powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script], stdin=subprocess.PIPE, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, creationflags=subprocess.CREATE_NO_WINDOW)
                    process.communicate(json.dumps({"id": transfer["id"], "title": title, "body": body}).encode(), timeout=10)
                    if process.returncode == 0:
                        return
                    self.log("notification", "windows_toast_failed")
                except (OSError, subprocess.TimeoutExpired):
                    self.log("notification", "windows_toast_failed")
                self.fallback.emit(title, body)
            self.executor.submit(send_windows)
            return
        elif self.loop and self.loop.is_running():
            import asyncio
            import html
            async def send():
                from dbus_next import Message, MessageType
                try:
                    reply = await self.bus.call(Message(destination="org.freedesktop.Notifications", path="/org/freedesktop/Notifications", interface="org.freedesktop.Notifications", member="Notify", signature="susssasa{sv}i", body=["handoff", 0, "handoff", title, html.escape(body), ["default", "Open request"], {}, -1]))
                    if reply.message_type == MessageType.ERROR:
                        raise RuntimeError(reply.error_name)
                    self.ids[int(reply.body[0])] = transfer["id"]
                except Exception as exc:
                    self.log("notification", "dbus_notification_failed", detail=str(exc))
                    self.fallback.emit(title, body)
            asyncio.run_coroutine_threadsafe(send(), self.loop)
            return
        self.tray.showMessage(title, body, self.tray.MessageIcon.Information, 10000)
