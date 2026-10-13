@echo off
rem =====================================================================
rem AgentWorker Windows guest launcher
rem
rem Started by the `AgentWorker` scheduled task, which runs as `Docker` in
rem the interactive session (UAC is disabled in this image and Docker is an
rem Administrator, so this process already holds a full High-IL admin token
rem - jobs therefore need no UAC/elevation dance at all).
rem
rem Responsibilities:
rem   1. fetch the pod-provided per-pod env (WORKER_TOKEN + SANDBOX_PROXY) from
rem      http://host.lan:8090/env
rem   2. fetch the current agent-worker.exe from http://host.lan:8090/worker
rem      (boot-fetch ONLY — no disk fallback: the container always serves the
rem      worker, so a worker upgrade is an image change, not a golden change)
rem   3. start agent-worker.exe as the current (docker) user
rem =====================================================================
setlocal enabledelayedexpansion

set "WROOT=C:\Users\Docker"
set "BIN=%WROOT%\agent-worker.exe"
set "DL=%WROOT%\agent-worker.dl.exe"
set "WS=%WROOT%\workspace"
set "LOG=%WS%\agent-worker.log"

if not exist "%WS%" mkdir "%WS%"

rem --- 1. pod env: WORKER_TOKEN (pre-auth) + SANDBOX_PROXY (HTTP proxy) -----
rem The guest does not inherit the pod's env; the container publishes the few
rem per-pod values at host.lan:8090/env as KEY=VALUE lines.
set "PODENV=%TEMP%\aw-env.txt"
set /a N=0
:try
curl.exe -s -m 2 -o "%PODENV%" http://host.lan:8090/env 2>nul
for %%A in ("%PODENV%") do set "PODSZ=%%~zA"
if defined PODSZ if !PODSZ! GTR 0 goto have
set /a N+=1
if !N! lss 10 (ping -n 2 127.0.0.1 >nul & goto try)
:have
set "WORKER_TOKEN="
for /f "usebackq tokens=1,* delims==" %%a in ("%PODENV%") do (
  if /i "%%a"=="WORKER_TOKEN" set "WORKER_TOKEN=%%b"
  if /i "%%a"=="SANDBOX_PROXY" set "SANDBOX_PROXY=%%b"
)
if defined SANDBOX_PROXY (
  set "HTTP_PROXY=!SANDBOX_PROXY!"
  set "HTTPS_PROXY=!SANDBOX_PROXY!"
  set "http_proxy=!SANDBOX_PROXY!"
  set "https_proxy=!SANDBOX_PROXY!"
  set "NO_PROXY=localhost,127.0.0.1,.svc,.svc.cluster.local,host.lan,artifact.worker.svc.cluster.local"
  set "no_proxy=!NO_PROXY!"
  rem Set the machine proxies. winget reads the WinINET (IE) proxy, NOT WinHTTP,
  rem so BOTH must be set or winget/VS/dotnet installs hang on GitHub. Bypass
  rem in-cluster + guest-local names so artifact/host.lan stay DIRECT.
  set "BYPASS=<local>;*.svc;*.svc.cluster.local;host.lan;*.nip.io;localhost;127.0.0.1"
  netsh winhttp set proxy proxy-server="!SANDBOX_PROXY!" bypass-list="!BYPASS!" >nul 2>&1
  reg add "HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings" /v ProxyEnable /t REG_DWORD /d 1 /f >nul 2>&1
  reg add "HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings" /v ProxyServer /t REG_SZ /d "!SANDBOX_PROXY!" /f >nul 2>&1
  reg add "HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings" /v ProxyOverride /t REG_SZ /d "!BYPASS!" /f >nul 2>&1
)
rem --- 2. firewall -----------------------------------------------------------
rem STATIC: the firewall is disabled at BAKE time (persistent registry state on
rem the golden disk: EnableFirewall=0 for all profiles). The launcher does NOT
rem re-apply it every boot — persistent machine state takes the static route.
rem (A sandbox is single-tenant behind the pod's private QEMU user-net, so no
rem "Windows Security" popup can block the worker.)

