@echo off
setlocal EnableExtensions
title Agent_b
if not defined AGENTB_HIDDEN_REENTRY (
  set "AGENTB_VISIBLE=0"
  for %%A in (%*) do if /i "%%~A"=="-Console" set "AGENTB_VISIBLE=1"
  if "%AGENTB_VISIBLE%"=="0" (
    wscript.exe //B "%~dp0scripts\launch-hidden.vbs" "%ComSpec%" "/d" "/s" "/c" "set AGENTB_HIDDEN_REENTRY=1&& call ""%~f0"" %*"
    exit /b 0
  )
)
set "AGENT_B_AUTO_CLOSE=0"
for %%A in (%*) do (
  if /i "%%~A"=="-NoPause" set "AGENT_B_AUTO_CLOSE=1"
  if /i "%%~A"=="-Detached" set "AGENT_B_AUTO_CLOSE=1"
)

powershell.exe -NoLogo -NoProfile -File "%~dp0scripts\launch-Agent_b.ps1" %*
set "AGENT_B_EXIT=%ERRORLEVEL%"
if not "%AGENT_B_EXIT%"=="0" (
  echo.
  echo Agent_b could not be opened. Review the error above or %LOCALAPPDATA%\Agent_b\logs\launcher-errors.log.
  if "%AGENT_B_AUTO_CLOSE%"=="0" (
    echo This window will close automatically in 10 seconds.
    timeout /t 10 /nobreak >nul 2>&1
  )
)
exit /b %AGENT_B_EXIT%
