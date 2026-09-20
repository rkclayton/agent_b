# Item 2gl, v1.2.6/W2 — closing the window does not stop the install.
#
# The operator's complaint behind this clause: an install that dies with the
# window it was started from, and a Setup page that freezes because the thing
# writing its progress was the wrapper rather than the installer.
#
# This proves both halves on a DISPOSABLE root, in TestMode so no elevation and
# no UAC is involved and production is never touched:
#   1. start `Agent_b.exe --install` against the disposable root,
#   2. wait until the install is plainly under way,
#   3. END THE WRAPPER BY PID - which is what closing its window does,
#   4. assert the install finishes anyway and that the progress file GREW
#      AFTER the wrapper died.
param(
    [Parameter(Mandatory = $true)][string]$Exe,
    [Parameter(Mandatory = $true)][string]$SourceDirectory,
    [Parameter(Mandatory = $true)][string]$Root,
    [Parameter(Mandatory = $true)][string]$Evidence
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'removal-guard.ps1')

$application = Join-Path $Root 'Application\Agent_b'
$data = Join-Path $Root 'LocalAppData\Agent_b'
$workspace = Join-Path $Root 'ProgramData\Agent_b\workspace'
$startMenu = Join-Path $Root 'Start Menu\Programs'
$registry = 'HKCU:\Software\Agent_bDetachedProof\Agent_b'
foreach ($path in @($application, $data, $workspace, $startMenu)) { $null = New-Item -ItemType Directory -Force -Path $path }
$null = New-Item -ItemType Directory -Force -Path $Evidence
$progress = Join-Path $data 'install-progress.jsonl'

$arguments = @(
    '--install', '--quiet',
    '--install-source', $SourceDirectory,
    '--install-data', $data,
    '-SourceDirectory', $SourceDirectory,
    '-ApplicationDirectory', $application,
    '-DataDirectory', $data,
    '-WorkspaceDirectory', $workspace,
    '-StartMenuDirectory', $startMenu,
    '-UninstallRegistryPath', $registry,
    '-OperatorSid', ([Security.Principal.WindowsIdentity]::GetCurrent().User.Value),
    '-OperatorLocalAppData', $data,
    '-TestMode'
)

$result = [ordered]@{ killed_at_phase = ''; wrapper_pid = 0; lines_before_kill = 0; lines_after_kill = 0; completed = $false }
$wrapper = Start-Process -FilePath $Exe -ArgumentList ($arguments | ForEach-Object { if ($_ -match '[\s"]') { '"' + $_.Replace('"', '\"') + '"' } else { $_ } }) -PassThru -WindowStyle Hidden
$result.wrapper_pid = $wrapper.Id
Write-Output ("wrapper PID $($wrapper.Id)")

# Wait until the installer is plainly under way: the progress file exists and
# names a phase past the start.
$deadline = (Get-Date).AddSeconds(90)
while ((Get-Date) -lt $deadline) {
    if (Test-Path -LiteralPath $progress) {
        $lines = @(Get-Content -LiteralPath $progress -ErrorAction SilentlyContinue)
        if ($lines.Count -ge 2) {
            $result.lines_before_kill = $lines.Count
            $result.killed_at_phase = ($lines[-1] | ConvertFrom-Json).phase
            break
        }
    }
    Start-Sleep -Milliseconds 100
}
if (-not $result.lines_before_kill) { throw 'the install never reported progress; nothing to prove' }

# Hard stop (12): the wrapper is ended BY PID, which is what closing its window
# does to it. Nothing else is touched.
Write-Output ("ending the wrapper by PID $($wrapper.Id) during '$($result.killed_at_phase)'")
Stop-Process -Id $wrapper.Id -Force
$wrapper.WaitForExit(10000) | Out-Null

# The install must carry on and finish, and the progress must keep arriving.
$deadline = (Get-Date).AddSeconds(240)
while ((Get-Date) -lt $deadline) {
    $lines = @(Get-Content -LiteralPath $progress -ErrorAction SilentlyContinue)
    $result.lines_after_kill = $lines.Count
    if ($lines.Count) {
        $last = $lines[-1] | ConvertFrom-Json
        if ($last.done) { $result.completed = [bool]$last.ok; break }
    }
    Start-Sleep -Milliseconds 200
}

$result.installed_exe = Test-Path -LiteralPath (Join-Path $application 'Agent_b.exe')
$result | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $Evidence 'detached-install.json') -Encoding utf8
Write-Output ("progress lines: $($result.lines_before_kill) before the kill, $($result.lines_after_kill) after")
Write-Output ("install completed after the wrapper died: $($result.completed); application exe present: $($result.installed_exe)")

if (Test-Path -LiteralPath $registry) { Remove-Item -LiteralPath $registry -Recurse -Force }
if (-not $result.completed) { throw 'the install did not finish after its wrapper was ended' }
if ($result.lines_after_kill -le $result.lines_before_kill) { throw 'no progress was written after the wrapper was ended' }
Write-Output 'DETACHED INSTALL PASS'
