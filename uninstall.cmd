@echo off
setlocal
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0agy-pro\scripts\uninstall.ps1" %*
exit /b %ERRORLEVEL%
