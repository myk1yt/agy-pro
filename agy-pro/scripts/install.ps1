# agy-pro one-click installer for Windows
# Automatically installs Go toolchain if missing, builds agy.exe, sets up PATH,
# and initializes the agy-pro environment with zero manual configuration.
# - Backs up the previous User PATH (value + registry kind) before rewriting it.
# - Runs `agy pro doctor --strict` and fails loudly on structural problems.
# - Offers the external-model setup wizard when the install succeeds
#   (pass -NoWizard to skip the prompt, -NoPause to skip the final pause).

[CmdletBinding()]
param(
    [switch]$NoPause,
    [switch]$NoWizard
)

$ErrorActionPreference = "Stop"

$OutputEncoding = [System.Text.Encoding]::UTF8
try {
    [Console]::OutputEncoding = [System.Text.Encoding]::UTF8
} catch {
    # Non-critical if console handle does not support changing output encoding
}

function Write-Header {
    param([string]$text)
    Write-Host ""
    Write-Host "============================================================" -ForegroundColor Cyan
    Write-Host "  $text" -ForegroundColor Cyan
    Write-Host "============================================================" -ForegroundColor Cyan
}

function Write-Step {
    param([string]$text)
    Write-Host ">> $text" -ForegroundColor Yellow
}

function Write-Success {
    param([string]$text)
    Write-Host "[OK] $text" -ForegroundColor Green
}

function Write-Notice {
    param([string]$text)
    Write-Host "[INFO] $text" -ForegroundColor Gray
}

function Write-Failure {
    param([string]$text)
    Write-Host "[ERROR] $text" -ForegroundColor Red
}

function Wait-ForUserExit {
    if ($NoPause) {
        return
    }
    Write-Host ""
    Write-Host "Press any key to continue . . . " -ForegroundColor Gray
    try {
        if ([Environment]::UserInteractive -and -not [Console]::IsInputRedirected) {
            $null = [System.Console]::ReadKey($true)
        }
    } catch {
        # Fallback if Console.ReadKey is not available
    }
}

function Test-GoVersionSufficient {
    param([string]$GoExecutable)
    # agy-pro's go.mod declares `go 1.22`; older toolchains cannot build it.
    # Returns $true only when `go version` parses and meets 1.22+.
    try {
        $verOutput = (& $GoExecutable version 2>$null) -join " "
    } catch {
        return $false
    }
    if ($verOutput -match 'go(\d+)\.(\d+)') {
        $major = [int]$Matches[1]
        $minor = [int]$Matches[2]
        return ($major -gt 1) -or ($major -eq 1 -and $minor -ge 22)
    }
    return $false
}

function Show-TroubleshootingGuide {
    param([string]$errorMessage)
    Write-Host ""
    Write-Host "============================================================" -ForegroundColor Red
    Write-Host "  [ERROR] agy-pro installation encountered an issue." -ForegroundColor Red
    Write-Host "============================================================" -ForegroundColor Red
    Write-Host ""
    Write-Host "Error Details: $errorMessage" -ForegroundColor Red
    Write-Host ""
    Write-Host "------------------------------------------------------------" -ForegroundColor DarkGray
    Write-Host "[Beginner-Friendly Recovery Guide]" -ForegroundColor Yellow
    Write-Host "  1. Check Internet Connection:" -ForegroundColor White
    Write-Host "     - A network timeout or disruption may have occurred while downloading the Go toolchain." -ForegroundColor Gray
    Write-Host "  2. Manual Go Installation:" -ForegroundColor White
    Write-Host "     - Download and install the official Go installer (.msi) from https://go.dev/dl and run this installer again." -ForegroundColor Gray
    Write-Host "  3. Antivirus & Security Software:" -ForegroundColor White
    Write-Host "     - Check if Windows Defender or third-party antivirus is blocking agy.exe binary creation." -ForegroundColor Gray
    Write-Host "  4. Run as Administrator:" -ForegroundColor White
    Write-Host "     - If permission errors occur, open PowerShell or CMD 'As Administrator' and retry." -ForegroundColor Gray
    Write-Host "  5. Seek Assistance:" -ForegroundColor White
    Write-Host "     - If the issue persists, take a screenshot of this terminal output and report it." -ForegroundColor Gray
    Write-Host "------------------------------------------------------------" -ForegroundColor DarkGray
    Write-Host ""
}

