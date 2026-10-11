pragma Singleton
import QtQuick
QtObject {
    readonly property string preference: handoff.snapshotData.settings.theme || "system"
    readonly property bool dark: preference === "dark" || (preference === "system" && Qt.styleHints.colorScheme === Qt.Dark)
    readonly property color bg: dark ? "#202326" : "#eef0f1"
    readonly property color panel: dark ? "#282c30" : "#ffffff"
    readonly property color elevated: dark ? "#31363b" : "#e3e7e9"
    readonly property color line: dark ? "#42484e" : "#c5cbd0"
    readonly property color text: dark ? "#e1e5e8" : "#242a2f"
    readonly property color muted: dark ? "#9da7ae" : "#626e76"
    readonly property color accent: dark ? "#76c6bc" : "#257c71"
    readonly property color selection: dark ? "#314843" : "#dceee9"
    readonly property color bad: dark ? "#e89a91" : "#a9352b"
    readonly property color warning: dark ? "#d9bf87" : "#866116"
}
