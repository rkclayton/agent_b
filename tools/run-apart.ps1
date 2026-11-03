[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][ValidatePattern('^v\d+\.\d+\.\d+$')][string]$Tag,
    [string]$ResultPath = '',
    [ValidateSet('', 'matrix-child', 'silent')][string]$FailureDiagnosticSelfTest = ''
)

$ErrorActionPreference = 'Stop'
$repository = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$windowsPowerShell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
if ([string]::IsNullOrWhiteSpace($ResultPath)) { $ResultPath = Join-Path $repository "logs\evidence\apart-$Tag.json" }
$results = New-Object System.Collections.Generic.List[object]

function Invoke-WithReleaseCandidate {
    param([scriptblock]$Action)
    $swap = Join-Path ([IO.Path]::GetTempPath()) ('Agent_b-apart-swap-' + [Guid]::NewGuid().ToString('N'))
    $names = @('Agent_b.exe', 'agentb.exe', 'candidate-final.json')
    $present = @{}
    try {
        $null = New-Item -ItemType Directory -Path $swap
        foreach ($name in $names) {
            $target = Join-Path $repository $name; $source = Join-Path $releaseCandidate $name
            if (-not (Test-Path -LiteralPath $source -PathType Leaf)) { throw "APART REFUSED: release candidate is missing $name." }
            $present[$name] = Test-Path -LiteralPath $target -PathType Leaf
            if ($present[$name]) { Copy-Item -LiteralPath $target -Destination (Join-Path $swap $name) }
            Copy-Item -LiteralPath $source -Destination $target -Force
        }
        & $Action
    } finally {
        foreach ($name in $names) {
            $target = Join-Path $repository $name
            if ($present[$name]) { Copy-Item -LiteralPath (Join-Path $swap $name) -Destination $target -Force }
            elseif (Test-Path -LiteralPath $target -PathType Leaf) { Remove-Item -LiteralPath $target -Force }
        }
        if (Test-Path -LiteralPath $swap) { Remove-TreeWithinAllowedRoots -Path $swap -AllowedRoots @([IO.Path]::GetTempPath()) -Purpose 'apart candidate swap cleanup' }
    }
}

function Invoke-ApartArm {
    param([string]$Name, [scriptblock]$Action)
    $started = [DateTime]::UtcNow; $tail = New-Object 'System.Collections.Generic.Queue[string]'
    $addTail = {
        param([string]$Line)
        $tail.Enqueue($Line)
        if ($tail.Count -gt 200) { $null = $tail.Dequeue() }
    }
    try {
        $previousErrorActionPreference = $ErrorActionPreference
        try {
            $ErrorActionPreference = 'Continue'
            & $Action 2>&1 | ForEach-Object { $line = [string]$_; Write-Host $line; & $addTail $line }
            $exitCode = $LASTEXITCODE
        } finally {
            $ErrorActionPreference = $previousErrorActionPreference
        }
    } catch {
        $exitCode = 1
        $line = [string]$_.Exception.Message
        Write-Host $line
        & $addTail $line
    }
    if ($Name -eq 'installer-matrix' -and $exitCode -ne 0 -and -not @($tail.ToArray() | Where-Object { $_ -match '^MATRIX FAILURE:' }).Count) {
        $outputTail = @($tail.ToArray() | Select-Object -Last 20)
        $diagnostic = "MATRIX FAILURE: stage=unknown; the script exited $exitCode without a diagnostic"
        Write-Host $diagnostic
        & $addTail $diagnostic
        if (-not $outputTail.Count) { $outputTail = @('(no output)') }
        foreach ($outputLine in $outputTail) {
            $line = "MATRIX OUTPUT: $outputLine"
            Write-Host $line
            & $addTail $line
        }
    }
    $results.Add([ordered]@{ name=$Name; exit_code=$exitCode; started_at=$started.ToString('o'); duration_ms=[long]([DateTime]::UtcNow-$started).TotalMilliseconds; output_tail=@($tail.ToArray()) })
}

