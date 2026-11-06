@echo off
setlocal
rem Native Windows read-only journal summary. Uses the installed alice-room Python helper inside the Python script.
rem No WSL or manual SSH command required.
set "PY_LAUNCHER=%USERPROFILE%\AppData\Local\Programs\Python\Launcher\py.exe"
if not exist "%PY_LAUNCHER%" set "PY_LAUNCHER=py.exe"
"%PY_LAUNCHER%" -3 "%~dp0alice_journal_report.py" %*
exit /b %errorlevel%
