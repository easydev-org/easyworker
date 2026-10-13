@echo off
rem =====================================================================
rem install-worker.cmd - (re)install the guest-side worker.
rem
rem Runs as SYSTEM. Normally unnecessary: the image already bakes the worker,
rem the launcher and the AgentWorker task into the guest disk. Use this only to
rem refresh a running guest (e.g. point it at a newer agent-worker.exe).
rem
rem Edit BASE to a URL serving agent-worker.exe + the two .cmd files, then run:
rem   schtasks /create /tn EWInstall /tr "cmd /c C:\Users\Docker\ewws\install-worker.cmd" ^
rem     /sc once /st 23:59 /ru SYSTEM /rl HIGHEST /f
rem   schtasks /run /tn EWInstall
rem (started via a task so the worker it kills part-way cannot kill the install)
rem =====================================================================
setlocal
set "W=C:\Users\Docker"
set "EWWS=%W%\workspace"
set "BASE=http://172.30.0.1:8099"
set "LOG=%EWWS%\install-worker.log"

echo === install start %DATE% %TIME% === > "%LOG%"

echo [1] stop old worker >> "%LOG%" 2>&1
schtasks /end /tn AgentWorker >> "%LOG%" 2>&1
schtasks /delete /tn AgentWorker /f >> "%LOG%" 2>&1
taskkill /f /im agent-worker-v0.5.2.exe >> "%LOG%" 2>&1
taskkill /f /im agent-worker.exe >> "%LOG%" 2>&1
ping -n 4 127.0.0.1 >nul

echo [2] fetch files >> "%LOG%" 2>&1
curl.exe -s -m 90 -o "%W%\agent-worker.exe"    "%BASE%/agent-worker.exe"    >> "%LOG%" 2>&1
curl.exe -s -m 90 -o "%W%\worker-launch.cmd" "%BASE%/worker-launch.cmd" >> "%LOG%" 2>&1
curl.exe -s -m 90 -o "%W%\ewelevate.cmd"     "%BASE%/ewelevate.cmd"     >> "%LOG%" 2>&1
dir "%W%\agent-worker.exe" "%W%\worker-launch.cmd" "%W%\ewelevate.cmd" >> "%LOG%" 2>&1

echo [3] register AgentWorker: Docker user, HIGHEST, interactive, at logon >> "%LOG%" 2>&1
schtasks /create /tn AgentWorker /tr "cmd /c %W%\worker-launch.cmd" /sc onlogon /ru Docker /rp admin /rl HIGHEST /it /f >> "%LOG%" 2>&1
if errorlevel 1 ( echo TASK_CREATE_FAILED >> "%LOG%" & goto :end )

echo [4] start now >> "%LOG%" 2>&1
schtasks /run /tn AgentWorker >> "%LOG%" 2>&1
ping -n 12 127.0.0.1 >nul

echo [5] state >> "%LOG%" 2>&1
schtasks /query /tn AgentWorker /v /fo list >> "%LOG%" 2>&1
tasklist /fi "imagename eq agent-worker.exe" /v >> "%LOG%" 2>&1
curl.exe -s -o NUL -w "healthz=%{http_code}" http://127.0.0.1:48080/healthz >> "%LOG%" 2>&1
echo. >> "%LOG%"

:end
echo === install done %DATE% %TIME% === >> "%LOG%" 2>&1
