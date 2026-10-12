# agy-pro root installer launcher (Windows)
#
# Self-relaunch guard: when this script is started directly from an existing
# PowerShell session whose execution policy blocks scripts (Restricted /
# AllSigned / RemoteSigned + a downloaded file), re-run it through a child
# `powershell -NoProfile -ExecutionPolicy Bypass` process - the same launch
# shape install.cmd uses - so double-click and one-liner users never hit
# "running scripts is disabled on this system".
# install.cmd below already launches with those flags, so it takes the fast path.

$relaunchGuard = $env:AGY_INSTALL_RELAUNCH_GUARD
if (-not $relaunchGuard) {
    $needsRelaunch = $false
    try {
        $effectivePolicy = Get-ExecutionPolicy -ErrorAction Stop
        if ($effectivePolicy -ne [Microsoft.PowerShell.ExecutionPolicy]::Bypass -and
            $effectivePolicy -ne [Microsoft.PowerShell.ExecutionPolicy]::Unrestricted) {
            $needsRelaunch = $true
        }
    } catch {
        # Policy query failed; assume the safe path and relaunch.
        $needsRelaunch = $true
    }
    # ISE and other non-console hosts break Read-Host/ReadKey prompts in the
    # installer; a real console child is always safe.
    if (-not $needsRelaunch -and $Host.Name -ne 'ConsoleHost') {
        $needsRelaunch = $true
    }

    if ($needsRelaunch) {
        $env:AGY_INSTALL_RELAUNCH_GUARD = '1'
        Write-Host "[INFO] Relaunching the installer via: powershell -NoProfile -ExecutionPolicy Bypass" -ForegroundColor Yellow
        & powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$PSCommandPath" @args
        exit $LASTEXITCODE
    }
}

& "$PSScriptRoot\agy-pro\scripts\install.ps1" @args
exit $LASTEXITCODE
