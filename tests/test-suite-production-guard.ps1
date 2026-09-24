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

Write-Host 'PASS production-incarnation helper records required fields and names changed start-time/process and commit fields'
