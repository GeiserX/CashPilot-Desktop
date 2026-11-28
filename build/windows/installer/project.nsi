Unicode true

####
## The CashPilot Desktop installer and uninstaller for Windows.
##
## `wails build -nsis` compiles this file. It writes wails_tools.nsh next to it
## from wails.json first, and the defines below win over that file's defaults.
## To compile it by hand after one such build:
##   makensis -DARG_WAILS_AMD64_BINARY="..\..\bin\CashPilot Desktop.exe" project.nsi
##
## The install is per user, with no admin prompt: the app goes to
## %LOCALAPPDATA%\Programs\CashPilot Desktop and its Apps & Features entry to HKCU.
## The app keeps its data in %APPDATA%\CashPilot Desktop whichever copy runs, so
## a user of the portable exe keeps everything after installing.
##
## The uninstaller runs the app with --uninstall-cleanup first (uninstall.go). That
## removes the background helper's Scheduled Task and the RunOnce value and, when
## the user asks, the earning containers and native earners (with their firewall
## rules) and the data. Then it removes the files, the shortcuts and the entry.
## scripts/windows-installer-check.ps1 checks all of it on a CI runner.
####
!define INFO_COMPANYNAME "GeiserX"
!define INFO_PRODUCTNAME "CashPilot Desktop"
!define PRODUCT_EXECUTABLE "CashPilot Desktop.exe"
!define UNINST_KEY_NAME "CashPilotDesktop"
!define REQUEST_EXECUTION_LEVEL "user"
!define HELPER_TASK "CashPilot\Helper"
!define PORTABLE_EXECUTABLE "CashPilot-Desktop-windows-amd64.exe"

!include "wails_tools.nsh"
!include "LogicLib.nsh"
!include "nsDialogs.nsh"
!include "x64.nsh"
!include "FileFunc.nsh"

# The version information for this two must consist of 4 parts
VIProductVersion "${INFO_PRODUCTVERSION}.0"
VIFileVersion    "${INFO_PRODUCTVERSION}.0"

VIAddVersionKey "CompanyName"     "${INFO_COMPANYNAME}"
VIAddVersionKey "FileDescription" "${INFO_PRODUCTNAME} Installer"
VIAddVersionKey "ProductVersion"  "${INFO_PRODUCTVERSION}"
VIAddVersionKey "FileVersion"     "${INFO_PRODUCTVERSION}"
VIAddVersionKey "LegalCopyright"  "${INFO_COPYRIGHT}"
VIAddVersionKey "ProductName"     "${INFO_PRODUCTNAME}"

ManifestDPIAware true

!include "MUI2.nsh"

!define MUI_ICON "..\icon.ico"
!define MUI_UNICON "..\icon.ico"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\${PRODUCT_EXECUTABLE}"
!define MUI_FINISHPAGE_RUN_TEXT "$(FINISHRUN)"
!define MUI_UNFINISHPAGE_NOAUTOCLOSE

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

UninstPage custom un.OptionsPage un.OptionsPageLeave
!insertmacro MUI_UNPAGE_INSTFILES

# Every language NSIS has of the 20 the app ships, and the installer's own
# strings in each, from frontend/src/locales (go run ./cmd/i18nnsis). NSIS picks
# the Windows display language, and English when it has no match.
!include "${__FILEDIR__}\i18n.nsh"

Name "${INFO_PRODUCTNAME}"
OutFile "..\..\bin\CashPilot-Desktop-setup.exe"
InstallDir "$LOCALAPPDATA\Programs\${INFO_PRODUCTNAME}"
ShowInstDetails show
ShowUninstDetails show
BrandingText "${INFO_PRODUCTNAME} ${INFO_PRODUCTVERSION}"

!define UNINST_ROOT HKCU

# 1 when the user asks the uninstaller to remove the earners, or the data.
Var RemoveEarners
Var DeleteData
Var RemoveEarnersBox
Var DeleteDataBox

