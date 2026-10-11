import QtQuick
import QtQuick.Controls
TextField {
    implicitHeight: 28
    font.pixelSize: 12
    color: Theme.text
    placeholderTextColor: Theme.muted
    selectionColor: Theme.selection
    selectedTextColor: Theme.text
    padding: 7
    background: Rectangle { color: Theme.panel; radius: 2; border.color: parent.activeFocus ? Theme.accent : Theme.line }
}
