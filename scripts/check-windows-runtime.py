#!/usr/bin/env python3
"""Check an assembled Windows runtime without requiring Windows or Python on it."""
import argparse
from pathlib import Path
from windows_runtime import binding_closure

parser = argparse.ArgumentParser()
parser.add_argument('bundle', type=Path)
args = parser.parse_args()
qt = args.bundle / 'runtime/Lib/site-packages/PySide6'
retained = binding_closure(qt)
assert (qt / 'qml/QtQuick/Controls/Fusion/qmldir').is_file(), 'Fusion QML module missing'
assert (qt / 'Qt6QuickControls2Fusion.dll').is_file(), 'Fusion DLL missing'
assert (args.bundle / 'runtime/python.exe').is_file(), 'Console runtime missing'
print('PASS: Windows binding dependency closure: ' + ', '.join(sorted(retained)))
