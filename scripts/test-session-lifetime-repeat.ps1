# Item 2hg (v1.3.0/W6): the session-lifetime scenario, twenty times in a row.
#
# The gate this proves used to pass on one run and fail the next against a
# byte-identical binary, and a release was withheld on it. The cause is named in
# the report and fixed in two places: the launcher no longer gives a background
# server a console window (so taskkill's WM_CLOSE has no console to close and
# turn into CTRL_CLOSE_EVENT, and then SIGTERM), and watchSessionEnd now returns
# only once its guard window exists (so "listening" implies "guarded").
#
# There is NO retry here by design. Each run is counted as it lands; the first
# failure stops the loop and keeps its evidence. A gate that passes on the third
# attempt has not passed.
[CmdletBinding()]
param(
    [int]$Runs = 20,
    [string]$TestRoot
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'removal-guard.ps1')
$repositoryRoot = Split-Path -Parent $PSScriptRoot

function Get-WindowsPowerShellPath {
    return (Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe')
}

function Get-FreeLoopbackPort {
    # Ask the OS rather than guessing: a random high port lands in WinNAT's
    # reserved ranges often enough to look like a failure of its own.
    $probe = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $probe.Start()
    try { return $probe.LocalEndpoint.Port } finally { $probe.Stop() }
}

if (-not $TestRoot) {
    $TestRoot = Join-Path ([IO.Path]::GetTempPath()) ("Agent_b-lifetime-repeat-" + [Guid]::NewGuid().ToString('N'))
}
# The installer refuses any application/workspace path whose leaf is not
# 'Agent_b' or 'workspace' (Assert-SafeAgentBPath), so the disposable tree uses
# the same names production does, three disjoint roots under one temp folder.
$application = Join-Path (Join-Path $TestRoot 'Program') 'Agent_b'
$data = Join-Path (Join-Path $TestRoot 'Data') 'Agent_b'
$workspace = Join-Path (Join-Path $TestRoot 'Shared') 'workspace'
$startMenu = Join-Path $TestRoot 'StartMenu'
$registry = 'HKCU:\Software\Agent_b-lifetime-repeat'

Write-Host "Building the candidate from the working tree..."
& (Get-WindowsPowerShellPath) -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'build-candidate.ps1') -SourceDirectory $repositoryRoot -SignForTest | Out-Null
if ($LASTEXITCODE -ne 0) { throw "build-candidate exited $LASTEXITCODE." }

Write-Host "Installing a disposable copy at $application ..."
& (Get-WindowsPowerShellPath) -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'install-Agent_b.ps1') `
    -SourceDirectory $repositoryRoot -ApplicationDirectory $application -DataDirectory $data `
    -WorkspaceDirectory $workspace -StartMenuDirectory $startMenu -UninstallRegistryPath $registry -TestMode | Out-Null
if ($LASTEXITCODE -ne 0) { throw "The disposable install exited $LASTEXITCODE." }

# The disposable copy must never wear production's port (item 2gu).
$port = Get-FreeLoopbackPort
$configPath = Join-Path $data 'harness.json'
$config = Get-Content -Raw -LiteralPath $configPath | ConvertFrom-Json
$config.listen = "127.0.0.1:$port"
$config | ConvertTo-Json -Depth 32 | Set-Content -LiteralPath $configPath -Encoding utf8
if ($port -eq 8790) { throw 'Refusing to run a disposable scenario on production''s port.' }
Write-Host "Disposable listen 127.0.0.1:$port"

$results = @()
$failedAt = 0
for ($run = 1; $run -le $Runs; $run++) {
    $started = Get-Date
    $output = ''
    $ok = $true
    try {
        $output = & (Join-Path $PSScriptRoot 'test-session-lifetime.ps1') `
            -ApplicationDirectory $application -DataDirectory $data -StartMenuDirectory $startMenu -Port $port *>&1 | Out-String
    } catch {
        $ok = $false
        $output = ($_ | Out-String)
    }
    # *>&1 above, not 2>&1: the scenario reports with Write-Host, which is the
    # information stream, so a narrower redirection captures nothing and every
    # run would read as a failure.
    if ($ok -and $output -notmatch 'ignored WM_CLOSE') { $ok = $false }
    $seconds = [math]::Round(((Get-Date) - $started).TotalSeconds, 1)
    $results += [pscustomobject]@{ run = $run; ok = $ok; seconds = $seconds }
    Write-Host ("run {0,2}/{1}: {2} in {3,5} s" -f $run, $Runs, $(if ($ok) { 'PASS' } else { 'FAIL' }), $seconds)
    if (-not $ok) {
        $failedAt = $run
        Write-Host "KEPT for evidence: $TestRoot"
        Write-Host $output
        break
    }
}

$passed = @($results | Where-Object ok).Count
Write-Host ''
Write-Host ("SESSION LIFETIME: {0}/{1} consecutive passes" -f $passed, $Runs)
if ($results.Count) {
    $times = @($results | ForEach-Object { $_.seconds })
    Write-Host ("per-run seconds: min {0}, max {1}" -f ($times | Measure-Object -Minimum).Minimum, ($times | Measure-Object -Maximum).Maximum)
}

if ($failedAt -eq 0) {
    if (Test-Path -LiteralPath $registry) { Remove-Item -LiteralPath $registry -Recurse -Force -ErrorAction SilentlyContinue }
    # The last run's server has just been asked to stop; give it a moment to
    # let go of its data directory before the tree is removed. Without this the
    # removal raced it and a twenty-for-twenty PASS exited 1 on cleanup, which
    # would have read as a failed gate.
    $deadline = (Get-Date).AddSeconds(30)
    while ((Get-Date) -lt $deadline) {
        $holding = @(Get-Process -Name 'Agent_b' -ErrorAction SilentlyContinue | Where-Object {
            try { $_.Path -and $_.Path.StartsWith($TestRoot, [StringComparison]::OrdinalIgnoreCase) } catch { $false }
        })
        if (-not $holding.Count) { break }
        Start-Sleep -Milliseconds 500
    }
    try {
        Remove-TreeWithinAllowedRoots -Path $TestRoot -AllowedRoots @([IO.Path]::GetTempPath()) -Purpose 'session-lifetime repeat cleanup'
        Write-Host 'Disposable root removed.'
    } catch {
        # The PROOF is the twenty runs. A directory that will not delete is
        # untidiness, and it is said out loud rather than turned into a verdict.
        Write-Host "NOT REMOVED (the proof still stands): $TestRoot - $($_.Exception.Message)"
    }
    exit 0
}
exit 1
