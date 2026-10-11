# agy-pro One-Click Setup Wizard English Localization Report

**Target File**: `agy-pro/scripts/install.ps1`  
**Date**: 2026-10-11  
**Status**: [SUCCESS]  

---

## 1. Overview & Objectives

In response to user feedback requesting an ultra-simple, professional, beginner-friendly English setup wizard, all output strings, interactive prompts, step indicators, completion cards, and troubleshooting guides in `agy-pro/scripts/install.ps1` were comprehensively converted to natural, polished English.

The script maintains 100% compatibility with Windows PowerShell 5.1 and modern PowerShell 7+, preserving UTF-8 with BOM encoding while ensuring zero mojibake across any Windows locale or code page.

---

## 2. Key Changes & Localized Components

### 2.1 Header & Subtitle
- **Header**: `agy-pro One-Click Automated Setup Wizard`
- **Subtitle**: `Configuring Antigravity Go Shim & Multi-Model Swarm Environment...`

### 2.2 5-Step Execution Workflow
1. **Step 1: Directory Preparation**
   - Step: `1/5 Checking and creating required directories...`
   - Success: `Base directories initialized ($TargetHome)`
2. **Step 2: Go Language Toolchain Verification & Automated Bootstrapping**
   - Step: `2/5 Verifying Go language toolchain...`
   - Missing Notice: `Go is not detected on this system. Starting automated installation...`
   - Winget Attempt: `Attempting official Go package installation via winget...`
   - Portable Archive Download: `Downloading official portable Go archive ($downloadUrl)...`
   - Extraction: `Extracting Go archive...`
   - Success: `Go toolchain detected: $goVer`
   - Failure: `Failed to configure Go toolchain. Please check your internet connection or install Go manually from https://go.dev`
3. **Step 3: Compiling agy-pro Shim Binary**
   - Step: `3/5 Compiling agy-pro shim binary...`
   - Build Failure: `Go build failed (exit code: $LASTEXITCODE)`
   - File Lock / Handle Swap: `Failed to build or swap agy.exe. If agy.exe is currently running in another terminal, please close it and retry.`
   - Success: `Build complete: $TargetExe`
4. **Step 4: PATH Environment Registration**
   - Step: `4/5 Registering ~/.agy-pro/bin to Windows User PATH (highest priority)...`
   - Success: `Windows User PATH successfully updated ($TargetBin)`
5. **Step 5: Post-Installation Diagnostics**
   - Step: `5/5 Running post-installation diagnostics...`
   - Doctor Failure: `Post-installation diagnostics (Doctor) reported an error (exit code: $LASTEXITCODE)`

### 2.3 Completion Celebration Screen & Next Steps
- **Banner**: `Installation Completed Successfully!`
- **Celebration Message**: `Congratulations! agy-pro has been installed and configured.`
- **Locations**:
  - `Binary Location: $TargetExe`
  - `Data Home: $TargetHome`
- **Next Steps Guide**:
  - `Open a NEW terminal window (PowerShell or CMD) and try the following commands:`
  - `1. agy pro setup  : Interactive wizard to connect external AI models (DeepSeek, OpenRouter, Groq, etc.)`
  - `2. agy pro mode   : Switch between Full-Stack (swarm orchestrator) and Clean (vanilla) profiles`
  - `3. agy pro doctor : System diagnostics and toolchain health check`
  - `4. agy            : 100% transparent passthrough to native Antigravity CLI`
- **Interactive Pause**: `Press any key to continue . . . `

### 2.4 Beginner-Friendly Recovery & Troubleshooting Guide (`Show-TroubleshootingGuide`)
When unhandled errors occur during execution, a high-visibility structured guide is shown:
- **Title**: `[ERROR] agy-pro installation encountered an issue.`
- **Details**: `Error Details: $errorMessage`
- **Sections**:
  1. `Check Internet Connection: A network timeout or disruption may have occurred while downloading the Go toolchain.`
  2. `Manual Go Installation: Download and install the official Go installer (.msi) from https://go.dev/dl and run this installer again.`
  3. `Antivirus & Security Software: Check if Windows Defender or third-party antivirus is blocking agy.exe binary creation.`
  4. `Run as Administrator: If permission errors occur, open PowerShell or CMD 'As Administrator' and retry.`
  5. `Seek Assistance: If the issue persists, take a screenshot of this terminal output and report it.`

---

## 3. Verification & Validation

1. **Non-ASCII Character Audit**:
   - `Select-String` audit confirmed 0 non-ASCII or leftover Korean characters in `agy-pro/scripts/install.ps1`.
2. **Encoding Verification**:
   - Inspected file header bytes: `0xEF, 0xBB, 0xBF` (UTF-8 with BOM confirmed).
   - Fully compatible with PowerShell 5.1 (default on Windows 10/11) and PowerShell Core.
3. **PowerShell AST Parsing**:
   - Parsed with `[System.Management.Automation.Language.Parser]::ParseFile()` with 0 errors.
4. **Go Test Suite Integrity**:
   - `go test ./...` and `go vet ./...` executed across all internal packages; all tests pass.
