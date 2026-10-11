@echo off
setlocal
cd /d "%~dp0"
title agy-pro 원클릭 자동 설치 마법사
cls
echo ============================================================
echo   agy-pro 초간편 자동 설치를 시작합니다...
echo ============================================================
echo.

powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0agy-pro\scripts\install.ps1" %*
set "EXIT_CODE=%ERRORLEVEL%"

if %EXIT_CODE% neq 0 (
    echo.
    echo [오류] 설치 중 문제가 발생했습니다. (종료 코드: %EXIT_CODE%)
    pause
    exit /b %EXIT_CODE%
)

echo.
pause
exit /b 0
