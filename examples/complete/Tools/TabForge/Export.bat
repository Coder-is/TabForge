@echo off
chcp 65001 >nul
cd /d "%~dp0"
tabforge.exe -project=../../tabforge.json
set "TABFORGE_RESULT=%ERRORLEVEL%"
if not "%TABFORGE_RESULT%"=="0" echo Export failed. Check the error above.
pause
exit /b %TABFORGE_RESULT%
