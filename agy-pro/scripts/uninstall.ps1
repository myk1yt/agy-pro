# agy-pro one-click uninstaller for Windows
#
# Removes exactly what agy-pro/scripts/install.ps1 installed:
#   1. the integrations the engine deployed into the Gemini config tree (rules,
#      skills, plugins, hooks and the settings.json statusLine slot) - retracted
#      by the shim FIRST, while the shim still exists
#   2. the ~/.agy-pro/bin entry from the Windows User PATH (that entry only,
#      every other entry is kept verbatim, registry value kind preserved)
#   3. the ~/.agy-pro folder, moved to the Recycle Bin (never deleted permanently)
#
# Options:
#   -KeepState    keep providers.json / state.json / backups; remove the shim only
#                 (the integrations retract still runs: it owns the Gemini config
#                 tree, not provider state, and its backups land in the kept home)
#   -RestorePath  restore the newest path-*.json backup written by the installer
#                 instead of deleting the PATH entry
#   -DryRun       print the planned actions and change nothing
#   -Force        skip the confirmation prompt (scripted use)
#   -NoPause      skip the final "press any key" wait
#   -TargetHome   override ~/.agy-pro (mock folder for testing; mock mode
#                 reports User PATH changes but never writes the registry)
#
# Safe by default: when stdin is not an interactive console (piped input,
# scheduled task, agent shell) the confirmation answers "no" and the script
# exits without touching anything.

[CmdletBinding()]
param(
    [switch]$KeepState,
    [switch]$RestorePath,
    [switch]$DryRun,
    [switch]$Force,
    [switch]$NoPause,
    [string]$TargetHome
)

$ErrorActionPreference = "Stop"

