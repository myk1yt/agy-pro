@echo off
setlocal disabledelayedexpansion

echo =======================================================
echo   Antigravity CLI Developer Toolkit (agy-tools) Installer
echo =======================================================

where node >nul 2>nul
if errorlevel 1 (
    echo [ERROR] Node.js is not installed or not in your PATH.
    echo Please install Node.js v16+ from https://nodejs.org/
    exit /b 1
)

node -e "process.exit(parseInt(process.versions.node.split('.')[0], 10) >= 16 ? 0 : 1)" >nul 2>nul
if errorlevel 1 (
    echo [ERROR] Node.js version 16 or higher is required.
    echo Please upgrade Node.js from https://nodejs.org/
    exit /b 1
)

where npm >nul 2>nul
if errorlevel 1 (
    echo [ERROR] npm is not installed or not in your PATH.
    echo Please ensure npm is installed and in your PATH.
    exit /b 1
)

set "SCRIPT_DIR=%~dp0"
for %%I in ("%SCRIPT_DIR%..") do set "ROOT_DIR=%%~fI"
set "LAUNCHER_GEN=%ROOT_DIR%\scripts\lib\make-launchers.js"
set "USER_BIN=%USERPROFILE%\.gemini\antigravity-cli\bin"

echo [INFO] Linking agy-tools globally via npm link...
cd /d "%ROOT_DIR%"
call npm link
if errorlevel 1 goto agy_npm_fallback

echo.
echo [SUCCESS] agy-tools, agy-tokens, agy-dashboard, and antigravity-tools installed successfully!
goto agy_marker

:agy_npm_fallback
echo.
echo [WARN] npm link failed. Setting up self-healing fallback launchers...
rem The mkdir check is nested on purpose: `call npm link` above already left ERRORLEVEL 1,
rem and a one-line `if not exist ... mkdir ...` skips mkdir without clearing it, so the
rem check would fire on every re-run (the directory exists by then) and abort a good install.
if not exist "%USER_BIN%" (
    mkdir "%USER_BIN%"
    if errorlevel 1 (
        echo [ERROR] Could not create the launcher directory: %USER_BIN%
        exit /b 1
    )
)
node "%LAUNCHER_GEN%" bat --bin "%USER_BIN%" --root "%ROOT_DIR%" --names "agy-tools.bat,antigravity-tools.bat,agy-tokens.bat,agy-dashboard.bat"
if errorlevel 1 (
    echo [ERROR] Could not generate the self-healing launchers in %USER_BIN%
    echo [ERROR] It needs package.json "name": "agy-tools" plus every script the four launchers run:
    echo [ERROR]     bin\agy-tools.js  - agy-tools.bat and antigravity-tools.bat
    echo [ERROR]     bin\agy-tokens.js - agy-tokens.bat
    echo [ERROR]     bin\agy-dashboard.js - agy-dashboard.bat
    echo [ERROR] Fix the repository layout, then re-run this installer.
    exit /b 1
)
echo [SUCCESS] Created self-healing launchers in %USER_BIN%
echo Make sure %USER_BIN% is in your PATH.
echo The launchers re-resolve the repository root at run time, so they survive moving this folder.

:agy_marker
node "%LAUNCHER_GEN%" marker --root "%ROOT_DIR%"
if errorlevel 1 (
    echo [ERROR] Could not write the agy-tools root marker.
    echo [ERROR] The self-healing launchers read the repository root from that marker; without one they must rescan your profile on every run, so the install aborts instead of degrading.
    echo [ERROR] Fix the error above, then re-run this installer.
    exit /b 1
)

echo.
echo [INFO] Deploying the Antigravity integrations with agy pro...
echo [INFO]   statusLine, rules, plugins, skills, lifecycle hooks
rem Trusted path first: agy-pro's installer deploys its shim to the fixed
rem user-owned path below, so invoke THAT by absolute path. A PATH-earlier `agy`
rem would otherwise run attacker code holding this deployment's --yes, and could
rem persist a payload in the statusLine slot (CWE-426 untrusted search path).
set "AGY_DOOR=%USERPROFILE%\.agy-pro\bin\agy.exe"
if exist "%AGY_DOOR%" goto agy_door_ready

rem No agy-pro shim on this machine: fall back to the CLI that PATH resolves.
where agy >nul 2>nul
if errorlevel 1 goto agy_no_engine
set "AGY_DOOR=agy"

:agy_door_ready
rem `call` keeps this installer alive when agy resolves to a .bat/.cmd shim.
call "%AGY_DOOR%" pro integrations apply tools --yes
if errorlevel 1 (
    echo [ERROR] agy pro integrations apply tools failed; the statusLine integration and the rules, plugins, skills and hooks deployment were NOT completed.
    echo [ERROR] Fix the error above, then re-run this installer.
    exit /b 1
)
goto agy_integrations_done

:agy_no_engine
echo [WARN] The agy CLI is not in your PATH, so the Antigravity integrations were NOT deployed.
echo [WARN] After agy is installed, run this exact command once:
echo          agy pro integrations apply tools --yes
echo [WARN] Then restart Antigravity CLI (agy) to start seeing real-time token tracking.

:agy_integrations_done
echo.

echo Try running:
echo   agy-tools --version
echo   agy-tools prices --currency krw
echo   agy-tokens --help
echo.

rem Explicit success exit: the `where agy` guard leaves ERRORLEVEL 1 on the no-engine branch,
rem and a `call`-context parent would inherit that sticky value. scripts/install.sh exits 0 on
rem the same path (and 1 at its door-fail path, like this file's `exit /b 1` above).
endlocal
exit /b 0
