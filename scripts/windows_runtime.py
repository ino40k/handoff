"""Retain the transitive Python binding imports embedded in Shiboken modules."""
import re
from pathlib import Path

REQUIRED_BINDINGS = {'QtCore', 'QtGui', 'QtNetwork', 'QtWidgets', 'QtQuick', 'QtQml', 'QtQuickControls2', 'QtSvg'}
IMPORT_NAME = re.compile(rb'(?<![A-Za-z0-9_.])PySide6\.(Qt[A-Za-z0-9_]+)\x00')

def binding_closure(directory: Path, roots=REQUIRED_BINDINGS):
    pending = list(roots)
    retained = set()
    while pending:
        name = pending.pop()
        if name in retained:
            continue
        binary = directory / (name + '.pyd')
        if not binary.is_file():
            raise RuntimeError('Windows runtime is missing required PySide6 binding: ' + name)
        retained.add(name)
        # These are module import names, not references to individual Qt types.
        pending.extend(value.decode('ascii') for value in IMPORT_NAME.findall(binary.read_bytes()))
    return retained