$OutputEncoding = [System.Text.Encoding]::UTF8
try {
    [Console]::OutputEncoding = [System.Text.Encoding]::UTF8
} catch {
    # Non-critical if the console handle does not support changing output encoding
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

function Write-DryRun {
    param([string]$text)
    Write-Host "[DRY-RUN] $text" -ForegroundColor DarkYellow
}

function Write-Skip {
    param([string]$text)
    Write-Host "[SKIP] $text" -ForegroundColor DarkGray
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

# Returns @{ Raw; Kind; Existed } for HKCU\Environment\Path.
# The raw value is read with DoNotExpandEnvironmentNames so %VAR% entries are
# preserved exactly as the user wrote them.
function Read-UserPathRegistry {
    $snapshot = @{ Raw = ""; Kind = "String"; Existed = $false }
    try {
        $envKey = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey("Environment")
        if ($envKey) {
            try {
                $snapshot.Existed = $envKey.GetValueNames() -contains "Path"
                if ($snapshot.Existed) {
                    $snapshot.Raw = [string]$envKey.GetValue("Path", "", [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
                    $snapshot.Kind = [string]$envKey.GetValueKind("Path")
                }
            } finally {
                $envKey.Close()
            }
        }
    } catch {
        Write-Notice "Could not read the User PATH registry value: $_"
    }
    return $snapshot
}

# [Environment]::SetEnvironmentVariable flattens REG_EXPAND_SZ to REG_SZ, which
# would stop %VAR% entries from expanding. Restore the original kind afterwards.
function Write-UserPathValue {
    param([string]$Value, [string]$Kind)
    [Environment]::SetEnvironmentVariable("Path", $Value, "User")
    try {
        $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey("Environment", $true)
        if ($key) {
            try {
                if (([string]$key.GetValueKind("Path")) -ne $Kind) {
                    $key.SetValue("Path", $Value, [Enum]::Parse([Microsoft.Win32.RegistryValueKind], $Kind))
                }
            } finally {
                $key.Close()
            }
        }
    } catch {
        Write-Notice "Could not verify the registry value kind of User PATH: $_"
    }
}

function Remove-UserPathValue {
    try {
        $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey("Environment", $true)
        if ($key) {
            try {
                $key.DeleteValue("Path", $false)
            } finally {
                $key.Close()
            }
        }
    } catch {
        Write-Notice "Could not delete the User PATH registry value: $_"
    }
}

function Split-PathEntries {
    param([string]$Value)
    if (-not $Value) {
        return @()
    }
    return @($Value.Split(';') | ForEach-Object { $_.Trim() } | Where-Object { $_ -ne "" })
}

# Splits entries into Kept / Removed by comparing the expanded form of every
# entry against the target directory, so both "C:\Users\x\.agy-pro\bin" and
# "%USERPROFILE%\.agy-pro\bin" are matched.
function Split-PathEntriesByTarget {
    param([string[]]$Entries, [string]$Target)
    $targetNorm = $Target.TrimEnd('\')
    $kept = @()
    $removed = @()
    foreach ($entry in $Entries) {
        $expanded = $entry
        try {
            $expanded = [Environment]::ExpandEnvironmentVariables($entry)
        } catch {
            $expanded = $entry
        }
        if ($expanded.TrimEnd('\') -ieq $targetNorm -or $entry.TrimEnd('\') -ieq $targetNorm) {
            $removed += $entry
        } else {
            $kept += $entry
        }
    }
    return @{ Kept = $kept; Removed = $removed }
}

# Deletion goes to the Recycle Bin only (never a permanent delete), so a
# mistaken uninstall can always be undone from Explorer.
function Move-ToRecycleBin {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) {
        return $false
    }
    Add-Type -AssemblyName Microsoft.VisualBasic
    $item = Get-Item -LiteralPath $Path -Force
    if ($item.PSIsContainer) {
        [Microsoft.VisualBasic.FileIO.FileSystem]::DeleteDirectory(
            $item.FullName,
            [Microsoft.VisualBasic.FileIO.UIOption]::OnlyErrorDialogs,
            [Microsoft.VisualBasic.FileIO.RecycleOption]::SendToRecycleBin)
    } else {
        [Microsoft.VisualBasic.FileIO.FileSystem]::DeleteFile(
            $item.FullName,
            [Microsoft.VisualBasic.FileIO.UIOption]::OnlyErrorDialogs,
            [Microsoft.VisualBasic.FileIO.RecycleOption]::SendToRecycleBin)
    }
    return $true
}

# Mirrors the installer's printed restore command, but quoted so it can be
# pasted into PowerShell directly (a double-quoted -Command string would let the
# interactive session expand $b before the child process ever sees it).
function Get-RestoreCommandText {
    param([string]$BackupFile)
    $template = @'
powershell -NoProfile -Command '$b = Get-Content ''{0}'' -Raw | ConvertFrom-Json; [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey(''Environment'', $true).SetValue(''Path'', $b.value, [Microsoft.Win32.RegistryValueKind]$b.kind)'
'@
    return ($template -f $BackupFile).Trim()
}

function Restore-UserPathFromBackup {
    param([string]$BackupFile)
    $backup = Get-Content -LiteralPath $BackupFile -Raw | ConvertFrom-Json
    $kindName = "String"
    if ($backup.kind) {
        $kindName = [string]$backup.kind
    }
    $kind = [Microsoft.Win32.RegistryValueKind]::String
    try {
        $kind = [Enum]::Parse([Microsoft.Win32.RegistryValueKind], $kindName)
    } catch {
        Write-Notice "Unknown registry kind '$kindName' in the backup; falling back to String."
        $kind = [Microsoft.Win32.RegistryValueKind]::String
    }

    if ($backup.existed -eq $false) {
        # The key had no Path value before the install; restoring means removing it again.
        Remove-UserPathValue
        return "removed (the backup recorded that no User PATH existed before the install)"
    }

    Write-UserPathValue -Value ([string]$backup.value) -Kind $kindName
    return "restored"
}

try {
    Write-Header "agy-pro Uninstaller"
    Write-Notice "Removes the agy.exe shim, the ~/.agy-pro data home, and the User PATH entry."

    # ---------------------------------------------------------------- targets
    $UserHome = [Environment]::GetFolderPath("UserProfile")
    if (-not $TargetHome) {
        $TargetHome = Join-Path $UserHome ".agy-pro"
    }
    $TargetHome = [System.IO.Path]::GetFullPath($TargetHome)
    $TargetBin = Join-Path $TargetHome "bin"
    $TargetBackups = Join-Path $TargetHome "backups"

    # Refuse targets that would be catastrophic to move to the Recycle Bin.
    $parent = Split-Path -Parent $TargetHome
    if (-not $parent -or $parent.TrimEnd('\') -eq $TargetHome.TrimEnd('\')) {
        throw "Refusing to operate on a drive root: $TargetHome"
    }
    if ($TargetHome.TrimEnd('\') -ieq $UserHome.TrimEnd('\')) {
        throw "Refusing to operate on the user profile root: $TargetHome"
    }

    # -TargetHome is the mock/testing switch: file operations run against that
    # folder, but the live User PATH registry value is never written. Restoring
    # the PATH from a mock backup file would overwrite the live PATH, so that
    # combination is refused outright.
    $mockMode = $PSBoundParameters.ContainsKey('TargetHome')
    if ($mockMode -and $RestorePath -and -not $DryRun) {
        throw "-RestorePath is refused together with -TargetHome: it would overwrite the live User PATH from a mock backup. Add -DryRun to preview it."
    }
    if ($mockMode) {
        Write-Notice "-TargetHome is set (mock mode): the User PATH is reported but never written."
    }

    # -------------------------------------------------------------- inventory
    Write-Step "1/4 Inspecting the current installation..."
    $homeExists = Test-Path -LiteralPath $TargetHome
    $binExists = Test-Path -LiteralPath $TargetBin
    $exePath = Join-Path $TargetBin "agy.exe"
    $exeExists = Test-Path -LiteralPath $exePath
    # The retract runs through the shim and nothing else: the authentic
    # Antigravity CLI is also named agy.exe, so a PATH lookup here could hand
    # the retract to the wrong tool. No shim means no retract, and the plan
    # below prints the command to run instead.
    $agyShim = $null
    if ($exeExists) {
        $agyShim = $exePath
    }

    $pathSnapshot = Read-UserPathRegistry
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $allEntries = Split-PathEntries -Value $userPath
    $split = Split-PathEntriesByTarget -Entries $allEntries -Target $TargetBin
    $entryPresent = @($split.Removed).Count -gt 0

    $pathBackup = $null
    if (Test-Path -LiteralPath $TargetBackups) {
        $pathBackup = Get-ChildItem -LiteralPath $TargetBackups -Filter "path-*.json" -File -ErrorAction SilentlyContinue |
            Sort-Object -Property Name -Descending | Select-Object -First 1
    }
    $modeBackups = @()
    if (Test-Path -LiteralPath $TargetBackups) {
        $modeBackups = @(Get-ChildItem -LiteralPath $TargetBackups -Directory -ErrorAction SilentlyContinue)
    }

    Write-Notice "Target home : $TargetHome"
    if ($exeExists) {
        Write-Success "Shim binary found: $exePath"
    } elseif ($binExists) {
        Write-Notice "Shim folder exists but agy.exe is missing: $TargetBin"
    } else {
        Write-Notice "No shim folder at $TargetBin"
    }
    if ($homeExists) {
        $dataFiles = @(Get-ChildItem -LiteralPath $TargetHome -Force -File -ErrorAction SilentlyContinue |
            Where-Object { $_.Name -ne "bin" } | Select-Object -ExpandProperty Name)
        if ($dataFiles.Count -gt 0) {
            Write-Notice ("Data files  : " + ($dataFiles -join ", "))
        }
    }
    if ($entryPresent) {
        Write-Success ("User PATH entry present: " + (@($split.Removed) -join ", "))
    } else {
        Write-Notice "No $TargetBin entry in the Windows User PATH"
    }
    if ($pathBackup) {
        Write-Notice ("PATH backup : " + $pathBackup.FullName)
    }
    if ($modeBackups.Count -gt 0) {
        Write-Notice ("Profile backups: {0} folder(s) under {1}" -f $modeBackups.Count, $TargetBackups)
    }

    if (-not $homeExists -and -not $entryPresent) {
        Write-Header "Nothing to uninstall"
        Write-Host "agy-pro is not installed for this user: no $TargetHome folder and no PATH entry." -ForegroundColor Green
        Wait-ForUserExit
        exit 0
    }

    # ------------------------------------------------------------------- plan
    Write-Host ""
    Write-Step "2/4 Planned actions"
    if ($KeepState) {
        Write-Notice "Mode: -KeepState (providers.json, state.json and backups are kept)"
    }
    # First mutating step of the run, so it is previewed first: the retract needs
    # the shim and the PATH entry that finds it, both of which this run removes.
    if ($mockMode) {
        Write-Notice "Integrations: mock mode - 'agy pro integrations retract --yes' is previewed, never executed"
    } elseif ($agyShim) {
        Write-Notice "Integrations: retract the deployed Gemini config tree (rules, skills, plugins, hooks + the settings.json statusLine slot) via 'agy pro integrations retract --yes' before the shim and PATH removal"
    } else {
        Write-Notice "Integrations: agy was not found, so the deployed Gemini config files stay where they are; run 'agy pro integrations retract --yes' from a working install BEFORE the next uninstall"
    }
    if ($RestorePath) {
        if ($pathBackup) {
            Write-Notice "User PATH: restore the pre-install value from $($pathBackup.Name)"
        } else {
            Write-Notice "User PATH: no path-*.json backup found; the .agy-pro\bin entry is removed instead"
        }
    } elseif ($entryPresent) {
        Write-Notice "User PATH: remove the $TargetBin entry (all other entries stay untouched)"
    } else {
        Write-Notice "User PATH: nothing to change"
    }
    if ($binExists) {
        Write-Notice "Files: move $TargetBin to the Recycle Bin"
    }
    if ($homeExists -and -not $KeepState) {
        Write-Notice "Files: move $TargetHome to the Recycle Bin (nothing is deleted permanently)"
    }
    if ($modeBackups.Count -gt 0 -and -not $KeepState) {
        Write-Notice "Hint: profile changes in ~/.gemini are undone by 'agy pro mode restore' - run it BEFORE uninstalling if you switched profiles."
    }

    if ($DryRun) {
        Write-Host ""
        Write-DryRun "No changes were made (dry run). Re-run without -DryRun to uninstall."
        Write-Header "Dry Run Completed"
        Wait-ForUserExit
        exit 0
    }

    # ----------------------------------------------------------- confirmation
    if (-not $Force) {
        $proceed = $false
        if ([Console]::IsInputRedirected -or -not [Environment]::UserInteractive) {
            Write-Skip "Non-interactive input detected; answering 'no'. Nothing was changed."
            Write-Notice "Re-run with -Force (or in a real console window) to confirm the removal."
        } else {
            $answer = ""
            try {
                $answer = (Read-Host "Proceed with the removal? (y/N)").Trim()
            } catch {
                $answer = ""
            }
            $proceed = ($answer -match '^(y|yes)$')
            if (-not $proceed) {
                Write-Skip "Cancelled by the user. Nothing was changed."
            }
        }
        if (-not $proceed) {
            Wait-ForUserExit
            exit 0
        }
    }

    # ------------------------------------------------------------ 3/4 action
    Write-Host ""
    Write-Step "3/4 Removing..."

    # Order matters: the integrations retract owns files in the Gemini config
    # tree and can only run while the shim is there to run it, so it goes before
    # the PATH rewrite and before the folder moves below. -KeepState still
    # retracts: the retract is about ~/.gemini deployments, not provider state,
    # and its backups land under $TargetBackups, which -KeepState keeps.
    $retractResult = "not run"
    if ($mockMode) {
        Write-Notice "[MOCK] Integrations: would run 'agy pro integrations retract --yes' via the shim (not executed)"
        $retractResult = "mock (not executed)"
    } elseif (-not $agyShim) {
        Write-Notice "agy was not found: the integration files deployed in the Gemini config tree were NOT retracted."
        Write-Notice "Run 'agy pro integrations retract --yes' from a working agy install BEFORE the next uninstall."
        $retractResult = "not run (agy not found)"
    } else {
        Write-Step "Retracting the deployed integrations: agy pro integrations retract --yes"
        Write-Notice ("Using: " + $agyShim)
        $savedAgyProHome = $env:AGY_PRO_HOME
        # Point the engine at the home this script removes, so the retract backups
        # land in $TargetBackups instead of an unrelated AGY_PRO_HOME.
        $env:AGY_PRO_HOME = $TargetHome
        $retractExit = 1
        try {
            & $agyShim pro integrations retract --yes
            $retractExit = $LASTEXITCODE
        } finally {
            if ($null -eq $savedAgyProHome) {
                $env:AGY_PRO_HOME = ""
            } else {
                $env:AGY_PRO_HOME = $savedAgyProHome
            }
        }
        if ($retractExit -eq 0) {
            Write-Success "Integrations retracted (exit 0); the deployed files were moved into a <ts>_retract folder under $TargetBackups"
            $retractResult = "retracted (exit 0)"
        } else {
            Write-Failure "agy pro integrations retract --yes exited $retractExit (0 ok, 1 runtime error or refusal, 2 usage error)."
            Write-Failure "The integration files deployed in the Gemini config tree were NOT retracted."
            Write-Notice "$TargetBin is removed by this run anyway; a later retract needs a working install or the folder restored from the Recycle Bin."
            $retractResult = "failed (exit $retractExit)"
        }
    }

    $pathResult = "unchanged"
    $pathPlan = "nothing to change"
    if ($RestorePath -and $pathBackup) {
        $pathPlan = "restore the pre-install value from $($pathBackup.Name)"
    } elseif ($RestorePath) {
        $pathPlan = "remove the $TargetBin entry (no path-*.json backup available)"
    } elseif ($entryPresent) {
        $pathPlan = "remove the $TargetBin entry ($((@($split.Kept)).Count) other entries kept)"
    }

    if ($pathPlan -eq "nothing to change") {
        Write-Notice "Windows User PATH: nothing to change"
    } elseif ($mockMode) {
        Write-Notice "[MOCK] Windows User PATH: would $pathPlan"
        $pathResult = "mock (not written): $pathPlan"
    } elseif ($RestorePath -and $pathBackup) {
        $pathResult = Restore-UserPathFromBackup -BackupFile $pathBackup.FullName
        Write-Success "Windows User PATH: $pathResult from $($pathBackup.Name)"
    } else {
        if ($RestorePath) {
            Write-Notice "No path-*.json backup available; removing the entry instead."
        }
        $newUserPath = (@($split.Kept) -join ';')
        if ($pathSnapshot.Existed) {
            Write-UserPathValue -Value $newUserPath -Kind $pathSnapshot.Kind
        } else {
            Write-UserPathValue -Value $newUserPath -Kind "String"
        }
        $pathResult = "entry removed"
        Write-Success "Windows User PATH updated (entry removed, $((@($split.Kept)).Count) other entries kept)"
    }

    # Keep the current process consistent with the registry so `where agy` in
    # this window reflects the removal.
    if ($pathResult -eq "entry removed") {
        $processSplit = Split-PathEntriesByTarget -Entries (Split-PathEntries -Value $env:PATH) -Target $TargetBin
        $env:PATH = (@($processSplit.Kept) -join ';')
    }

    $movedBin = $false
    $movedHome = $false
    if ($KeepState) {
        if ($binExists) {
            $movedBin = Move-ToRecycleBin -Path $TargetBin
            if ($movedBin) {
                Write-Success "Moved to the Recycle Bin: $TargetBin"
            } else {
                Write-Notice "Shim folder not found: $TargetBin"
            }
        } else {
            Write-Notice "No shim folder to remove: $TargetBin"
        }
    } else {
        if ($homeExists) {
            $movedHome = Move-ToRecycleBin -Path $TargetHome
            if ($movedHome) {
                Write-Success "Moved to the Recycle Bin: $TargetHome"
            }
        } else {
            Write-Notice "No data home to remove: $TargetHome"
        }
    }

    # ------------------------------------------------------------- 4/4 summary
    Write-Header "Uninstall Summary"
    Write-Host ""
    Write-Host "  * User PATH   : $pathResult" -ForegroundColor White
    Write-Host "  * Integrations: $retractResult" -ForegroundColor White
    if ($retractResult -eq "retracted (exit 0)" -and -not $KeepState) {
        Write-Host "    (the <ts>_retract backups under $TargetBackups went to the Recycle Bin with the home)" -ForegroundColor Gray
    }
    if ($retractResult -like "failed*" -or $retractResult -eq "not run (agy not found)") {
        Write-Host "    (the deployed Gemini config files are still in place; 'agy pro integrations retract --yes' needs a working install)" -ForegroundColor Red
    }
    if ($KeepState) {
        Write-Host "  * Removed     : $TargetBin (Recycle Bin)" -ForegroundColor White
        Write-Host "  * Kept        : providers.json, state.json, backups ($TargetHome)" -ForegroundColor White
    } else {
        Write-Host "  * Removed     : $TargetHome (Recycle Bin)" -ForegroundColor White
    }
    Write-Host "  * Recoverable : open Explorer > Recycle Bin and restore the folder" -ForegroundColor Gray
    Write-Host ""
    Write-Host "Next steps:" -ForegroundColor Cyan
    Write-Host "  1. Open a NEW terminal and confirm the shim is gone:  where agy" -ForegroundColor Yellow
    Write-Host "  2. The authentic Antigravity CLI is untouched; plain 'agy' keeps working." -ForegroundColor Yellow
    if ($modeBackups.Count -gt 0 -and -not $KeepState) {
        Write-Host "  3. Switched profiles earlier? Restore the folder from the Recycle Bin, run" -ForegroundColor Yellow
        Write-Host "     'agy pro mode restore', then uninstall again." -ForegroundColor Yellow
    }
    if ($pathBackup -and -not $RestorePath) {
        Write-Host ""
        Write-Host "Previous User PATH backup (not applied - the .agy-pro\bin entry was removed instead):" -ForegroundColor Cyan
        Write-Host "  $($pathBackup.FullName)" -ForegroundColor Gray
        Write-Host "To restore it verbatim, paste this into PowerShell:" -ForegroundColor Cyan
        Write-Host "  $(Get-RestoreCommandText -BackupFile $pathBackup.FullName)" -ForegroundColor Gray
        Write-Host "  (or run this uninstaller again with -RestorePath)" -ForegroundColor Gray
    }
    Write-Host ""

    Wait-ForUserExit
    exit 0
} catch {
    Write-Host ""
    Write-Failure $_.Exception.Message
    Write-Notice "Nothing else was changed. Fix the problem and re-run uninstall.cmd."
    Wait-ForUserExit
    exit 1
}
