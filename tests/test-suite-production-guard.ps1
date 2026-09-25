$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'suite-production-guard.ps1')

$before = Get-AgentBProductionIncarnation
$after = Get-AgentBProductionIncarnation
Assert-AgentBProductionIncarnationUnchanged -Before $before -After $after -Suite 'guard-self-test'

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
