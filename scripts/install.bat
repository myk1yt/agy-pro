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
set "NPM_LINK_OK=0"

echo [INFO] Linking agy-tools globally via npm link...
cd /d "%ROOT_DIR%"
call npm link
if errorlevel 1 goto agy_npm_warn

set "NPM_LINK_OK=1"
echo.
echo [SUCCESS] agy-tools, agy-tokens, agy-dashboard, agy-pro, and antigravity-tools linked globally via npm!
goto agy_create_launchers

:agy_npm_warn
echo.
echo [WARN] npm link failed. Continuing with self-healing launchers...

:agy_create_launchers
if not exist "%USER_BIN%" (
    mkdir "%USER_BIN%"
    if errorlevel 1 (
        echo [ERROR] Could not create the launcher directory: %USER_BIN%
        exit /b 1
    )
)
node "%LAUNCHER_GEN%" bat --bin "%USER_BIN%" --root "%ROOT_DIR%" --names "agy-tools.bat,antigravity-tools.bat,agy-tokens.bat,agy-dashboard.bat,agy-pro.bat"
if errorlevel 1 (
    echo [ERROR] Could not generate the self-healing launchers in %USER_BIN%
    echo [ERROR] It needs package.json "name": "agy-tools" plus every script the five launchers run:
    echo [ERROR]     bin\agy-tools.js     - agy-tools.bat and antigravity-tools.bat
    echo [ERROR]     bin\agy-tokens.js    - agy-tokens.bat
    echo [ERROR]     bin\agy-dashboard.js - agy-dashboard.bat
    echo [ERROR]     bin\agy-pro.js       - agy-pro.bat
    echo [ERROR] Fix the repository layout, then re-run this installer.
    exit /b 1
)
echo [SUCCESS] Created self-healing launchers in %USER_BIN%

node "%LAUNCHER_GEN%" marker --root "%ROOT_DIR%"
if errorlevel 1 (
    echo [ERROR] Could not write the agy-tools root marker.
    echo [ERROR] The self-healing launchers read the repository root from that marker; without one they must rescan your profile on every run, so the install aborts instead of degrading.
    echo [ERROR] Fix the error above, then re-run this installer.
    exit /b 1
)

if "%NPM_LINK_OK%"=="0" (
    echo Make sure %USER_BIN% is in your PATH.
    echo The launchers re-resolve the repository root at run time, so they survive moving this folder.
)

echo.
echo [INFO] Deploying Antigravity customizations (rules, plugins, skills, hooks, agents, references)...
node "%ROOT_DIR%\scripts\lib\configure-customizations.js"
if errorlevel 1 (
    echo [ERROR] Failed to deploy Antigravity customizations.
    echo [ERROR] Fix the error above, then re-run this installer.
    exit /b 1
)

echo [INFO] Configuring Antigravity statusLine in settings.json...
node "%ROOT_DIR%\scripts\lib\configure-statusline.js"
if errorlevel 1 (
    echo [ERROR] Failed to configure statusLine in settings.json.
    echo [ERROR] Fix the error above, then re-run this installer.
    exit /b 1
)

echo.
echo Try running:
echo   agy-tools --version
echo   agy-tools prices --currency krw
echo   agy-tokens --help
echo   agy-pro setup
echo.
echo Next step (optional): run "agy-pro setup" to configure an external LLM gateway.
echo.

endlocal
exit /b 0
