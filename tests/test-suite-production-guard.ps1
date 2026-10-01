$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'suite-production-guard.ps1')

$before = Get-AgentBProductionIncarnation
$after = Get-AgentBProductionIncarnation
Assert-AgentBProductionIncarnationUnchanged -Before $before -After $after -Suite 'guard-self-test'

$launched = $false
try {
    Assert-AgentBSuiteLaunch -ApplicationRoot (Join-Path $env:LOCALAPPDATA 'Programs\Agent_b') `
        -DataRoot (Join-Path $env:LOCALAPPDATA 'Agent_b') -SuiteRoots @([IO.Path]::GetTempPath()) `
        -Action { $script:launched = $true }
    throw 'suite launch guard accepted the canonical production roots'
} catch {
    if ($_.Exception.Message -notmatch 'SUITE LAUNCH REFUSED.*canonical') { throw }
}
if ($launched) { throw 'suite launch guard started the planted canonical process' }

$coverageCaught = $false
try { Assert-AgentBSuiteLaunchCoverage -Text 'Start-Process -FilePath $setup -ArgumentList $args' -Label 'planted bypass' }
catch { if ($_.Exception.Message -match 'SUITE LAUNCH COVERAGE REFUSED') { $coverageCaught = $true } else { throw } }
if (-not $coverageCaught) { throw 'suite launch source coverage accepted a planted bypass' }

foreach ($name in @('test-installer.ps1', 'test-updater-cycle.ps1')) {
    Assert-AgentBSuiteLaunchCoverage -Text (Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot $name)) -Label $name
}

$changed = [ordered]@{}
foreach ($entry in $before.GetEnumerator()) { $changed[$entry.Key] = $entry.Value }
$changed.processes = @([ordered]@{ pid = 123; start_time_utc = '2026-09-23T00:00:00.0000000Z'; executable_path = 'C:\fake\Agent_b.exe' })
$changed.api_commit = 'deliberately-changed-commit'
try {
    Assert-AgentBProductionIncarnationUnchanged -Before $before -After $changed -Suite 'deliberate-change'
    throw 'guard accepted a changed process incarnation'
} catch {
    if ($_.Exception.Message -notmatch 'PRODUCTION INCARNATION CHANGED.*processes.*api_commit') { throw }
}

$mtimeOnly = [ordered]@{}
foreach ($entry in $before.GetEnumerator()) { $mtimeOnly[$entry.Key] = $entry.Value }
$mtimeOnly.config = [ordered]@{}
foreach ($entry in $before.config.GetEnumerator()) { $mtimeOnly.config[$entry.Key] = $entry.Value }
$mtimeOnly.config.mtime_utc = '2099-01-01T00:00:00.0000000Z'
Assert-AgentBProductionIncarnationUnchanged -Before $before -After $mtimeOnly -Suite 'identical-config-rewrite'

$contentChanged = [ordered]@{}
foreach ($entry in $before.GetEnumerator()) { $contentChanged[$entry.Key] = $entry.Value }
$contentChanged.config = [ordered]@{}
foreach ($entry in $before.config.GetEnumerator()) { $contentChanged.config[$entry.Key] = $entry.Value }
$contentChanged.config.sha256 = 'deliberately-changed-config-content'
try {
    Assert-AgentBProductionIncarnationUnchanged -Before $before -After $contentChanged -Suite 'changed-config-content'
    throw 'guard accepted changed config content'
} catch {
    if ($_.Exception.Message -notmatch 'PRODUCTION INCARNATION CHANGED.*config') { throw }
}

Write-Host 'PASS production-incarnation helper ignores config mtime alone and names changed process, commit, and config content'