rem --- 3. boot-fetch the worker binary (dynamic ONLY, no disk fallback) -----
rem The container always serves the worker at host.lan:8090/worker. Fetch it on
rem every boot and run THAT binary (a worker upgrade must never require a golden
rem rebake). If the fetch fails there is no fallback — the worker does not start.
>>"%LOG%" echo [%DATE% %TIME%] launcher: fetching worker from host.lan:8090/worker
del "%DL%" >nul 2>&1
curl.exe -s -m 60 -o "%DL%" http://host.lan:8090/worker 2>>"%LOG%"
for %%A in ("%DL%") do set "DLSZ=%%~zA"
if defined DLSZ if !DLSZ! GTR 1000000 (
  move /y "%DL%" "%BIN%" >nul 2>&1 || set "BIN=%DL%"
) else (
  del "%DL%" >nul 2>&1
  echo [%DATE% %TIME%] launcher: worker fetch FAILED (no fallback) >>"%LOG%"
  ping -n 5 127.0.0.1 >nul
  goto run
)

rem --- 3b. xa11y computer-use CLI (STATIC: baked into the golden disk) ------
rem --- 3c. native-a11y (est://) daemon: boot-fetch like the worker ----------
rem The Go worker talks to native-a11y over est:// on 127.0.0.1:48079; the
rem daemon owns capture/encode/input/UIA. Fetch it on every boot (image change,
rem not golden rebake) and start it before the worker.
set "NABIN=%WROOT%\native-a11y.exe"
set "NADL=%WROOT%\native-a11y.dl.exe"
>>"%LOG%" echo [%DATE% %TIME%] launcher: fetching native-a11y from host.lan:8090/native-a11y
del "%NADL%" >nul 2>&1
curl.exe -s -m 60 -o "%NADL%" http://host.lan:8090/native-a11y 2>>"%LOG%"
for %%A in ("%NADL%") do set "NASZ=%%~zA"
if defined NASZ if !NASZ! GTR 1000000 (
  move /y "%NADL%" "%NABIN%" >nul 2>&1
  echo [%DATE% %TIME%] launcher: starting native-a11y on :48079 >>"%LOG%"
  start "" /b "%NABIN%" serve >>"%LOG%" 2>&1
) else (
  del "%NADL%" >nul 2>&1
  echo [%DATE% %TIME%] launcher: native-a11y fetch FAILED >>"%LOG%"
)

rem xa11y is a stable CLI, so it takes the STATIC route: it is BAKED into the
rem golden disk at %XDIR%\xa11y.exe and %XDIR% is already on the machine Path
rem (baked by the bake step). The launcher does NOT boot-fetch it; it only
rem ensures %XDIR% is on THIS process's PATH so the worker (started below) and
rem every job it runs find xa11y. Do NOT rewrite the machine/user Path here:
rem %PATH% is only this session's value (no dotnet/MSVC/SDK entries), so a
rem `reg add ... /d "%PATH%;%XDIR%"` would CLOBBER the machine Path (real bug).
set "XDIR=%WROOT%\bin"
set "PATH=%PATH%;%XDIR%"

rem --- 4. run the worker in this (docker) session, supervised ------------
rem The worker can exit on its own (a panic in the shell interpreter, a bad
rem job, ...). Without a supervisor the task ends and the worker stays down
rem until the next logon — losing the sandbox. Loop it, with a short backoff
rem so a crash-on-start cannot spin the CPU. (macOS gets the same effect from
rem launchd's KeepAlive.)
rem
rem On-demand toolchains: the guest can only reach the container (host.lan:8090),
rem whose nginx bridges /artifacts/ to the in-cluster artifact registry — so the
rem worker's installer fetches toolchains through that bridge.
set "WORKER_TOOLCHAIN_INDEX=http://artifact.worker.svc.cluster.local/artifacts/generic/toolchains/index/index.json"
set /a RESTARTS=0
:run
>>"%LOG%" echo [%DATE% %TIME%] launcher: starting worker as %USERNAME% (%BIN%)
"%BIN%" -addr 0.0.0.0:48080 -workspace "%WS%" -db "%WS%\jobs.db" >>"%LOG%" 2>&1
set "RC=!ERRORLEVEL!"
set /a RESTARTS+=1
>>"%LOG%" echo [%DATE% %TIME%] launcher: worker exited rc=!RC! (restart #!RESTARTS!)
rem Backoff: 3s normally, up to ~30s if it keeps dying immediately.
set /a WAIT=3
if !RESTARTS! GTR 5 set /a WAIT=10
if !RESTARTS! GTR 20 set /a WAIT=30
ping -n !WAIT! 127.0.0.1 >nul
goto run

endlocal
