"""Collect the utility's QML runtime without pulling in a web browser or 3D engine."""
from pathlib import Path
from PyInstaller.utils.hooks.qt import add_qt6_dependencies, pyside6_library_info
hiddenimports, binaries, datas = add_qt6_dependencies(__file__)
qml_binaries, qml_datas = pyside6_library_info.collect_qtqml_files()
root=Path(pyside6_library_info.location['QmlImportsPath']).resolve()
def used(pair):
    relative=Path(pair[0]).resolve().relative_to(root).as_posix()
    top=relative.split('/')[0]
    if top not in {'QtCore','QtQml','QtQuick','Qt'}:return False
    if relative.startswith(('QtQuick/Scene2D/','QtQuick/Scene3D/','QtQuick/Pdf/','QtQuick/VirtualKeyboard/')):return False
    return True
binaries += [pair for pair in qml_binaries if used(pair)]
datas += [pair for pair in qml_datas if used(pair)]
