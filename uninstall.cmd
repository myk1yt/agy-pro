@echo off
setlocal
node "%~dp0scripts\lib\uninstall.js" %*
exit /b %ERRORLEVEL%
