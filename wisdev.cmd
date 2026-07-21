@echo off
setlocal
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0scripts\wisdev.ps1" %*
exit /b %ERRORLEVEL%
