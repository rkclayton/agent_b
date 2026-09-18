[CmdletBinding()]
param()

# Item 2eq on a disposable install. A disposable Agent_b is installed and
# started through its sign-in shortcut; a real service-account tool process
# (the shell tool, through TestServiceAccountToolCannotReachProductionWindow)
# then tries to find its hidden window, post and send it WM_CLOSE and a session
# end, and set its graceful-stop event. The disposable server must still be the
# same running process afterwards with nothing new in its launcher log, and the
# stop event, set by the operator's own account, must still stop it gracefully.
# The service-account credential is a disposable copy of the operator's stored
# one under a Public root the service account can reach; it is removed after.

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'removal-guard.ps1')
. (Join-Path $PSScriptRoot 'agentb-stop.ps1')
$repository = Split-Path -Parent $PSScriptRoot
$go = Join-Path $repository '.tools\go\bin\go.exe'
$tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\')
$testRoot = Join-Path $tempRoot ('Agent_b-window-probe-' + [Guid]::NewGuid().ToString('N'))
$testApplication = Join-Path $testRoot 'Application\Agent_b'
$testData = Join-Path $testRoot 'Data\Agent_b'
$testWorkspace = Join-Path $testRoot 'ProgramData\Agent_b\workspace'
$testStart = Join-Path $testRoot 'StartMenu'
$testRegistry = 'HKCU:\Software\Agent_b-Window-Probe-' + [Guid]::NewGuid().ToString('N')
$publicRoot = Join-Path 'C:\Users\Public' ('agentb-window-probe-' + [Guid]::NewGuid().ToString('N'))
$executable = Join-Path $testApplication 'Agent_b.exe'

function Get-Running {
    return @(Get-Process -Name 'Agent_b' -ErrorAction SilentlyContinue | Where-Object {
        try { [IO.Path]::GetFullPath($_.Path).Equals($executable, [StringComparison]::OrdinalIgnoreCase) } catch { $false }
    })
}

try {
    & powershell.exe -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'build-candidate.ps1') -SourceDirectory $repository
    if ($LASTEXITCODE -ne 0) { throw "Candidate build exited $LASTEXITCODE." }
    & powershell.exe -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'install-Agent_b.ps1') -ApplicationDirectory $testApplication -DataDirectory $testData -WorkspaceDirectory $testWorkspace -StartMenuDirectory $testStart -UninstallRegistryPath $testRegistry -TestMode
    if ($LASTEXITCODE -ne 0) { throw "Disposable install exited $LASTEXITCODE." }
    $configPath = Join-Path $testData 'harness.json'
    $config = Get-Content -Raw -LiteralPath $configPath | ConvertFrom-Json
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    try { $listener.Start(); $port = ([Net.IPEndPoint]$listener.LocalEndpoint).Port } finally { $listener.Stop() }
    $config.listen = "127.0.0.1:$port"
    [IO.File]::WriteAllText($configPath, ($config | ConvertTo-Json -Depth 100) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))

    $shortcut = (New-Object -ComObject WScript.Shell).CreateShortcut((Join-Path $testStart 'Startup\Agent_b.lnk'))
    Start-Process -FilePath $shortcut.TargetPath -ArgumentList $shortcut.Arguments -WorkingDirectory $shortcut.WorkingDirectory | Out-Null
    $deadline = [DateTime]::UtcNow.AddSeconds(60)
    do {
        Start-Sleep -Milliseconds 500
        $ready = $false
        try { $null = Invoke-RestMethod -Uri "http://127.0.0.1:$port/api/state" -TimeoutSec 3; $ready = $true } catch { }
    } while (-not ($ready -and (Get-Running).Count -eq 1) -and [DateTime]::UtcNow -lt $deadline)
    if (-not $ready) { throw 'The disposable Agent_b never became ready.' }
    $target = (Get-Running)[0]
    $launcherLog = Join-Path $testData 'logs\launcher-errors.log'
    $before = if (Test-Path -LiteralPath $launcherLog) { @(Get-Content -LiteralPath $launcherLog).Count } else { 0 }
    Write-Host "TARGET: disposable Agent_b PID $($target.Id) on port $port"

    foreach ($directory in 'workspace', 'data') { $null = New-Item -ItemType Directory -Force (Join-Path $publicRoot $directory) }
    Copy-Item -LiteralPath (Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'Agent_b\.agentb-shell-credential.dpapi') -Destination (Join-Path $publicRoot 'data')
    $env:AGENTB_WINDOW_PROBE_PID = [string]$target.Id
    $env:AGENTB_CAPABILITY_WORKSPACE = Join-Path $publicRoot 'workspace'
    $env:AGENTB_CAPABILITY_DATA = Join-Path $publicRoot 'data'
    Push-Location $repository
    try {
        & $go test -count=1 -v -run 'TestServiceAccountToolCannotReachProductionWindow$' ./internal/tools
        $probeExit = $LASTEXITCODE
    } finally { Pop-Location }
    if ($probeExit -ne 0) { throw "The service-account window probe failed (go test exit $probeExit)." }

    Start-Sleep -Seconds 2
    $after = Get-Running
    if ($after.Count -ne 1 -or $after[0].Id -ne $target.Id) { throw 'The disposable Agent_b did not survive the probe as the same process.' }
    $added = if (Test-Path -LiteralPath $launcherLog) { @(Get-Content -LiteralPath $launcherLog | Select-Object -Skip $before) } else { @() }
    if ($added.Count) { throw "The probe left launcher-log lines: $($added -join ' | ')" }
    Write-Host "PASS: PID $($target.Id) is still running with no new launcher-log line after the service-account probe"

    $channel = Request-AgentbGracefulStop -ProcessId $target.Id
    if (-not $target.WaitForExit(15000)) { throw 'The stop event did not stop the disposable Agent_b.' }
    $stopLine = @(Get-Content -LiteralPath $launcherLog | Select-Object -Skip $before | Where-Object { $_ -match "PID $($target.Id) stopped: asked to close" })
    if ($channel -ne 'stop event' -or -not $stopLine.Count) { throw "The graceful stop was not recorded through the stop event ($channel)." }
    Write-Host "PASS: the operator's stop event still stops it gracefully: $($stopLine[-1].Trim())"
} finally {
    Remove-Item Env:\AGENTB_WINDOW_PROBE_PID, Env:\AGENTB_CAPABILITY_WORKSPACE, Env:\AGENTB_CAPABILITY_DATA -ErrorAction SilentlyContinue
    foreach ($process in @(Get-Running)) {
        & (Join-Path $env:SystemRoot 'System32\taskkill.exe') /PID $process.Id /T /F | Out-Null
        $process.WaitForExit(15000) | Out-Null
    }
    if (Test-Path -LiteralPath $testRegistry) { Remove-Item -LiteralPath $testRegistry -Recurse -Force }
    if (Test-Path -LiteralPath $publicRoot) { Remove-TreeWithinAllowedRoots -Path $publicRoot -AllowedRoots @('C:\Users\Public') -Purpose 'window-probe credential root cleanup' }
    if (Test-Path -LiteralPath $testRoot) { Remove-TreeWithinAllowedRoots -Path $testRoot -AllowedRoots @($tempRoot) -Purpose 'window-probe disposable-root cleanup' }
}
