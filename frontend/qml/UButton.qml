import QtQuick
import QtQuick.Controls
Button {
    id: control
    property bool primary: false
    implicitHeight: 27
    implicitWidth: Math.max(45, contentItem.implicitWidth + 18)
    padding: 8
    topPadding: 3
    bottomPadding: 3
    font.pixelSize: 12
    contentItem: Text { text: control.text; font: control.font; color: control.enabled ? (control.primary ? Theme.accent : Theme.text) : Theme.muted; horizontalAlignment: Text.AlignHCenter; verticalAlignment: Text.AlignVCenter; opacity: control.enabled ? 1 : 0.55 }
    background: Rectangle { radius: 3; color: control.down ? Theme.selection : control.hovered ? Theme.elevated : Theme.panel; border.color: control.primary ? Theme.accent : Theme.line; opacity: control.enabled ? 1 : 0.6 }
}
