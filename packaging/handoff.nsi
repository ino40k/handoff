Unicode true
Target amd64-unicode
!include "MUI2.nsh"
!include "x64.nsh"
!define APP "handoff"
!define VERSION "1.0.4"
!ifndef BUNDLE
!define BUNDLE "../dist/handoff-windows"
!endif
Name "handoff ${VERSION}"
OutFile "../dist/handoff-${VERSION}-windows-x64-setup.exe"
InstallDir "$LOCALAPPDATA\Programs\handoff"
InstallDirRegKey HKCU "Software\handoff" "InstallDir"
RequestExecutionLevel user
SetCompressor /SOLID lzma
Icon "../assets/handoff.ico"
UninstallIcon "../assets/handoff.ico"
!define MUI_ABORTWARNING
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "../LICENSE"
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN "$INSTDIR\handoff.exe"
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"
Function .onInit
 ${IfNot} ${RunningX64}
  MessageBox MB_ICONSTOP "handoff requires 64-bit Windows."
  Abort
 ${EndIf}
 check_running:
 System::Call 'kernel32::OpenMutexW(i 0x100000, i 0, w "Local\handoff-is-running") p .r0'
 ${If} $0 != 0
  System::Call 'kernel32::CloseHandle(p r0)'
  MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "Please quit handoff from its tray menu before installing or upgrading." IDRETRY check_running
  Abort
 ${EndIf}
FunctionEnd
Section "handoff" SEC_APP
 SetShellVarContext current
 SetOutPath "$INSTDIR"
 File /r "${BUNDLE}/*"
 WriteUninstaller "$INSTDIR\Uninstall.exe"
 ExecWait '$\"$INSTDIR\handoff.exe$\" --register-shortcut' $0
 ${If} $0 != 0
  MessageBox MB_ICONEXCLAMATION "Windows could not register the notification shortcut. You can retry by running handoff.exe --register-shortcut."
 ${EndIf}
 WriteRegStr HKCU "Software\handoff" "InstallDir" "$INSTDIR"
 WriteRegStr HKCU "Software\Classes\handoff" "" "URL:handoff transfer request"
 WriteRegStr HKCU "Software\Classes\handoff" "URL Protocol" ""
 WriteRegStr HKCU "Software\Classes\handoff\shell\open\command" "" '$\"$INSTDIR\handoff.exe$\" $\"%1$\"'
 WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\handoff" "DisplayName" "handoff"
 WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\handoff" "DisplayVersion" "${VERSION}"
 WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\handoff" "DisplayIcon" "$INSTDIR\assets\handoff.ico"
 WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\handoff" "UninstallString" '$\"$INSTDIR\Uninstall.exe$\"'
 WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\handoff" "QuietUninstallString" '$\"$INSTDIR\Uninstall.exe$\" /S'
 WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\handoff" "NoModify" 1
 WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\handoff" "NoRepair" 1
SectionEnd
Function un.onInit
 check_running:
 System::Call 'kernel32::OpenMutexW(i 0x100000, i 0, w "Local\handoff-is-running") p .r0'
 ${If} $0 != 0
  System::Call 'kernel32::CloseHandle(p r0)'
  MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "Please quit handoff from its tray menu before uninstalling." IDRETRY check_running
  Abort
 ${EndIf}
FunctionEnd
Section "Uninstall"
 SetShellVarContext current
 Delete "$SMPROGRAMS\handoff.lnk"
 DeleteRegKey HKCU "Software\Classes\handoff"
 DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "handoff"
 DeleteRegKey HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\handoff"
 DeleteRegKey HKCU "Software\handoff"
 RMDir /r "$INSTDIR\runtime"
 RMDir /r "$INSTDIR\frontend"
 RMDir /r "$INSTDIR\assets"
 RMDir /r "$INSTDIR\bin"
 RMDir /r "$INSTDIR\licenses"
 Delete "$INSTDIR\handoff.exe"
 Delete "$INSTDIR\README.txt"
 Delete "$INSTDIR\Uninstall.exe"
 RMDir "$INSTDIR"
 ; The separate per-user data directory is retained for reinstallation.
SectionEnd
