@echo off
setlocal
call "%~dp0scripts\install.bat" %*
exit /b %ERRORLEVEL%
