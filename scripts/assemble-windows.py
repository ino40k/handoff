#!/usr/bin/env python3
"""Assemble pinned Windows runtime components; executable on Fedora or Windows."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import urllib.request
import zipfile
ROOT=Path(__file__).resolve().parent.parent
BUILD=ROOT/'.build'
VERSION='3.13.16'
PYTHON_SHA256='97dae5274cc54867065e8d5a3226e48c35017ed332a0fdb0e27d5b5821961297'
GO=os.environ.get('GO','go')
WHEELS=BUILD/'windows-wheels'
BUNDLE=ROOT/'dist/handoff-windows'
WHEELS.mkdir(parents=True,exist_ok=True)
archive=BUILD/f'python-{VERSION}-embed-amd64.zip'
if not archive.exists():
    urllib.request.urlretrieve(f'https://www.python.org/ftp/python/{VERSION}/python-{VERSION}-embed-amd64.zip',archive)
if hashlib.sha256(archive.read_bytes()).hexdigest()!=PYTHON_SHA256:
    raise SystemExit('Embedded Python checksum does not match the official release')
subprocess.run([sys.executable,'-m','pip','download','--no-index','--find-links',str(WHEELS),'--only-binary=:all:','--platform','win_amd64','--python-version','313','--implementation','cp','--abi','cp313','--dest',str(WHEELS),'PySide6==6.11.2'],check=True)
if BUNDLE.exists():shutil.rmtree(BUNDLE)
(BUNDLE/'runtime/Lib/site-packages').mkdir(parents=True)
with zipfile.ZipFile(archive) as z:z.extractall(BUNDLE/'runtime')
(BUNDLE/'runtime/python313._pth').write_text('python313.zip\n.\nLib/site-packages\n../frontend\nimport site\n',encoding='utf-8')
runtime_lock=json.loads((ROOT/'packaging/windows-runtime-lock.json').read_text())
for filename,expected in runtime_lock.items():
    if not filename.endswith('.whl'):continue
    wheel=WHEELS/filename
    if not wheel.exists() or hashlib.sha256(wheel.read_bytes()).hexdigest()!=expected:
        raise SystemExit('Windows wheel checksum mismatch: '+filename)
    with zipfile.ZipFile(wheel) as z:z.extractall(BUNDLE/'runtime/Lib/site-packages')
# Remove unused development tools, bindings and Qt modules; keep the complete
# QtQuick/Qml tree and all its styles to avoid assuming internal dependencies.
qt=BUNDLE/'runtime/Lib/site-packages/PySide6'
keep_bindings={'QtCore','QtGui','QtNetwork','QtWidgets','QtQuick','QtQml','QtQuickControls2','QtSvg'}
for binding in qt.glob('Qt*.pyd'):
    if binding.stem not in keep_bindings:binding.unlink()
for sub in ['include','typesystems','glue','scripts','qml/Qt3D','qml/QtQuick3D','qml/QtWebEngine','qml/QtCharts','qml/QtGraphs','qml/QtMultimedia','qml/QtLocation','qml/QtPositioning','qml/QtSensors','qml/QtWebView','qml/QtWebSockets','qml/QtDataVisualization']:
    shutil.rmtree(qt/sub,ignore_errors=True)
for library in qt.glob('Qt6*.dll'):
    if not library.name.startswith(('Qt6Core.','Qt6Gui.','Qt6Network.','Qt6Widgets.','Qt6Quick','Qt6Qml','Qt6Svg','Qt6OpenGL','Qt6DBus','Qt6Labs','Qt6Sql.','Qt6Concurrent.','Qt6PrintSupport.','Qt6Lottie','Qt6Pdf','Qt6VirtualKeyboard','Qt6StateMachine')):
        library.unlink()
for sub in ['assistant.exe','designer.exe','linguist.exe','lrelease.exe','lupdate.exe','qmlformat.exe','qmllint.exe','qmltyperegistrar.exe','balsam.exe','balsamui.exe']:
    (qt/sub).unlink(missing_ok=True)
(BUNDLE/'bin').mkdir()
env=dict(os.environ,GOOS='windows',GOARCH='amd64',CGO_ENABLED='0')
for target,output in [('handoff-engine','bin/handoff-engine.exe'),('handoff-launcher','handoff.exe')]:
    subprocess.run([GO,'build','-trimpath','-ldflags=-s -w -H windowsgui','-o',str(BUNDLE/output),'./cmd/'+target],cwd=ROOT/'backend',env=env,check=True)
shutil.copytree(ROOT/'frontend',BUNDLE/'frontend',ignore=shutil.ignore_patterns('__pycache__'))
shutil.copytree(ROOT/'assets',BUNDLE/'assets')
(BUNDLE/'licenses').mkdir()
shutil.copyfile(ROOT/'LICENSE',BUNDLE/'licenses/LICENSE-handoff')
shutil.copyfile(ROOT/'docs/THIRD-PARTY.md',BUNDLE/'licenses/THIRD-PARTY.md')
shutil.copyfile(BUNDLE/'runtime/LICENSE.txt',BUNDLE/'licenses/LICENSE-Python.txt')
subprocess.run([sys.executable,str(ROOT/'scripts/collect-go-licenses.py'),str(BUNDLE/'licenses/go')],check=True)
(BUNDLE/'README.txt').write_text('handoff 1.0.1 — Windows 11 x64\n\nExtract this entire folder, then run handoff.exe. No Python, Qt, Go or Tailscale installation is required.\nUse the tray menu to quit. Close handoff before replacing this folder with a newer version.\nThe device identity, trusted peers and transfer state are stored separately in %LOCALAPPDATA%/handoff.\nWindows runtime integration requires testing on Windows 11.\nIf startup fails: open a terminal here and run runtime\\python.exe frontend\\main.py.\n',encoding='utf-8')
checksums={p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in WHEELS.glob('*.whl')}
checksums[archive.name]=PYTHON_SHA256
(BUNDLE/'licenses/runtime-checksums.json').write_text(json.dumps(checksums,indent=2))
archive_out=ROOT/'dist/handoff-1.0.1-windows-x64-portable.zip'
with zipfile.ZipFile(archive_out,'w',zipfile.ZIP_DEFLATED,compresslevel=6) as z:
    for f in BUNDLE.rglob('*'):
        if f.is_file():z.write(f,f.relative_to(BUNDLE.parent))
print(archive_out)
