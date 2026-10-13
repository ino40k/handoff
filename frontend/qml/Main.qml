import QtQuick
import QtQuick.Controls
import QtQuick.Layouts
import QtQuick.Window

ApplicationWindow {
    id: win
    width: 790; height: 590; minimumWidth: 650; minimumHeight: 470
    visible: true; title: "handoff"
    color: Theme.bg
    font.family: Qt.platform.os === "windows" ? "Segoe UI" : "Noto Sans"
    font.pixelSize: 12
    palette.window: Theme.bg; palette.windowText: Theme.text; palette.base: Theme.panel
    palette.text: Theme.text; palette.button: Theme.elevated; palette.buttonText: Theme.text
    palette.highlight: Theme.selection; palette.highlightedText: Theme.text
    property var appState: handoff.snapshotData
    readonly property int pendingIncomingCount: {
        var count = 0
        for (var transfer of (appState.transfers || []))
            if (transfer.direction === "incoming" && transfer.status === "pending") count++
        return count
    }
    property var selectedPeers: []
    property string filter: "all"
    property var expanded: ({})
    property string inspectId: ""
    property bool awaitingInspection: false
    property var inspected: null
    property string renameId: ""
    property string removeId: ""
    property string banner: ""
    property bool bannerBad: false
    property bool wizardOpened: false
    property bool settingsDirty: false
    property alias currentTab: tabs.currentIndex
    onClosing: function(close) { close.accepted = false; win.hide() }
    function bytes(n) { if(n < 1024) return n + " B"; var units = ["KiB", "MiB", "GiB", "TiB"]; var k = 0; n /= 1024; while(n >= 1024 && k < 3) { n /= 1024; k++ } return n.toFixed(n < 10 ? 1 : 0) + " " + units[k] }
    function duration(n) { if(!n || n < 1) return "—"; if(n < 60) return Math.ceil(n) + "s"; if(n < 3600) return Math.ceil(n/60) + "m"; return (n/3600).toFixed(1) + "h" }
    function terminal(s) { return ["completed", "failed", "declined", "canceled"].indexOf(s) >= 0 }
    function statusName(s) { return {remote_paused:"Peer paused",pending:"Awaiting approval",active:"Transferring",queued:"Queued",paused:"Paused",completed:"Completed",failed:"Failed",declined:"Declined",canceled:"Canceled"}[s] || s }
    function togglePeer(id) { var next = selectedPeers.slice(); var i = next.indexOf(id); if(i < 0) next.push(id); else next.splice(i,1); selectedPeers = next }
    function matches(t) { if(filter === "all") return true; if(filter === "failed") return ["failed","declined","canceled"].indexOf(t.status)>=0; if(filter === "queued") return ["queued","paused","remote_paused"].indexOf(t.status)>=0; return filter === t.status }
    function openTransfer(id) { tabs.currentIndex = 1; inspectId = id; awaitingInspection=true;for(var t of appState.transfers) if(t.id === id) { inspected = t; awaitingInspection=false;requestDialog.open(); return } }
    function refreshSettings() { deviceName.text=appState.settings.name; receivingDir.text=appState.settings.receiveDir; themeBox.currentIndex=["system","dark","light"].indexOf(appState.settings.theme); startup.checked=appState.settings.autostart; settingsDirty=false }
    Connections {
        target: handoff
        function onChanged() {
            if(!appState.settings.configured && !wizardOpened) { wizardOpened=true; wizardName.text=appState.settings.name; wizardDir.text=appState.settings.receiveDir; wizard.open() }
            if(inspectId) for(var t of appState.transfers) if(t.id===inspectId) {inspected=t;if(awaitingInspection){awaitingInspection=false;requestDialog.open()}}
            if(!settingsDirty) refreshSettings()
        }
        function onError(message) { banner=message; bannerBad=true; bannerTimer.restart() }
        function onNotice(message) { banner=message; bannerBad=false; bannerTimer.restart() }
        function onDirectoryChosen(target, path) {
            if(target === "settings") {receivingDir.text=path; settingsDirty=true}
            else if(target === "wizard") wizardDir.text=path
        }
        function onInvitationReady(code) { invitation.text=code; invitationDialog.open() }
        function onNavigate(id) { openTransfer(id) }
    }
    Timer { id: bannerTimer; interval: 12000; onTriggered: banner="" }
    ColumnLayout {
        anchors.fill: parent; spacing: 0
        Rectangle {
            objectName: "appHeader"
            Layout.fillWidth: true; Layout.minimumHeight: 44; Layout.maximumHeight: 44; implicitHeight: 44; color: Theme.panel
            RowLayout {
                anchors.fill: parent; anchors.leftMargin: 13; anchors.rightMargin: 13; spacing: 9
                Image { source: assetPath + "handoff.svg"; sourceSize.width: 25; sourceSize.height: 25; Layout.preferredWidth: 25; Layout.preferredHeight: 25 }
                Text { textFormat: Text.PlainText; text: "handoff"; color: Theme.text; font.pixelSize: 16; font.weight: Font.DemiBold }
                Text { textFormat: Text.PlainText; text: " / " + (appState.settings.name || "Starting…"); color: Theme.muted; font.pixelSize: 12; elide: Text.ElideRight; Layout.fillWidth: true }
            }
            Rectangle { anchors.bottom: parent.bottom; width: parent.width; height: 1; color: Theme.line }
        }
        Rectangle {
            objectName: "appNavigation"
            Layout.fillWidth: true; Layout.minimumHeight: 38; Layout.maximumHeight: 38; implicitHeight: 38
            color: Theme.bg
            Rectangle { anchors.bottom: parent.bottom; width: parent.width; height: 1; color: Theme.line }
            TabBar {
                id: tabs; anchors.left: parent.left; anchors.right: parent.right; anchors.top: parent.top
                anchors.leftMargin: 12; anchors.rightMargin: 12; anchors.topMargin: 4
                height: 32; implicitHeight: 32; spacing: 2; padding: 0
                background: Item { }
                Repeater { model: ["Peers", "Transfers", "Settings"]
                    TabButton {
                        id: tabButton
                        required property string modelData
                        required property int index
                        objectName: "navigationTab" + index
                        text: modelData; width: 100 + (requestBadge.visible ? requestBadge.width + 6 : 0)
                        implicitHeight: 32; height: 32; padding: 2
                        Accessible.name: index === 1 && win.pendingIncomingCount > 0 ? "Transfers, " + win.pendingIncomingCount + " incoming requests awaiting approval" : text
                        contentItem: Item {
                            Row {
                                anchors.centerIn: parent; spacing: 6
                                Text { textFormat: Text.PlainText; text: tabButton.text; color: tabs.currentIndex===tabButton.index ? Theme.text : Theme.muted; font.pixelSize: 12; anchors.verticalCenter: parent.verticalCenter }
                                Rectangle {
                                    id: requestBadge; objectName: tabButton.index === 1 ? "pendingRequestsBadge" : ""
                                    visible: tabButton.index === 1 && win.pendingIncomingCount > 0
                                    width: Math.max(18, requestCount.implicitWidth + 8); height: width; radius: width / 2
                                    color: Theme.selection; border.color: Theme.accent; border.width: 1
                                    Text { id: requestCount; objectName: tabButton.index === 1 ? "pendingRequestsCount" : ""; anchors.centerIn: parent; textFormat: Text.PlainText; text: win.pendingIncomingCount > 99 ? "99+" : win.pendingIncomingCount; color: Theme.accent; font.pixelSize: 10; font.weight: Font.DemiBold }
                                }
                            }
                        }
                        background: Rectangle { color: tabButton.checked ? Theme.panel : tabButton.hovered ? Theme.elevated : "transparent"; Rectangle { anchors.bottom: parent.bottom; height: 2; width: parent.width; color: tabButton.checked ? Theme.accent : "transparent" } }
                    }
                }
            }
        }
        Rectangle {
            visible: banner.length>0; Layout.fillWidth: true; implicitHeight: bannerText.implicitHeight+16; color: Theme.selection
            Text { textFormat: Text.PlainText; id: bannerText; anchors.left: parent.left; anchors.right: dismiss.left; anchors.margins: 9; anchors.verticalCenter: parent.verticalCenter; text: banner; color: bannerBad ? Theme.bad : Theme.text; wrapMode: Text.Wrap; font.pixelSize: 12 }
            UButton { id:dismiss; anchors.right: parent.right; anchors.verticalCenter: parent.verticalCenter; anchors.rightMargin:6; text:"×"; onClicked:banner="" }
        }
        StackLayout {
            currentIndex: tabs.currentIndex; Layout.fillWidth: true; Layout.fillHeight: true
            // Trusted peers: compact rows, with a direct drop target for each device.
            Item {
                ColumnLayout {
                    anchors.fill: parent; anchors.margins: 12; spacing: 8
                    RowLayout {
                        spacing:6
                        UButton { text:"Add peer…"; primary:true; enabled:appState.ready; onClicked: addDialog.open() }
                        Item { Layout.fillWidth:true }
                        Text { textFormat: Text.PlainText; text:selectedPeers.length>1 ? selectedPeers.length+" recipients" : "Send to selected peer"; color:Theme.muted; font.pixelSize:11 }
                        UButton { text:"Files…"; enabled:selectedPeers.length>0 && !handoff.busy; onClicked:handoff.pickSend(selectedPeers,false) }
                        UButton { text:"Folder…"; enabled:selectedPeers.length>0 && !handoff.busy; onClicked:handoff.pickSend(selectedPeers,true) }
                    }
                    Rectangle {
                        Layout.fillWidth:true; Layout.fillHeight:true; color:Theme.panel; border.color:Theme.line
                        ColumnLayout {
                            anchors.fill:parent; spacing:0
                            Rectangle { Layout.fillWidth:true; height:25; color:Theme.elevated
                                RowLayout { anchors.fill:parent; anchors.leftMargin:12; anchors.rightMargin:12
                                    Text { textFormat: Text.PlainText; text:"TRUSTED DEVICES"; font.pixelSize:10; font.letterSpacing:0.6; color:Theme.muted; Layout.fillWidth:true }
                                    Text { textFormat: Text.PlainText; text:"CONNECTION"; font.pixelSize:10; font.letterSpacing:0.6; color:Theme.muted; Layout.preferredWidth:145 }
                                }
                            }
                            ListView {
                                id:peerList; Layout.fillWidth:true; Layout.fillHeight:true; clip:true; model:appState.peers
                                ScrollBar.vertical: ScrollBar { }
                                delegate:Rectangle {
                                    id:peerRow; required property var modelData
                                    width:peerList.width; height:58; color:drop.containsDrag ? Theme.selection : selectedPeers.indexOf(modelData.id)>=0 ? Theme.selection : peerHover.hovered ? Theme.elevated : Theme.panel
                                    HoverHandler { id:peerHover }
                                    TapHandler { onTapped: { if(point.modifiers & Qt.ControlModifier) togglePeer(peerRow.modelData.id); else selectedPeers=[peerRow.modelData.id] } }
                                    RowLayout { anchors.fill:parent; anchors.leftMargin:10; anchors.rightMargin:10; spacing:9
                                        CheckBox { checked:selectedPeers.indexOf(peerRow.modelData.id)>=0; onClicked:togglePeer(peerRow.modelData.id); Layout.preferredWidth:24 }
                                        Image { source:assetPath+"device.svg"; Layout.preferredWidth:24; Layout.preferredHeight:24; opacity:peerRow.modelData.online ? 1 : 0.45 }
                                        ColumnLayout { spacing:3; Layout.fillWidth:true
                                            Text { textFormat: Text.PlainText; text:peerRow.modelData.name; font.pixelSize:12; font.weight:Font.DemiBold; color:Theme.text; elide:Text.ElideRight; Layout.fillWidth:true }
                                            Text { textFormat: Text.PlainText; text:peerRow.modelData.online ? "Online · "+(peerRow.modelData.routeScope || "discovering path")+" · drop files or folders here" : "Offline · transfers wait for connection"; font.pixelSize:10; color:Theme.muted }
                                        }
                                        Rectangle { width:5; height:5; radius:3; color:peerRow.modelData.online ? Theme.accent : Theme.muted }
                                        Text { textFormat: Text.PlainText; text:peerRow.modelData.online ? (peerRow.modelData.connection === "direct" ? "Direct" : peerRow.modelData.connection === "relayed" ? "Relayed" : "Negotiating") : "Offline"; color:Theme.muted; font.pixelSize:11; Layout.preferredWidth:79 }
                                        UButton { text:"Send"; enabled:!handoff.busy; onClicked:handoff.pickSend([peerRow.modelData.id],false) }
                                        UButton { text:"···"; onClicked:peerMenu.popup() }
                                    }
                                    Rectangle { anchors.bottom:parent.bottom; height:1; width:parent.width; color:Theme.line; opacity:0.6 }
                                    DropArea { id:drop; anchors.fill:parent; onDropped:function(dropEvent){if(dropEvent.hasUrls){handoff.dropSend(peerRow.modelData.id,dropEvent.urls);dropEvent.acceptProposedAction()}} }
                                    Menu { id:peerMenu
                                        MenuItem { text:"Send folder…"; onTriggered:handoff.pickSend([peerRow.modelData.id],true) }
                                        MenuItem { text:"Rename…"; onTriggered:{renameId=peerRow.modelData.id;renameField.text=peerRow.modelData.name;renameDialog.open()} }
                                        MenuItem { text:"Copy identity fingerprint"; onTriggered:handoff.copy(peerRow.modelData.fingerprint) }
                                        MenuSeparator { }
                                        MenuItem { text:"Remove peer…"; onTriggered:{removeId=peerRow.modelData.id;removeDialog.open()} }
                                    }
                                }
                                Text { textFormat: Text.PlainText; anchors.centerIn:parent; visible:appState.peers.length===0; text:"No trusted devices yet\nAdd a peer to create your first connection."; horizontalAlignment:Text.AlignHCenter; color:Theme.muted; font.pixelSize:12; lineHeight:1.6 }
                            }
                        }
                    }
                    Repeater { model:appState.pairings
                        Rectangle {
                            required property var modelData
                            visible:modelData.status !== "waiting" || !modelData.incoming
                            Layout.fillWidth:true; implicitHeight:visible ? 53 : 0; color:Theme.panel; border.color:Theme.line
                            RowLayout { anchors.fill:parent; anchors.margins:8
                                ColumnLayout { spacing:3; Layout.fillWidth:true
                                    Text { textFormat: Text.PlainText; text:modelData.incoming && modelData.status==="approval" ? "Approve pairing with "+modelData.name+"?" : (modelData.name || "Invitation")+" · "+modelData.status; color:Theme.text; font.pixelSize:12 }
                                    Text { textFormat: Text.PlainText; text:modelData.fingerprint ? "Identity: "+modelData.fingerprint : "Invitation expires in 15 minutes"; color:Theme.muted; font.pixelSize:10 }
                                }
                                UButton { visible:modelData.incoming && modelData.status==="approval"; text:"Decline"; onClicked:handoff.pairDecision(modelData.id,false) }
                                UButton { visible:modelData.incoming && modelData.status==="approval"; text:"Approve"; primary:true; onClicked:handoff.pairDecision(modelData.id,true) }
                            }
                        }
                    }
                    Text { textFormat: Text.PlainText; text:handoff.busy ? "Preparing transfer · hashing files for integrity…" : "Every transfer requires approval on the receiving device."; color:Theme.muted; font.pixelSize:11 }
                }
            }
            // One transfer list with persistent expansion and compact filters.
            Item {
                ColumnLayout { anchors.fill:parent; anchors.margins:12; spacing:8
                    RowLayout { spacing:4
                        Repeater { model:["all","pending","active","queued","completed","failed"]
                            UButton { required property string modelData; text:modelData.charAt(0).toUpperCase()+modelData.slice(1); primary:filter===modelData; onClicked:filter=modelData }
                        }
                        Item { Layout.fillWidth:true }
                        UButton { text:"Clear history"; onClicked:historyDialog.open() }
                    }
                    Rectangle { Layout.fillWidth:true; Layout.fillHeight:true; color:Theme.panel; border.color:Theme.line
                        ColumnLayout { anchors.fill:parent; spacing:0
                            Rectangle { Layout.fillWidth:true; height:25; color:Theme.elevated
                                RowLayout { anchors.fill:parent; anchors.leftMargin:12; anchors.rightMargin:12
                                    Text { textFormat: Text.PlainText; text:"TRANSFER / PEER"; color:Theme.muted; font.pixelSize:10; Layout.fillWidth:true }
                                    Text { textFormat: Text.PlainText; text:"PROGRESS"; color:Theme.muted; font.pixelSize:10; Layout.preferredWidth:160 }
                                    Text { textFormat: Text.PlainText; text:"ACTIONS"; color:Theme.muted; font.pixelSize:10; Layout.preferredWidth:140 }
                                }
                            }
                            ListView { id:transferList; Layout.fillWidth:true; Layout.fillHeight:true; clip:true; model:appState.transfers; ScrollBar.vertical:ScrollBar{}
                                delegate:Column {
                                    id:transferRow; required property var modelData; width:transferList.width; visible:matches(modelData); height:visible ? mainRow.height+(details.visible ? details.height : 0) : 0
                                    Rectangle { id:mainRow; width:parent.width; height:71; color:inspectId===transferRow.modelData.id ? Theme.selection : Theme.panel
                                        RowLayout { anchors.fill:parent; anchors.margins:10; spacing:8
                                            UButton { text:expanded[transferRow.modelData.id] ? "−" : "+"; implicitWidth:23; onClicked:{var x=Object.assign({},expanded);x[transferRow.modelData.id]=!x[transferRow.modelData.id];expanded=x} }
                                            Text { textFormat: Text.PlainText; text:transferRow.modelData.direction==="incoming" ? "↓" : "↑"; font.pixelSize:18; color:Theme.accent }
                                            ColumnLayout { spacing:4; Layout.fillWidth:true; Layout.minimumWidth:100
                                                Text { textFormat: Text.PlainText; text:transferRow.modelData.label; color:Theme.text; font.pixelSize:12; Layout.fillWidth:true; elide:Text.ElideMiddle }
                                                Text { textFormat: Text.PlainText; text:transferRow.modelData.peer+" · "+statusName(transferRow.modelData.status); color:transferRow.modelData.status==="failed" ? Theme.bad : transferRow.modelData.status==="pending" ? Theme.warning : Theme.muted; font.pixelSize:10; Layout.fillWidth:true; elide:Text.ElideRight }
                                            }
                                            ColumnLayout { spacing:4; Layout.preferredWidth:147
                                                Text { textFormat: Text.PlainText; text:bytes(transferRow.modelData.done)+" / "+bytes(transferRow.modelData.size); color:Theme.muted; font.pixelSize:10 }
                                                Rectangle { Layout.fillWidth:true; height:3; color:Theme.line; Rectangle { height:3; width:parent.width*(transferRow.modelData.size ? Math.min(1,transferRow.modelData.done/transferRow.modelData.size) : transferRow.modelData.status==="completed" ? 1 : 0); color:Theme.accent } }
                                                Text { textFormat: Text.PlainText; text:transferRow.modelData.speed>0 ? bytes(transferRow.modelData.speed)+"/s · "+duration(transferRow.modelData.eta)+" left" : transferRow.modelData.status==="completed" ? "Verified" : "—"; color:Theme.muted; font.pixelSize:10 }
                                            }
                                            RowLayout { Layout.preferredWidth:145; spacing:4
                                                UButton { text:transferRow.modelData.direction==="incoming" && transferRow.modelData.status==="pending" ? "Review" : "Details"; primary:transferRow.modelData.direction==="incoming" && transferRow.modelData.status==="pending"; onClicked:openTransfer(transferRow.modelData.id) }
                                                UButton { text:transferRow.modelData.paused || transferRow.modelData.status==="failed" ? "▶" : "Ⅱ"; visible:!terminal(transferRow.modelData.status)||transferRow.modelData.status==="failed"; enabled:transferRow.modelData.status!=="pending"; implicitWidth:26; onClicked:handoff.transfer(transferRow.modelData.id,transferRow.modelData.status==="failed" ? "retry" : transferRow.modelData.paused ? "resume" : "pause") }
                                                UButton { text:"···"; implicitWidth:26; onClicked:transferMenu.popup() }
                                            }
                                        }
                                        Rectangle { anchors.bottom:parent.bottom; width:parent.width; height:1; color:Theme.line }
                                        Menu { id:transferMenu
                                            MenuItem { text:"Move earlier"; enabled:transferRow.modelData.direction==="outgoing" && !terminal(transferRow.modelData.status); onTriggered:handoff.reorder(transferRow.modelData.id,-1) }
                                            MenuItem { text:"Move later"; enabled:transferRow.modelData.direction==="outgoing" && !terminal(transferRow.modelData.status); onTriggered:handoff.reorder(transferRow.modelData.id,1) }
                                            MenuSeparator { }
                                            MenuItem { text:"Cancel transfer"; enabled:!terminal(transferRow.modelData.status); onTriggered:handoff.transfer(transferRow.modelData.id,"cancel") }
                                        }
                                    }
                                    ListView { id:details; width:parent.width; visible:!!expanded[transferRow.modelData.id]; height:visible ? Math.min(200,transferRow.modelData.entries.length*25) : 0;clip:true;model:visible ? transferRow.modelData.entries : [];ScrollBar.vertical:ScrollBar{}
                                        delegate:Rectangle { required property var modelData; width:details.width; height:25; color:Theme.bg
                                            RowLayout { anchors.fill:parent;anchors.leftMargin:45;anchors.rightMargin:15
                                                Text { textFormat: Text.PlainText;text:modelData.dir ? "▸" : "·";color:Theme.muted;font.pixelSize:11}
                                                Text { textFormat: Text.PlainText;text:modelData.path;color:Theme.muted;font.pixelSize:11;elide:Text.ElideMiddle;Layout.fillWidth:true}
                                                Text { textFormat: Text.PlainText;text:modelData.dir ? "Folder" : bytes(modelData.done)+" / "+bytes(modelData.size);color:Theme.muted;font.pixelSize:10}
                                            }
                                        }
                                    }
                                }
                                Text { textFormat: Text.PlainText; anchors.centerIn:parent; visible:appState.transfers.filter(matches).length===0; text:"No transfers in this view"; color:Theme.muted; font.pixelSize:12 }
                            }
                        }
                    }
                }
            }
            ScrollView { clip:true
                ColumnLayout { width:parent.width; spacing:12
                    Item { Layout.preferredHeight:1 }
                    Text { textFormat: Text.PlainText; text:"DEVICE & RECEIVING"; color:Theme.muted; font.pixelSize:10; font.letterSpacing:0.6; Layout.leftMargin:18 }
                    GridLayout { columns:3; rowSpacing:9; columnSpacing:10; Layout.fillWidth:true; Layout.leftMargin:18; Layout.rightMargin:18
                        Text { textFormat: Text.PlainText; text:"Device name"; color:Theme.text; Layout.preferredWidth:125 }
                        UField { id:deviceName; Layout.fillWidth:true; maximumLength:80; onTextEdited:settingsDirty=true }
                        Item { Layout.preferredWidth:78 }
                        Text { textFormat: Text.PlainText; text:"Receiving directory"; color:Theme.text }
                        UField { id:receivingDir; Layout.fillWidth:true; onTextEdited:settingsDirty=true }
                        UButton { text:"Browse…"; onClicked:handoff.pickDirectory(receivingDir.text,"settings") }
                        Text { textFormat: Text.PlainText; text:"Theme"; color:Theme.text }
                        ComboBox { id:themeBox; model:["Follow system","Dark","Light"]; Layout.preferredWidth:185; implicitHeight:28; font.pixelSize:12; onActivated:settingsDirty=true }
                        Item { }
                        Text { textFormat: Text.PlainText; text:"Automatic startup"; color:Theme.text }
                        CheckBox { id:startup; text:"Start quietly in the system tray"; font.pixelSize:12; onClicked:settingsDirty=true }
                        Item { }
                        Item { }
                        UButton { text:"Save preferences"; primary:true; enabled:settingsDirty; onClicked:{handoff.saveSettings(deviceName.text,receivingDir.text,["system","dark","light"][themeBox.currentIndex],startup.checked,true);settingsDirty=false} }
                        Item { }
                    }
                    Rectangle { Layout.fillWidth:true; Layout.leftMargin:18; Layout.rightMargin:18; height:1; color:Theme.line }
                    Text { textFormat: Text.PlainText; text:"DIAGNOSTICS & HISTORY"; color:Theme.muted; font.pixelSize:10; font.letterSpacing:0.6; Layout.leftMargin:18 }
                    RowLayout { Layout.leftMargin:18; Layout.rightMargin:18; spacing:7
                        UButton { text:"Open logs folder"; onClicked:handoff.openLogs() }
                        UButton { text:"Collect sanitized diagnostics"; onClicked:handoff.diagnostics() }
                        UButton { text:"Clear transfer history…"; onClicked:historyDialog.open() }
                    }
                    Text { textFormat: Text.PlainText; Layout.leftMargin:18; Layout.rightMargin:18; Layout.fillWidth:true; text:"Logs rotate automatically. Diagnostic bundles exclude names, filenames, paths, credentials, and detailed error text."; wrapMode:Text.Wrap; color:Theme.muted; font.pixelSize:11 }
                    Rectangle { Layout.fillWidth:true; Layout.leftMargin:18; Layout.rightMargin:18; height:1; color:Theme.line }
                    Text { textFormat: Text.PlainText; Layout.leftMargin:18; text:"handoff "+(appState.version || "1.0.7")+" · Qt 6 / tailcat"; color:Theme.muted; font.pixelSize:11 }
                    Text { textFormat: Text.PlainText; Layout.leftMargin:18; Layout.rightMargin:18; Layout.fillWidth:true; text:"Device identity: "+(appState.deviceId || "Starting…"); color:Theme.muted; font.pixelSize:10; wrapMode:Text.Wrap }
                    Text { textFormat: Text.PlainText; Layout.leftMargin:18; Layout.rightMargin:18; Layout.fillWidth:true; text:"Closing the window keeps transfers running. Use the tray menu to quit.\nIncoming transfers always require your approval."; color:Theme.muted; font.pixelSize:11; lineHeight:1.5 }
                    Item { Layout.preferredHeight:12 }
                }
            }
        }
        Rectangle { Layout.fillWidth:true; implicitHeight:25; color:Theme.elevated
            RowLayout { anchors.fill:parent; anchors.leftMargin:12; anchors.rightMargin:12
                Text { textFormat: Text.PlainText; text:appState.networkError || (appState.peers.filter(p=>p.online).length+" online · "+appState.peers.length+" trusted"); color:appState.networkError ? Theme.warning : Theme.muted; font.pixelSize:10; Layout.fillWidth:true; elide:Text.ElideRight }
                Text { textFormat: Text.PlainText; text:"Encrypted peer-to-peer transfers"; color:Theme.muted; font.pixelSize:10 }
            }
        }
    }
    Dialog {
        id:addDialog; title:"Add a trusted peer"; modal:true; anchors.centerIn:parent; width:480
        background:Rectangle { color:Theme.panel; border.color:Theme.line; radius:3 }
        contentItem:ColumnLayout { spacing:10
            Text { textFormat: Text.PlainText; text:"Create an invitation and share it privately. Both devices must approve the connection."; color:Theme.muted; wrapMode:Text.Wrap; Layout.fillWidth:true; font.pixelSize:12 }
            UButton { text:"Create 15-minute invitation"; primary:true; enabled:appState.ready; onClicked:{handoff.invite();addDialog.close()} }
            Rectangle { Layout.fillWidth:true; height:1; color:Theme.line }
            Text { textFormat: Text.PlainText; text:"Or paste an invitation from the other device:"; color:Theme.text; font.pixelSize:12 }
            ScrollView { Layout.fillWidth:true; Layout.preferredHeight:80; TextArea { id:joinCode; placeholderText:"handoff1.…"; wrapMode:Text.WrapAnywhere; font.pixelSize:11; color:Theme.text; background:Rectangle{color:Theme.bg;border.color:Theme.line} } }
            Text { textFormat: Text.PlainText; text:"Only approve codes received from someone you trust."; color:Theme.muted; font.pixelSize:11 }
            RowLayout { Item{Layout.fillWidth:true}  UButton{text:"Close";onClicked:addDialog.close()}  UButton{text:"Connect and approve";primary:true;enabled:joinCode.text.trim().length>0;onClicked:{handoff.join(joinCode.text);joinCode.clear();addDialog.close()}} }
        }
    }
    Dialog {
        id:invitationDialog; title:"Share invitation"; modal:true; anchors.centerIn:parent; width:480
        background:Rectangle { color:Theme.panel; border.color:Theme.line; radius:3 }
        contentItem:ColumnLayout { spacing:10
            Text { textFormat: Text.PlainText; text:"Send this entire code through a private messaging service. It expires after 15 minutes and works for one pairing."; color:Theme.muted; wrapMode:Text.Wrap; Layout.fillWidth:true; font.pixelSize:12 }
            ScrollView { Layout.fillWidth:true; Layout.preferredHeight:130; TextArea { id:invitation; readOnly:true; selectByMouse:true; wrapMode:Text.WrapAnywhere; font.pixelSize:11; color:Theme.text; background:Rectangle{color:Theme.bg;border.color:Theme.line} } }
            RowLayout { UButton{text:"Copy invitation";primary:true;onClicked:handoff.copy(invitation.text)} Item{Layout.fillWidth:true} UButton{text:"Done";onClicked:invitationDialog.close()} }
        }
    }
    Dialog {
        id:renameDialog; title:"Rename peer"; modal:true; anchors.centerIn:parent; width:340
        contentItem:ColumnLayout { UField{id:renameField;Layout.fillWidth:true;maximumLength:80} RowLayout{Item{Layout.fillWidth:true} UButton{text:"Cancel";onClicked:renameDialog.close()} UButton{text:"Save";primary:true;onClicked:{handoff.renamePeer(renameId,renameField.text);renameDialog.close()}}} }
    }
    Dialog { id:removeDialog;title:"Remove trusted peer?";modal:true;anchors.centerIn:parent;width:380
        contentItem:ColumnLayout { spacing:12;Text { textFormat: Text.PlainText;text:"This revokes future access and cancels unfinished transfers with this peer. Pair again to reconnect.";wrapMode:Text.Wrap;color:Theme.text;Layout.fillWidth:true} RowLayout{Item{Layout.fillWidth:true} UButton{text:"Keep peer";onClicked:removeDialog.close()} UButton{text:"Remove";onClicked:{handoff.removePeer(removeId);selectedPeers=selectedPeers.filter(p=>p!==removeId);removeDialog.close()}}} }
    }
    Dialog { id:historyDialog;title:"Clear finished transfer history?";modal:true;anchors.centerIn:parent;width:390
        contentItem:ColumnLayout { spacing:12;Text { textFormat: Text.PlainText;text:"Completed, declined, and acknowledged canceled records will be removed. Received files and retryable failures stay available.";wrapMode:Text.Wrap;color:Theme.text;Layout.fillWidth:true} RowLayout{Item{Layout.fillWidth:true} UButton{text:"Keep history";onClicked:historyDialog.close()} UButton{text:"Clear history";onClicked:{handoff.clearHistory();historyDialog.close()}}} }
    }
    Dialog {
        id:requestDialog;title:inspected && inspected.direction==="incoming" && inspected.status==="pending" ? "Review incoming transfer" : "Transfer details";modal:true;anchors.centerIn:parent;width:Math.min(620,win.width-40);height:Math.min(465,win.height-40)
        background:Rectangle{color:Theme.panel;border.color:Theme.line;radius:3}
        contentItem:ColumnLayout { spacing:9
            Text { textFormat: Text.PlainText; text:inspected ? (inspected.direction==="incoming" ? "From " : "To ")+inspected.peer+" · "+bytes(inspected.size)+" total" : ""; color:Theme.text; font.weight:Font.DemiBold; font.pixelSize:12; Layout.fillWidth:true; elide:Text.ElideRight }
            Text { textFormat: Text.PlainText; text:inspected ? statusName(inspected.status)+(inspected.error ? " · "+inspected.error : "") : ""; color:inspected && inspected.error ? Theme.bad : Theme.muted; font.pixelSize:11; wrapMode:Text.Wrap; Layout.fillWidth:true }
            Rectangle { Layout.fillWidth:true;Layout.fillHeight:true;color:Theme.bg;border.color:Theme.line
                ListView { id:preview; anchors.fill:parent;anchors.margins:1;clip:true;model:inspected ? inspected.entries : [];ScrollBar.vertical:ScrollBar{}
                    delegate:RowLayout { required property var modelData; width:preview.width-14; height:27; spacing:7
                        Text { textFormat: Text.PlainText; text:modelData.dir ? "▸" : "·";color:Theme.muted;Layout.leftMargin:8 }
                        Text { textFormat: Text.PlainText; text:modelData.path; color:Theme.text; font.pixelSize:11; Layout.fillWidth:true; elide:Text.ElideMiddle; ToolTip.visible:previewHover.hovered;ToolTip.text:modelData.path;HoverHandler{id:previewHover} }
                        Text { textFormat: Text.PlainText; text:modelData.dir ? "Folder" : bytes(modelData.size); color:Theme.muted;font.pixelSize:10 }
                    }
                }
            }
            Text { textFormat: Text.PlainText; text:inspected ? "Receive into: "+(inspected.destination || appState.settings.receiveDir) : "";color:Theme.muted;font.pixelSize:11;Layout.fillWidth:true;elide:Text.ElideMiddle }
            RowLayout { spacing:6
                UButton { text:"Different folder…";visible:inspected && inspected.direction==="incoming" && inspected.status==="pending";onClicked:handoff.accept(inspectId,true) }
                UButton { text:"Open receiving folder";visible:inspected && inspected.status==="completed" && inspected.direction==="incoming";onClicked:handoff.openFolder(inspected.destination) }
                Item { Layout.fillWidth:true }
                UButton { text:"Close";onClicked:requestDialog.close() }
                UButton { text:"Decline";visible:inspected && inspected.direction==="incoming" && inspected.status==="pending";onClicked:handoff.transfer(inspectId,"decline") }
                UButton { text:"Accept transfer";primary:true;visible:inspected && inspected.direction==="incoming" && inspected.status==="pending";onClicked:handoff.accept(inspectId,false) }
            }
        }
    }
    Dialog {
        id:wizard;title:"Welcome to handoff";modal:true;closePolicy:Popup.NoAutoClose;anchors.centerIn:parent;width:460
        background:Rectangle{color:Theme.panel;border.color:Theme.line;radius:3}
        contentItem:ColumnLayout { spacing:10
            Text { textFormat: Text.PlainText; text:"Choose how this device appears and where approved files are received.";color:Theme.muted;wrapMode:Text.Wrap;Layout.fillWidth:true;font.pixelSize:12 }
            Text { textFormat: Text.PlainText;text:"Device name";color:Theme.text}
            UField{id:wizardName;Layout.fillWidth:true;maximumLength:80}
            Text { textFormat: Text.PlainText;text:"Receiving directory";color:Theme.text}
            RowLayout{UField{id:wizardDir;Layout.fillWidth:true} UButton{text:"Browse…";onClicked:handoff.pickDirectory(wizardDir.text,"wizard")}}
            CheckBox{id:wizardStartup;text:"Start quietly when I log in";checked:true;font.pixelSize:12}
            Text { textFormat: Text.PlainText;text:"Every incoming transfer asks for approval. You can change these preferences in Settings.";color:Theme.muted;wrapMode:Text.Wrap;Layout.fillWidth:true;font.pixelSize:11}
            UButton{text:"Finish setup";primary:true;Layout.alignment:Qt.AlignRight;enabled:wizardName.text.trim().length>0;onClicked:handoff.saveSettings(wizardName.text,wizardDir.text,"system",wizardStartup.checked,true)}
        }
        Connections{target:handoff;function onChanged(){if(appState.settings.configured)wizard.close()}}
    }
}
