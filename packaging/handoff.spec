# PyInstaller specification, used on both Linux and Windows.
from pathlib import Path
import sys
root = Path(SPECPATH).parent
backend = 'handoff-engine.exe' if sys.platform == 'win32' else 'handoff-engine'
a = Analysis([str(root / 'frontend/main.py')], pathex=[str(root / 'frontend')],
    binaries=[(str(root / 'bin' / backend), 'bin')],
    datas=[(str(root / 'frontend/qml'), 'frontend/qml'), (str(root / 'assets'), 'assets'), (str(root / 'LICENSE'), 'licenses'), (str(root / 'docs/THIRD-PARTY.md'), 'licenses')],
    hookspath=[str(root / "packaging/hooks")],
    hiddenimports=['PySide6.QtQuick', 'PySide6.QtQuickControls2', 'PySide6.QtSvg'],
    excludes=['tkinter','PySide6.QtWebEngineCore','PySide6.QtWebEngineWidgets','PySide6.Qt3DCore','PySide6.QtMultimedia'],
    noarchive=False)
pyz = PYZ(a.pure)
exe = EXE(pyz, a.scripts, [], exclude_binaries=True, name='handoff', debug=False,
    bootloader_ignore_signals=False, strip=False, upx=False, console=False,
    icon=str(root / 'assets/handoff.ico') if sys.platform == 'win32' else None)
coll = COLLECT(exe, a.binaries, a.datas, strip=False, upx=False, name='handoff')