if (-not [string]::IsNullOrWhiteSpace($FailureDiagnosticSelfTest)) {
    Invoke-ApartArm 'installer-matrix' {
        if ($FailureDiagnosticSelfTest -eq 'matrix-child') {
            & $windowsPowerShell -NoLogo -NoProfile -NonInteractive -WindowStyle Hidden -File (Join-Path $repository 'tests\test-installer.ps1') -FailureDiagnosticChildPathSelfTest
        } else {
            & $windowsPowerShell -NoLogo -NoProfile -NonInteractive -WindowStyle Hidden -Command 'exit 1'
        }
    }
    if (@($results | Where-Object { $_.exit_code -ne 0 }).Count) { exit 1 }
    exit 0
}

$commit = [string](& git -C $repository rev-parse HEAD)
if ($LASTEXITCODE -ne 0 -or $commit.Trim() -notmatch '^[0-9a-fA-F]{40}$') { throw 'APART REFUSED: HEAD is unavailable.' }
$commit = $commit.Trim().ToLowerInvariant()
$releaseCandidate = Join-Path (Join-Path $repository 'candidates') $Tag
$candidateState = Get-Content -Raw -LiteralPath (Join-Path $releaseCandidate 'candidate-final.json') | ConvertFrom-Json
# Item 2wj: a release cut before the record was committed ahead of its tag is followed by the record
# commit alone; HEAD descended from the candidate's commit by nothing but that record is its source.
if ([string]$candidateState.commit -cne $commit -and [string]$candidateState.commit -match '^[0-9a-f]{40}$') {
    & git -C $repository merge-base --is-ancestor $candidateState.commit $commit
    $recordOnly = $LASTEXITCODE -eq 0 -and ((@(& git -C $repository diff --name-only $candidateState.commit $commit) -join '|') -ceq 'tests/compaction-proof.json')
    if ($recordOnly) { $commit = [string]$candidateState.commit }
}
if ([string]$candidateState.tag -cne $Tag -or [string]$candidateState.commit -cne $commit) {
    throw "APART REFUSED: $releaseCandidate is not the $Tag candidate for $commit."
}
. (Join-Path $repository 'scripts\removal-guard.ps1')

Invoke-ApartArm 'clock-timed-tests' {
    & (Join-Path $repository '.tools\go\bin\go.exe') test -count=1 ./cmd/harness ./internal/chatstore ./internal/tools ./internal/web -run '^(TestSixtyFourChatStartupReadsSmallStateWithinTwoSeconds2qc|TestWatchCoalescesBurstAndStillNoticesOneChange|TestCallServiceExecCredentialSuccessBearerAndCacheExpiry|TestAttachmentStorageSearchDoesNotGrowWithStoredAttachments2pt)$'
}
Invoke-ApartArm 'long-packaged-chat-run' {
    Invoke-WithReleaseCandidate {
        & $windowsPowerShell -NoLogo -NoProfile -NonInteractive -WindowStyle Hidden -File (Join-Path $repository 'tests\test-chat-journeys.ps1') -SkipBuild -ExpectedCommit $commit -ExpectedDirty false -EvidenceDirectory (Join-Path (Split-Path -Parent $ResultPath) "apart-$Tag-chat")
    }
}
Invoke-ApartArm 'installer-matrix' {
    Invoke-WithReleaseCandidate {
        & $windowsPowerShell -NoLogo -NoProfile -NonInteractive -WindowStyle Hidden -File (Join-Path $repository 'tests\test-installer.ps1') -UseExistingSignedCandidate -SkipWindowAcceptance -EvidenceDirectory (Join-Path (Split-Path -Parent $ResultPath) "apart-$Tag-installer")
    }
}

$report = [ordered]@{ schema=1; tag=$Tag; commit=$commit; created_at=[DateTime]::UtcNow.ToString('o'); results=@($results | ForEach-Object { $_ }) }
$null = New-Item -ItemType Directory -Force -Path (Split-Path -Parent $ResultPath)
[IO.File]::WriteAllText($ResultPath, ($report | ConvertTo-Json -Depth 6) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
Write-Host "APART RESULT: $ResultPath"
if (@($results | Where-Object { $_.exit_code -ne 0 }).Count) { exit 1 }
exit 0
