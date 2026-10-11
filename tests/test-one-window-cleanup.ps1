[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'
$repository = Split-Path -Parent $PSScriptRoot
. (Join-Path $repository 'scripts\removal-guard.ps1')
. (Join-Path $PSScriptRoot 'one-window-cleanup.ps1')

Add-Type -TypeDefinition @'
using System;
using System.IO;
using System.Threading;
public sealed class AgentBTimedFileLock : IDisposable {
    private FileStream stream;
    public AgentBTimedFileLock(string path, int milliseconds) {
        stream = new FileStream(path, FileMode.Open, FileAccess.ReadWrite, FileShare.None);
        var thread = new Thread(() => { Thread.Sleep(milliseconds); Dispose(); });
        thread.IsBackground = true;
        thread.Start();
    }
    public void Dispose() {
        var held = Interlocked.Exchange(ref stream, null);
        if (held != null) held.Dispose();
    }
}
'@

$temp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\')
$releasedRoot = Join-Path $temp ('Agent_b-one-window-cleanup-release-' + [Guid]::NewGuid().ToString('N'))
$heldRoot = Join-Path $temp ('Agent_b-one-window-cleanup-held-' + [Guid]::NewGuid().ToString('N'))
try {
    $null = New-Item -ItemType Directory -Path $releasedRoot
    $releasedFile = Join-Path $releasedRoot 'journal'
    [IO.File]::WriteAllText($releasedFile, 'fixture')
    $releasedLock = [AgentBTimedFileLock]::new($releasedFile, 1200)
    try {
        Wait-RemoveOneWindowRoot -Path $releasedRoot -AllowedRoot $temp -MaxWaitSeconds 4 -RetrySeconds 1
    } finally { $releasedLock.Dispose() }
    if (Test-Path -LiteralPath $releasedRoot) { throw 'A lock released within the wait was not cleaned up.' }
    Write-Host 'PASS one-window cleanup: a planted lock released inside the wait is retried and removed'

    $null = New-Item -ItemType Directory -Path $heldRoot
    $heldFile = Join-Path $heldRoot 'journal'
    [IO.File]::WriteAllText($heldFile, 'fixture')
    $heldLock = [IO.File]::Open($heldFile, [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
    try {
        $refused = $false
        try { Wait-RemoveOneWindowRoot -Path $heldRoot -AllowedRoot $temp -MaxWaitSeconds 2 -RetrySeconds 1 } catch {
            $refused = $_.Exception.Message -match 'holder\(s\): .+\(PID \d+\)'
        }
        if (-not $refused) { throw 'An unreleased planted lock did not fail naming its holder.' }
    } finally { $heldLock.Dispose() }
    Write-Host 'PASS one-window cleanup: an unreleased planted lock fails naming its holder'
} finally {
    foreach ($root in @($releasedRoot, $heldRoot)) {
        if (Test-Path -LiteralPath $root) {
            Remove-TreeWithinAllowedRoots -Path $root -AllowedRoots @($temp) -Purpose 'one-window cleanup fixture cleanup'
        }
    }
}