try {
    Write-Header "agy-pro One-Click Automated Setup Wizard"
    Write-Notice "Configuring Antigravity Go Shim & Multi-Model Swarm Environment..."

    # 1. Resolve paths
    $ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
    $AgyProDir = Split-Path -Parent $ScriptDir
    $UserHome = [Environment]::GetFolderPath("UserProfile")
    $TargetHome = Join-Path $UserHome ".agy-pro"
    $TargetBin = Join-Path $TargetHome "bin"
    $TargetBackups = Join-Path $TargetHome "backups"
    $TargetExe = Join-Path $TargetBin "agy.exe"

    Write-Step "1/5 Checking and creating required directories..."
    $dirsToCreate = @($TargetHome, $TargetBin, $TargetBackups)
    foreach ($dir in $dirsToCreate) {
        if (-not (Test-Path $dir)) {
            New-Item -ItemType Directory -Force -Path $dir | Out-Null
        }
    }
    Write-Success "Base directories initialized ($TargetHome)"

    # 2. Check / Install Go toolchain
    Write-Step "2/5 Verifying Go language toolchain..."
    $GoExe = $null

    # Check current PATH
    $goInPath = Get-Command "go.exe" -ErrorAction SilentlyContinue
    if ($goInPath) {
        $GoExe = $goInPath.Source
    }

    # Check Program Files
    if (-not $GoExe -and (Test-Path "C:\Program Files\Go\bin\go.exe")) {
        $GoExe = "C:\Program Files\Go\bin\go.exe"
    }

    # Check ~/.agy-pro/go
    $portableGo = Join-Path $TargetHome "go\bin\go.exe"
    if (-not $GoExe -and (Test-Path $portableGo)) {
        $GoExe = $portableGo
    }

    # An existing toolchain older than the go.mod requirement (Go 1.22) cannot
    # build agy-pro; route it into the automated install path below instead of
    # failing at compile time.
    if ($GoExe -and -not (Test-GoVersionSufficient -GoExecutable $GoExe)) {
        $tooOld = ""
        try { $tooOld = (& $GoExe version 2>$null) -join " " } catch { }
        Write-Notice "Detected Go toolchain is too old for agy-pro ($tooOld). Go 1.22+ is required; installing a newer toolchain..."
        $GoExe = $null
    }

    if (-not $GoExe) {
        Write-Notice "No usable Go toolchain found (none installed, or older than Go 1.22). Starting automated installation..."
        
        # Try winget first
        $wingetAvailable = Get-Command "winget.exe" -ErrorAction SilentlyContinue
        if ($wingetAvailable) {
            Write-Notice "Attempting official Go package installation via winget..."
            try {
                $wingetProc = Start-Process -FilePath "winget.exe" `
                    -ArgumentList "install --id GoLang.Go -e --silent --accept-source-agreements --accept-package-agreements" `
                    -NoNewWindow -Wait -PassThru
                if ($wingetProc.ExitCode -eq 0 -and (Test-Path "C:\Program Files\Go\bin\go.exe") -and (Test-GoVersionSufficient -GoExecutable "C:\Program Files\Go\bin\go.exe")) {
                    $GoExe = "C:\Program Files\Go\bin\go.exe"
                }
            } catch {
                Write-Notice "Exception occurred during winget installation: $_"
            }
        }

        # If winget did not complete, use portable Go zip fallback
        if (-not $GoExe) {
            $tempZip = Join-Path $env:TEMP "go1.23.6.windows-amd64.zip"
            $downloadUrl = "https://dl.google.com/go/go1.23.6.windows-amd64.zip"
            Write-Notice "Downloading official portable Go archive ($downloadUrl)..."
            
            if (Get-Command "curl.exe" -ErrorAction SilentlyContinue) {
                & curl.exe -L $downloadUrl -o $tempZip --silent --show-error
            } else {
                Invoke-WebRequest -Uri $downloadUrl -OutFile $tempZip -UseBasicParsing
            }

            Write-Notice "Extracting Go archive..."
            if (Get-Command "tar.exe" -ErrorAction SilentlyContinue) {
                & tar.exe -xf $tempZip -C $TargetHome
            } else {
                Expand-Archive -Path $tempZip -DestinationPath $TargetHome -Force
            }

            Remove-Item $tempZip -Force -ErrorAction SilentlyContinue

            if (Test-Path $portableGo) {
                $GoExe = $portableGo
            }
        }
    }

    if (-not $GoExe -or -not (Test-Path $GoExe) -or -not (Test-GoVersionSufficient -GoExecutable $GoExe)) {
        throw "Failed to configure a Go toolchain (Go 1.22+ required). Please check your internet connection or install Go manually from https://go.dev"
    }

    # Add Go directory to process PATH
    $goDir = Split-Path -Parent $GoExe
    $env:PATH = "$goDir;$env:PATH"
    $goVer = & $GoExe version
    Write-Success "Go toolchain detected: $goVer"

    # 3. Build agy-pro shim binary
    Write-Step "3/5 Compiling agy-pro shim binary..."
    Push-Location $AgyProDir
    $tempBuildOutput = Join-Path $TargetBin "agy.tmp.exe"
    try {
        & $GoExe build -ldflags="-s -w" -o $tempBuildOutput ./cmd/agy
        if ($LASTEXITCODE -ne 0) {
            # Fallback: the repository ships a prebuilt shim binary; use it so a
            # broken local toolchain still completes the installation. The user
            # is told exactly where the binary came from.
            $repoExe = Join-Path $AgyProDir "bin\agy.exe"
            if (Test-Path $repoExe) {
                Write-Notice "Go build failed (exit code: $LASTEXITCODE). Falling back to the repository prebuilt binary."
                Write-Notice "Source of agy.exe: $repoExe (prebuilt fallback, not freshly compiled)."
                Copy-Item -Path $repoExe -Destination $tempBuildOutput -Force
            } else {
                throw "Go build failed (exit code: $LASTEXITCODE) and no prebuilt fallback binary exists at $repoExe. Check the Go error output above or install Go manually from https://go.dev"
            }
        }

        # Safely swap into target
        if (Test-Path $TargetExe) {
            try {
                Remove-Item $TargetExe -Force -ErrorAction Stop
            } catch {
                # If TargetExe is locked by another process, move it aside
                $oldExe = Join-Path $TargetBin "agy.old.$([System.Guid]::NewGuid().ToString('N').Substring(0,8)).exe"
                try {
                    Move-Item -Path $TargetExe -Destination $oldExe -Force -ErrorAction Stop
                } catch {
                    throw "Failed to build or swap agy.exe. If agy.exe is currently running in another terminal, please close it and retry."
                }
            }
        }
        Move-Item -Path $tempBuildOutput -Destination $TargetExe -Force

        # Housekeeping: keep only the newest agy.old.*.exe swap remnant; older
        # ones are stale copies of previous installs and only waste disk space.
        $oldShims = @(Get-ChildItem -Path $TargetBin -Filter "agy.old.*.exe" -File -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending)
        if ($oldShims.Count -gt 1) {
            $staleCount = $oldShims.Count - 1
            $oldShims | Select-Object -Skip 1 | Remove-Item -Force -ErrorAction SilentlyContinue
            Write-Notice "Cleaned up $staleCount stale agy.old.*.exe backup(s); kept the most recent one."
        }
    } finally {
        Pop-Location
        if (Test-Path $tempBuildOutput) {
            Remove-Item $tempBuildOutput -Force -ErrorAction SilentlyContinue
        }
    }
    Write-Success "Build complete: $TargetExe"

    # 4. Register ~/.agy-pro/bin to Windows User PATH (prepend to highest priority)
    Write-Step "4/5 Registering ~/.agy-pro/bin to Windows User PATH (highest priority)..."

    # Back up the current User PATH (raw, unexpanded value + registry kind)
    # before touching it, so a corrupted PATH can always be restored.
    $oldPathKind = [string][Microsoft.Win32.RegistryValueKind]::String
    $oldPathRaw = ""
    $pathExisted = $false
    try {
        $envKey = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey("Environment")
        if ($envKey) {
            try {
                $oldPathRaw = [string]$envKey.GetValue("Path", "", [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
                $pathExisted = $envKey.GetValueNames() -contains "Path"
                if ($pathExisted) {
                    $oldPathKind = [string]$envKey.GetValueKind("Path")
                }
            } finally {
                $envKey.Close()
            }
        }
    } catch {
        Write-Notice "Could not read the current registry PATH for backup: $_"
    }

    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if (-not $userPath) {
        $userPath = ""
    }

    $entries = $userPath.Split(';') | ForEach-Object { $_.Trim() } | Where-Object { $_ -ne "" }
    $cleanedEntries = @()
    foreach ($entry in $entries) {
        if ($entry.TrimEnd('\') -ne $TargetBin.TrimEnd('\')) {
            $cleanedEntries += $entry
        }
    }

    # Prepend TargetBin to the top
    $newUserPath = "$TargetBin;" + ($cleanedEntries -join ';')
    [Environment]::SetEnvironmentVariable("Path", $newUserPath, "User")
    $env:PATH = "$TargetBin;$env:PATH"

    # [Environment]::SetEnvironmentVariable may flatten a REG_EXPAND_SZ value
    # to REG_SZ, which would stop %VAR% entries from expanding. Restore the
    # original registry kind when it changed.
    try {
        $kindKey = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey("Environment", $true)
        if ($kindKey) {
            try {
                if ($pathExisted -and ([string]$kindKey.GetValueKind("Path")) -ne $oldPathKind) {
                    $kindKey.SetValue("Path", $newUserPath, [Enum]::Parse([Microsoft.Win32.RegistryValueKind], $oldPathKind))
                }
            } finally {
                $kindKey.Close()
            }
        }
    } catch {
        Write-Notice "Could not verify the registry value kind of User PATH: $_"
    }

    # Persist the previous PATH so it can be restored verbatim.
    $pathBackupFile = Join-Path $TargetBackups ("path-" + (Get-Date -Format "yyyyMMdd_HHmmss") + ".json")
    try {
        $pathBackup = [ordered]@{
            created   = (Get-Date).ToString("o")
            hive      = "HKCU\Environment"
            valueName = "Path"
            existed   = $pathExisted
            kind      = $oldPathKind
            value     = $oldPathRaw
        }
        $pathBackup | ConvertTo-Json | Set-Content -Path $pathBackupFile -Encoding UTF8
        Write-Success "Windows User PATH successfully updated ($TargetBin)"
        Write-Notice "Previous User PATH backed up to: $pathBackupFile"
        Write-Notice "If the PATH was damaged by this install, restore it with:"
        $restoreCmd = 'powershell -NoProfile -Command ''$b = Get-Content "{0}" -Raw | ConvertFrom-Json; [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey("Environment", $true).SetValue("Path", $b.value, [Microsoft.Win32.RegistryValueKind]$b.kind)''' -f $pathBackupFile
        Write-Notice ("  " + $restoreCmd)
    } catch {
        Write-Notice "Could not write the PATH backup file: $_"
        Write-Success "Windows User PATH successfully updated ($TargetBin)"
    }

    # 5. Verification
    Write-Step "5/5 Running post-installation diagnostics (strict)..."
    & $TargetExe pro doctor --strict
    if ($LASTEXITCODE -ne 0) {
        Write-Notice "Doctor --strict reported structural problems (exit code: $LASTEXITCODE)."
        Write-Notice "Most common cause: the authentic Antigravity CLI is not installed, or the shim is not reachable on PATH."
        Write-Notice "Run 'agy pro doctor' (without --strict) in a NEW terminal for full details, fix the [FAIL] items, then re-run this installer."
        throw "Post-installation diagnostics (Doctor --strict) reported an error (exit code: $LASTEXITCODE)"
    }

    Write-Header "Installation Completed Successfully!"
    Write-Host ""
    Write-Host "Congratulations! agy-pro has been installed and configured." -ForegroundColor Green
    Write-Host ""
    Write-Host "  * Binary Location: $TargetExe" -ForegroundColor White
    Write-Host "  * Data Home: $TargetHome" -ForegroundColor White
    Write-Host ""
    Write-Host "Open a NEW terminal window (PowerShell or CMD) and try the following commands:" -ForegroundColor Cyan
    Write-Host "  1. agy pro setup  : Interactive wizard to connect external AI models (DeepSeek, OpenRouter, Groq, etc.)" -ForegroundColor Yellow
    Write-Host "  2. agy pro mode   : Switch between Full-Stack (swarm orchestrator) and Clean (vanilla) profiles" -ForegroundColor Yellow
    Write-Host "  3. agy pro doctor : System diagnostics and toolchain health check" -ForegroundColor Yellow
    Write-Host "  4. agy            : 100% transparent passthrough to native Antigravity CLI" -ForegroundColor Yellow
    Write-Host ""

    # Offer the external-model setup wizard right away so a fresh install ends
    # with at least one working provider. -NoWizard (automation) skips it.
    if (-not $NoWizard) {
        $wizardAnswer = ""
        try {
            $wizardAnswer = (Read-Host "Run the external model setup wizard now? (y/N)").Trim()
        } catch {
            # Non-interactive stdin: treat as "no".
        }
        if ($wizardAnswer -match '^(y|yes)$') {
            Write-Step "Launching the provider setup wizard (agy pro setup)..."
            & $TargetExe pro setup
            if ($LASTEXITCODE -ne 0) {
                Write-Notice "Setup wizard exited with code $LASTEXITCODE. You can re-run 'agy pro setup' anytime."
            }
        } else {
            Write-Notice "Skipping the wizard. Run 'agy pro setup' anytime to connect external models."
        }
    }

    Wait-ForUserExit
    exit 0
} catch {
    Show-TroubleshootingGuide $_.Exception.Message
    Wait-ForUserExit
    exit 1
}
