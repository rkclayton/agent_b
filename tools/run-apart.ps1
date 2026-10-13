[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][ValidatePattern('^v\d+\.\d+\.\d+$')][string]$Tag,
    [string]$ResultPath = ''
)

$ErrorActionPreference = 'Stop'
$repository = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$commit = [string](& git -C $repository rev-parse HEAD)
if ($LASTEXITCODE -ne 0 -or $commit.Trim() -notmatch '^[0-9a-fA-F]{40}$') { throw 'APART REFUSED: HEAD is unavailable.' }
$commit = $commit.Trim().ToLowerInvariant()
$windowsPowerShell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
if ([string]::IsNullOrWhiteSpace($ResultPath)) { $ResultPath = Join-Path $repository "logs\evidence\apart-$Tag.json" }
$results = New-Object System.Collections.Generic.List[object]

function Invoke-ApartArm {
    param([string]$Name, [scriptblock]$Action)
    $started = [DateTime]::UtcNow; $tail = New-Object 'System.Collections.Generic.Queue[string]'
    try {
        & $Action 2>&1 | ForEach-Object { $line = [string]$_; Write-Host $line; $tail.Enqueue($line); if ($tail.Count -gt 200) { $null = $tail.Dequeue() } }
        $exitCode = $LASTEXITCODE
    } catch { $exitCode = 1; $tail.Enqueue($_.Exception.Message); Write-Host $_.Exception.Message }
    $results.Add([ordered]@{ name=$Name; exit_code=$exitCode; started_at=$started.ToString('o'); duration_ms=[long]([DateTime]::UtcNow-$started).TotalMilliseconds; output_tail=@($tail) })
}

Invoke-ApartArm 'clock-timed-tests' {
    & (Join-Path $repository '.tools\go\bin\go.exe') test -count=1 ./cmd/harness ./internal/chatstore ./internal/tools ./internal/web -run '^(TestSixtyFourChatStartupReadsSmallStateWithinTwoSeconds2qc|TestWatchCoalescesBurstAndStillNoticesOneChange|TestCallServiceExecCredentialSuccessBearerAndCacheExpiry|TestAttachmentStorageSearchDoesNotGrowWithStoredAttachments2pt)$'
}
Invoke-ApartArm 'long-packaged-chat-run' {
    & $windowsPowerShell -NoLogo -NoProfile -NonInteractive -WindowStyle Hidden -File (Join-Path $repository 'tests\test-chat-acceptance.ps1') -SkipBuild -ExpectedCommit $commit -ExpectedDirty false -Headless true -EvidenceDirectory (Join-Path (Split-Path -Parent $ResultPath) "apart-$Tag-chat")
}
Invoke-ApartArm 'installer-matrix' {
    & $windowsPowerShell -NoLogo -NoProfile -NonInteractive -WindowStyle Hidden -File (Join-Path $repository 'tests\test-installer.ps1') -UseExistingSignedCandidate -SkipWindowAcceptance -EvidenceDirectory (Join-Path (Split-Path -Parent $ResultPath) "apart-$Tag-installer")
}

$report = [ordered]@{ schema=1; tag=$Tag; commit=$commit; created_at=[DateTime]::UtcNow.ToString('o'); results=@($results) }
$null = New-Item -ItemType Directory -Force -Path (Split-Path -Parent $ResultPath)
[IO.File]::WriteAllText($ResultPath, ($report | ConvertTo-Json -Depth 6) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
Write-Host "APART RESULT: $ResultPath"
if (@($results | Where-Object { $_.exit_code -ne 0 }).Count) { exit 1 }
exit 0
