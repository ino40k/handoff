#!/usr/bin/env python3
"""Capture the real QML interface with documentation-only example data.

No engine, network connection, real profile, notifications or filesystem picker
is started. This script is separate from the installed application.
"""
import copy
import os
from pathlib import Path
import sys
import time

os.environ.setdefault('QT_QPA_PLATFORM', 'offscreen')
os.environ.setdefault('QT_QUICK_BACKEND', 'software')
ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'frontend'))
from PySide6.QtCore import QObject, QUrl, qInstallMessageHandler
from PySide6.QtQuick import QQuickWindow
from PySide6.QtQuickControls2 import QQuickStyle
from PySide6.QtQml import QQmlApplicationEngine
from PySide6.QtWidgets import QApplication
from handoff.bridge import Bridge
from handoff import __version__

MiB = 1 << 20

def entry(path, size=0, done=0, folder=False):
    return {'path': path, 'size': size, 'done': done, 'dir': folder}

def transfer(identifier, label, peer, direction, status, entries, done=0, speed=0, eta=0):
    return {'id': identifier, 'label': label, 'peer': peer, 'direction': direction,
            'status': status, 'entries': entries, 'size': sum(item['size'] for item in entries),
            'done': done, 'speed': speed, 'eta': eta, 'paused': False, 'destination': '', 'error': ''}

class ScreenshotBridge(Bridge):
    def __init__(self):
        QObject.__init__(self)
        self.pending = {}
        self.state = {
            'settings': {'name': 'Workstation', 'receiveDir': '/home/user/Downloads/handoff',
                         'theme': 'dark', 'autostart': True, 'configured': True},
            'ready': True, 'version': __version__, 'deviceId': '0123456789abcdef0123456789abcdef',
            'networkError': '', 'pairings': [],
            'peers': [
                {'id': 'desktop', 'name': 'Studio desktop', 'online': True, 'connection': 'direct', 'routeScope': 'internet', 'fingerprint': 'example'},
                {'id': 'laptop', 'name': 'Travel laptop', 'online': True, 'connection': 'relayed', 'routeScope': 'public relay', 'fingerprint': 'example'},
                {'id': 'home', 'name': 'Home PC', 'online': False, 'connection': '', 'routeScope': '', 'fingerprint': 'example'},
            ],
            'transfers': [
                transfer('photos', 'Travel photos · 4 items', 'Studio desktop', 'outgoing', 'active', [
                    entry('Travel photos', folder=True), entry('Travel photos/cover.jpg', 12*MiB, 12*MiB),
                    entry('Travel photos/clips', folder=True), entry('Travel photos/clips/preview.mp4', 640*MiB, 288*MiB)
                ], 300*MiB, 24*MiB, 15),
                transfer('request', 'Project files · 4 items', 'Travel laptop', 'incoming', 'pending', [
                    entry('Project files', folder=True), entry('Project files/brief.pdf', 2*MiB),
                    entry('Project files/assets', folder=True), entry('Project files/assets/reference.png', 12*MiB)
                ]),
                transfer('queued', 'notes.pdf', 'Home PC', 'outgoing', 'queued', [entry('notes.pdf', 1440*1024)]),
                transfer('complete', 'drawing.svg', 'Studio desktop', 'incoming', 'completed', [entry('drawing.svg', 88*1024, 88*1024)], 88*1024),
            ],
        }
    def call(self, *args, **kwargs):
        raise RuntimeError('Documentation capture never sends application requests')

QQuickStyle.setStyle('Fusion')
app = QApplication([])
app.setStyle('Fusion')
messages = []
qInstallMessageHandler(lambda typ, context, message: messages.append(message))
bridge = ScreenshotBridge()
engine = QQmlApplicationEngine()
engine.rootContext().setContextProperty('handoff', bridge)
engine.rootContext().setContextProperty('assetPath', QUrl.fromLocalFile(str(ROOT/'assets')+'/').toString())
engine.load(QUrl.fromLocalFile(str(ROOT/'frontend/qml/Main.qml')))
if not engine.rootObjects():
    raise RuntimeError('\n'.join(messages))
window = engine.rootObjects()[0]
window.resize(880, 620)

def pump():
    for _ in range(20):
        app.processEvents()
        time.sleep(.005)

output = ROOT/'docs/screenshots'
output.mkdir(parents=True, exist_ok=True)
def capture(name, tab, theme):
    bridge.state = copy.deepcopy(bridge.state)
    bridge.state['settings']['theme'] = theme
    bridge.changed.emit()
    window.setProperty('currentTab', tab)
    pump()
    if not window.grabWindow().save(str(output/name)):
        raise RuntimeError('Could not save '+name)

pump()
capture('peers-dark.png', 0, 'dark')
window.setProperty('expanded', {'photos': True})
capture('transfers-dark.png', 1, 'dark')
capture('settings-light.png', 2, 'light')
failures = [message for message in messages if any(part in message for part in ['ReferenceError', 'TypeError', 'Unable to assign', 'Binding loop', 'Error:'])]
if failures:
    raise RuntimeError('\n'.join(failures))
print('Captured three README screenshots using example data in '+str(output))