# Stops every copy of the app that runs from the install folder, so its files
# can be replaced or deleted. It matches the full path, so a portable copy
# elsewhere keeps running. PowerShell reads the folder from an environment
# variable, so no character in the path can break the command.
!macro StopInstalledCopies
    System::Call 'Kernel32::SetEnvironmentVariable(t "CASHPILOT_INSTDIR", t "$INSTDIR")'
    ${DisableX64FSRedirection}
    nsExec::Exec `"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -NonInteractive -Command "$$p = Join-Path $$env:CASHPILOT_INSTDIR '${PRODUCT_EXECUTABLE}'; Get-CimInstance Win32_Process | Where-Object { $$_.ExecutablePath -and $$_.ExecutablePath -ieq $$p } | ForEach-Object { Stop-Process -Id $$_.ProcessId -Force -ErrorAction SilentlyContinue; Wait-Process -Id $$_.ProcessId -Timeout 10 -ErrorAction SilentlyContinue }"`
    Pop $0
    ${EnableX64FSRedirection}
!macroend

Function .onInit
    !insertmacro wails.checkArchitecture
FunctionEnd

Section
    SetShellVarContext current

    !insertmacro wails.webview2runtime

    DetailPrint "$(CLOSING)"
    !insertmacro StopInstalledCopies

    SetOutPath $INSTDIR
    !insertmacro wails.files

    CreateShortcut "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"
    CreateShortcut "$DESKTOP\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}"

    WriteUninstaller "$INSTDIR\uninstall.exe"

    SetRegView 64
    WriteRegStr ${UNINST_ROOT} "${UNINST_KEY}" "DisplayName" "${INFO_PRODUCTNAME}"
    WriteRegStr ${UNINST_ROOT} "${UNINST_KEY}" "DisplayVersion" "${INFO_PRODUCTVERSION}"
    WriteRegStr ${UNINST_ROOT} "${UNINST_KEY}" "Publisher" "${INFO_COMPANYNAME}"
    WriteRegStr ${UNINST_ROOT} "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\${PRODUCT_EXECUTABLE}"
    WriteRegStr ${UNINST_ROOT} "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
    WriteRegStr ${UNINST_ROOT} "${UNINST_KEY}" "URLInfoAbout" "https://github.com/GeiserX/CashPilot-Desktop"
    WriteRegStr ${UNINST_ROOT} "${UNINST_KEY}" "UninstallString" "$\"$INSTDIR\uninstall.exe$\""
    WriteRegStr ${UNINST_ROOT} "${UNINST_KEY}" "QuietUninstallString" "$\"$INSTDIR\uninstall.exe$\" /S"
    WriteRegDWORD ${UNINST_ROOT} "${UNINST_KEY}" "NoModify" 1
    WriteRegDWORD ${UNINST_ROOT} "${UNINST_KEY}" "NoRepair" 1
    ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
    IntFmt $0 "0x%08X" $0
    WriteRegDWORD ${UNINST_ROOT} "${UNINST_KEY}" "EstimatedSize" "$0"

    # An update stops a running background helper to replace the exe. Start it
    # again; with no helper registered, nothing happens.
    nsExec::Exec '"$SYSDIR\schtasks.exe" /query /tn "${HELPER_TASK}"'
    Pop $0
    ${If} $0 == 0
        nsExec::Exec '"$SYSDIR\schtasks.exe" /run /tn "${HELPER_TASK}"'
        Pop $0
    ${EndIf}
SectionEnd

Function un.onInit
    StrCpy $RemoveEarners 0
    StrCpy $DeleteData 0
    # /REMOVEEARNERS and /DELETEDATA set the two answers for a silent uninstall
    # (/S). Without them a silent uninstall keeps the earners and the data.
    ${GetParameters} $R0
    ClearErrors
    ${GetOptions} $R0 "/REMOVEEARNERS" $R1
    ${IfNot} ${Errors}
        StrCpy $RemoveEarners 1
    ${EndIf}
    ClearErrors
    ${GetOptions} $R0 "/DELETEDATA" $R1
    ${IfNot} ${Errors}
        StrCpy $DeleteData 1
    ${EndIf}
FunctionEnd

