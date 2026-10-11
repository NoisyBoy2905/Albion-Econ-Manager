@echo off
REM Double-click this to pull the latest cloud price history onto your PC.
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0sync-cloud-history.ps1"
echo.
pause
