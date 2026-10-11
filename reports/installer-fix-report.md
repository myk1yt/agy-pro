# agy-pro Installer Fix & Hardening Report

**Author / Worker**: installer-fix  
**Target Files**: `install.cmd`, `install.ps1`, `agy-pro/scripts/install.ps1`, `manual.txt`  
**Date**: 2026-10-11  
**Status**: [SUCCESS]  

---

## 1. Problem Diagnosis & Root Cause Analysis

### 1.1 Symptoms Reported
When running `.\install.cmd` in Windows PowerShell or Command Prompt, fatal syntax/parsing errors occurred:
```
'cho' is not recognized as an internal or external command,
'ecutionPolicy' is not recognized as an internal or external command,
'IT_CODE' is not recognized as an internal or external command,
```

### 1.2 Root Cause
- Windows `cmd.exe` processes batch files line-by-line using byte-offset seeking.
- When a batch script (`.cmd`/`.bat`) contains multi-byte UTF-8 sequences (such as Korean characters `원클릭`, `초간편`, `[오류]`) without code page switching or with BOM discrepancies, `cmd.exe` miscalculates byte positions during file seek operations.
- This causes commands to be parsed mid-token:
  - `@echo` $\rightarrow$ `'cho'`
  - `-ExecutionPolicy` $\rightarrow$ `'ecutionPolicy'`
  - `EXIT_CODE` $\rightarrow$ `'IT_CODE'`
- Furthermore, Windows PowerShell 5.1 interprets script files lacking a Byte Order Mark (BOM) as ANSI (CP949 on Korean Windows), causing Korean text in terminal outputs to corrupt into mojibake unless saved with a UTF-8 BOM.

---

## 2. Implemented Solutions

### 2.1 Pure ASCII Minimalist Root `install.cmd`
- Stripped all Korean characters and comments from `install.cmd`.
- Maintained 100% pure ASCII byte stream (all byte values $\le 127$), standard CRLF line endings, and NO BOM.
- Quoted paths safely for spaces and special characters:
  ```cmd
  @echo off
  setlocal
  powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0agy-pro\scripts\install.ps1" %*
  exit /b %ERRORLEVEL%
  ```
- All user-facing UI, messages, error handling, and formatting are delegated cleanly to PowerShell.

### 2.2 Root `install.ps1` Dual Entry Point
- Added root `install.ps1` launcher so users running either `.\install.ps1` or `.\install.cmd` in PowerShell get immediate execution:
  ```powershell
  # agy-pro root installer launcher
  & "$PSScriptRoot\agy-pro\scripts\install.ps1" @args
  exit $LASTEXITCODE
  ```

### 2.3 Hardened & Beginner-Friendly `agy-pro/scripts/install.ps1`
1. **UTF-8 with BOM Encoding**:
   - File saved with UTF-8 BOM (`0xEF 0xBB 0xBF`) to ensure Windows PowerShell 5.1 and PowerShell 7+ parse all Korean strings without mojibake.
2. **Explicit Console & Pipeline Encodings**:
   - Configured `$OutputEncoding = [System.Text.Encoding]::UTF8` and `[Console]::OutputEncoding = [System.Text.Encoding]::UTF8`.
3. **Beginner-Friendly Troubleshooting Guide (`Show-TroubleshootingGuide`)**:
   - Wrapped the entire 5-step installer in a `try ... catch` block.
   - On error, displays a structured recovery guide including:
     1. Internet connection check (Go toolchain automatic download).
     2. Manual Go download link (`https://go.dev/dl`).
     3. Antivirus / Windows Defender binary creation check.
     4. Guidance on running as Administrator if permissions fail.
     5. Error capture and issue reporting instructions.
4. **Interactive Double-Click Window Retention (`Wait-ForUserExit`)**:
   - Added interactive pause prompt (`"계속하려면 아무 키나 누르십시오 . . ."`) so the Explorer double-click console window does not close abruptly on completion or failure.
   - Safe interactive detection (`[Environment]::UserInteractive -and -not [Console]::IsInputRedirected`) and `-NoPause` switch to ensure automated scripts and CI runs do not hang.
5. **Locked Binary Swap Protection**:
   - In Step 3, if existing `agy.exe` is locked/held by another running process, renames it safely before atomic replacement.

### 2.4 User Manual Update (`manual.txt`)
- Updated Section 1 of both Korean and English guides:
  - Documented Method A: Double-clicking `install.cmd` from Windows File Explorer.
  - Documented Method B: Executing `.\install.cmd` or `.\install.ps1` from PowerShell / Command Prompt.
  - Explained the "Press any key to continue" prompt behavior on Explorer execution.

---

## 3. Verification & Validation Matrix

| Test Scenario | Command | Expected Result | Actual Result | Status |
|---|---|---|---|---|
| **ASCII Check** | Verify byte values of `install.cmd` | All bytes < 128, no BOM | Max byte: 122, BOM: False | PASS |
| **BOM Check** | Verify bytes 0..2 of `install.ps1` | `239 187 191` (UTF-8 BOM) | `239 187 191` | PASS |
| **CMD Execution** | `cmd.exe /c "install.cmd -NoPause"` | No parsing errors, exit code 0 | Full 5-step pass, exit code 0 | PASS |
| **PowerShell Launcher** | `powershell.exe -File .\install.ps1 -NoPause` | Forwards args, exit code 0 | Full 5-step pass, exit code 0 | PASS |
| **Non-interactive Pause** | `cmd.exe /c "install.cmd"` | Shows prompt, skips blocking when redirected | Completed cleanly, no hang | PASS |
| **Go Vet & Test** | `go test ./...` and `go vet ./...` in `agy-pro` | All tests pass, no lint issues | `ok agypro/...`, exit code 0 | PASS |

---

## 4. Modified / Created Artifacts

- [`install.cmd`](file:///D:/OneDrive/Projects/Antigravity-cli/install.cmd): 100% pure ASCII cmd launcher.
- [`install.ps1`](file:///D:/OneDrive/Projects/Antigravity-cli/install.ps1): Root PowerShell forwarder.
- [`agy-pro/scripts/install.ps1`](file:///D:/OneDrive/Projects/Antigravity-cli/agy-pro/scripts/install.ps1): Enhanced installer script with UTF-8 BOM, pause prompt, locked-file handling, and error recovery guide.
- [`manual.txt`](file:///D:/OneDrive/Projects/Antigravity-cli/manual.txt): Updated manual with both installation methods.
- [`reports/installer-fix-report.md`](file:///D:/OneDrive/Projects/Antigravity-cli/reports/installer-fix-report.md): This verification report.
