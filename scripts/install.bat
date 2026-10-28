@echo off
setlocal enabledelayedexpansion

echo ===================================================================
echo   Antigravity ^& Gemini Configuration, Rules ^& Skills Installer
echo ===================================================================
echo.

set "TARGET_GLOBAL=%USERPROFILE%\.gemini"
set "SCRIPT_DIR=%~dp0"
set "ROOT_DIR=%SCRIPT_DIR%.."

echo [INFO] Target Global Directory: %TARGET_GLOBAL%

:: Create necessary directories
if not exist "%TARGET_GLOBAL%" mkdir "%TARGET_GLOBAL%"
if not exist "%TARGET_GLOBAL%\rules" mkdir "%TARGET_GLOBAL%\rules"
if not exist "%TARGET_GLOBAL%\skills" mkdir "%TARGET_GLOBAL%\skills"
if not exist "%TARGET_GLOBAL%\skills\usage" mkdir "%TARGET_GLOBAL%\skills\usage"
if not exist "%TARGET_GLOBAL%\skills\autonomous-orchestrator" mkdir "%TARGET_GLOBAL%\skills\autonomous-orchestrator"
if not exist "%TARGET_GLOBAL%\hooks" mkdir "%TARGET_GLOBAL%\hooks"

:: Copy rules
echo [INFO] Installing Rules...
copy /Y "%ROOT_DIR%\rules\AGENTS.md" "%TARGET_GLOBAL%\rules\AGENTS.md" >nul
copy /Y "%ROOT_DIR%\rules\GEMINI.md" "%TARGET_GLOBAL%\rules\GEMINI.md" >nul
copy /Y "%ROOT_DIR%\rules\AGENTS.md" "%TARGET_GLOBAL%\AGENTS.md" >nul
copy /Y "%ROOT_DIR%\rules\GEMINI.md" "%TARGET_GLOBAL%\GEMINI.md" >nul

:: Copy skills
echo [INFO] Installing Skills...
copy /Y "%ROOT_DIR%\skills\usage\SKILL.md" "%TARGET_GLOBAL%\skills\usage\SKILL.md" >nul
copy /Y "%ROOT_DIR%\skills\autonomous-orchestrator\SKILL.md" "%TARGET_GLOBAL%\skills\autonomous-orchestrator\SKILL.md" >nul

:: Copy hooks
echo [INFO] Installing Hooks...
copy /Y "%ROOT_DIR%\hooks\hooks.json" "%TARGET_GLOBAL%\hooks\hooks.json" >nul

echo.
echo ===================================================================
echo [SUCCESS] Antigravity/Gemini configuration installed successfully!
echo.
echo Installed Locations:
echo   - Rules:  %TARGET_GLOBAL%\rules\
echo   - Skills: %TARGET_GLOBAL%\skills\
echo   - Hooks:  %TARGET_GLOBAL%\hooks\hooks.json
echo ===================================================================

endlocal