# The one question the uninstaller asks: what to remove besides the app.
# Both boxes start empty, so the default keeps the earners and the data.
Function un.OptionsPage
    !insertmacro MUI_HEADER_TEXT "$(OPTIONS_TITLE)" "$(OPTIONS_SUBTITLE)"
    nsDialogs::Create 1018
    Pop $0
    ${If} $0 == error
        Abort
    ${EndIf}
    # Arabic lays the page out right to left; $(^RTL) is 0 for the other languages.
    nsDialogs::SetRTL $(^RTL)

    ${NSD_CreateLabel} 0 0 100% 24u "$(OPTIONS_INTRO)"
    Pop $0

    ${NSD_CreateCheckbox} 0 30u 100% 12u "$(OPTIONS_REMOVEEARNERS)"
    Pop $RemoveEarnersBox
    ${NSD_CreateLabel} 12u 43u -12u 26u "$(OPTIONS_REMOVEEARNERSHELP)"
    Pop $0
    ${If} $RemoveEarners == 1
        ${NSD_Check} $RemoveEarnersBox
    ${EndIf}

    ${NSD_CreateCheckbox} 0 74u 100% 12u "$(OPTIONS_DELETEDATA)"
    Pop $DeleteDataBox
    ${NSD_CreateLabel} 12u 87u -12u 26u "$(OPTIONS_DELETEDATAHELP)"
    Pop $0
    ${If} $DeleteData == 1
        ${NSD_Check} $DeleteDataBox
    ${EndIf}

    # This page is the last one before the removal starts.
    GetDlgItem $0 $HWNDPARENT 1
    SendMessage $0 ${WM_SETTEXT} 0 "STR:$(OPTIONS_UNINSTALL)"

    nsDialogs::Show
FunctionEnd

Function un.OptionsPageLeave
    ${NSD_GetState} $RemoveEarnersBox $0
    ${If} $0 == ${BST_CHECKED}
        StrCpy $RemoveEarners 1
    ${Else}
        StrCpy $RemoveEarners 0
    ${EndIf}
    ${NSD_GetState} $DeleteDataBox $0
    ${If} $0 == ${BST_CHECKED}
        StrCpy $DeleteData 1
    ${Else}
        StrCpy $DeleteData 0
    ${EndIf}
FunctionEnd

Section "uninstall"
    SetShellVarContext current

    DetailPrint "$(CLOSING)"
    !insertmacro StopInstalledCopies

    # What only the app knows about: the background helper, the RunOnce value,
    # and the earners and data when asked.
    StrCpy $1 '"$INSTDIR\${PRODUCT_EXECUTABLE}" --uninstall-cleanup'
    ${If} $RemoveEarners == 1
        StrCpy $1 "$1 --remove-earners"
    ${EndIf}
    ${If} $DeleteData == 1
        StrCpy $1 "$1 --delete-data"
    ${EndIf}
    ${If} ${FileExists} "$INSTDIR\${PRODUCT_EXECUTABLE}"
        nsExec::ExecToLog $1
        Pop $0
    ${Else}
        StrCpy $0 "missing"
    ${EndIf}
    ${If} $0 != 0
        DetailPrint "$(CLEANUPFAILED)"
        SetErrorLevel 2
        ${IfNot} ${Silent}
            MessageBox MB_ICONEXCLAMATION|MB_OK "$(SOMEPARTSLEFT)"
        ${EndIf}
    ${EndIf}

    Delete "$INSTDIR\${PRODUCT_EXECUTABLE}"
    Delete "$INSTDIR\uninstall.exe"
    # Not /r: the folder can be one the user picked, with other files in it.
    RMDir "$INSTDIR"

    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk"
    Delete "$DESKTOP\${INFO_PRODUCTNAME}.lnk"

    # WebView2 keeps its cache in %APPDATA%\<exe name>. The app stores nothing
    # there, so the installed copy's cache always goes. The portable copy's goes
    # with the data.
    RMDir /r "$APPDATA\${PRODUCT_EXECUTABLE}"
    ${If} $DeleteData == 1
        RMDir /r "$APPDATA\${PORTABLE_EXECUTABLE}"
    ${EndIf}

    SetRegView 64
    DeleteRegKey ${UNINST_ROOT} "${UNINST_KEY}"
SectionEnd
