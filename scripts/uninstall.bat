@echo off
setlocal enabledelayedexpansion

echo ===================================================================
echo   Antigravity ^& Gemini Configuration, Rules ^& Skills Uninstaller
echo ===================================================================
echo.

set "TARGET_GLOBAL=%USERPROFILE%\.gemini"

echo [INFO] Target Global Directory: %TARGET_GLOBAL%
echo [INFO] This removes ONLY the files installed by this bundle:
echo          rules\AGENTS.md, rules\GEMINI.md, AGENTS.md, GEMINI.md,
echo          skills\usage\SKILL.md, skills\autonomous-orchestrator\SKILL.md
echo          hooks\hooks.json
echo.

:: Remove root-level rule copies
if exist "%TARGET_GLOBAL%\AGENTS.md" del /F /Q "%TARGET_GLOBAL%\AGENTS.md" && echo [REMOVED] %TARGET_GLOBAL%\AGENTS.md
if exist "%TARGET_GLOBAL%\GEMINI.md" del /F /Q "%TARGET_GLOBAL%\GEMINI.md" && echo [REMOVED] %TARGET_GLOBAL%\GEMINI.md

:: Remove rules
if exist "%TARGET_GLOBAL%\rules\AGENTS.md" del /F /Q "%TARGET_GLOBAL%\rules\AGENTS.md" && echo [REMOVED] %TARGET_GLOBAL%\rules\AGENTS.md
if exist "%TARGET_GLOBAL%\rules\GEMINI.md" del /F /Q "%TARGET_GLOBAL%\rules\GEMINI.md" && echo [REMOVED] %TARGET_GLOBAL%\rules\GEMINI.md

:: Remove skills
if exist "%TARGET_GLOBAL%\skills\usage\SKILL.md" del /F /Q "%TARGET_GLOBAL%\skills\usage\SKILL.md" && echo [REMOVED] %TARGET_GLOBAL%\skills\usage\SKILL.md
if exist "%TARGET_GLOBAL%\skills\usage" rd /S /Q "%TARGET_GLOBAL%\skills\usage" 2>nul && echo [REMOVED] %TARGET_GLOBAL%\skills\usage\
if exist "%TARGET_GLOBAL%\skills\autonomous-orchestrator\SKILL.md" del /F /Q "%TARGET_GLOBAL%\skills\autonomous-orchestrator\SKILL.md" && echo [REMOVED] %TARGET_GLOBAL%\skills\autonomous-orchestrator\SKILL.md
if exist "%TARGET_GLOBAL%\skills\autonomous-orchestrator" rd /S /Q "%TARGET_GLOBAL%\skills\autonomous-orchestrator" 2>nul && echo [REMOVED] %TARGET_GLOBAL%\skills\autonomous-orchestrator\

:: Remove hooks file (directory left intact for other hooks)
if exist "%TARGET_GLOBAL%\hooks\hooks.json" del /F /Q "%TARGET_GLOBAL%\hooks\hooks.json" && echo [REMOVED] %TARGET_GLOBAL%\hooks\hooks.json

echo.
echo ===================================================================
echo [SUCCESS] Antigravity/Gemini configuration bundle uninstalled!
echo   (Directories created by the installer are left in place;
echo    no user data outside this bundle's own files was touched.)
echo ===================================================================

endlocal
