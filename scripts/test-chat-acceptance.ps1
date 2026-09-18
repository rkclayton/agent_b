[CmdletBinding()]
param(
    [switch]$RealModel,
    [string]$RealModelUrl,
    [string]$RealModelName,
    [string]$ReplayPath,
    [string]$ReplayApplicationDirectory,
    [string]$EvidenceDirectory,
    [string]$ExpectedCommit,
    [string]$ExpectedDirty,
    [switch]$SkipBuild,
    [switch]$ReplayOnly,
    [switch]$ExpectStableShell,
    [ValidateSet('true', 'false')]
    [string]$Headless = 'true'
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'removal-guard.ps1')
$sourceRoot = Split-Path -Parent $PSScriptRoot
$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('Agent_b-chat-acceptance-' + [Guid]::NewGuid().ToString('N'))
$application = Join-Path $testRoot 'Application\Agent_b'
$data = Join-Path $testRoot 'LocalAppData\Agent_b'
$workspace = Join-Path $testRoot 'ProgramData\Agent_b\workspace'
$startMenu = Join-Path $testRoot 'Start Menu\Programs'
$registry = 'HKCU:\Software\Agent_bChatAcceptance-' + [Guid]::NewGuid().ToString('N') + '\Agent_b'
$evidence = if (-not [string]::IsNullOrWhiteSpace($EvidenceDirectory)) {
    $EvidenceDirectory
} elseif (-not [string]::IsNullOrWhiteSpace($env:AGENTB_CHAT_ACCEPTANCE_EVIDENCE)) {
    $env:AGENTB_CHAT_ACCEPTANCE_EVIDENCE
} else {
    Join-Path $sourceRoot ('logs\evidence\2026-09-09-v0.18.0-playwright\candidate-' + [Guid]::NewGuid().ToString('N'))
}
$expectedCommit = $ExpectedCommit
if ([string]::IsNullOrWhiteSpace($expectedCommit)) {
    $expectedCommit = [string](& git -C $sourceRoot rev-parse HEAD 2>$null | Select-Object -First 1)
    if ([string]::IsNullOrWhiteSpace($expectedCommit)) { throw 'Could not resolve the source commit. Pass -ExpectedCommit when the source is not a git checkout.' }
    $expectedCommit = $expectedCommit.Trim()
}
$expectedDirty = $ExpectedDirty
if (-not [string]::IsNullOrWhiteSpace($expectedDirty) -and $expectedDirty -notin @('true', 'false')) {
    throw '-ExpectedDirty must be true or false when supplied.'
}
if ([string]::IsNullOrWhiteSpace($expectedDirty)) {
    $expectedDirty = if (@(& git -C $sourceRoot status --porcelain --untracked-files=normal).Count -gt 0) { 'true' } else { 'false' }
    if ($LASTEXITCODE -ne 0) { throw 'Could not resolve the source dirty state.' }
}

if (Test-Path -LiteralPath $evidence) {
    throw "EvidenceDirectory already exists: $evidence"
}

# Item 2er: each run proves it starts from nothing it did not create. The
# disposable root (and the Edge profile inside it) must not exist yet, and the
# host's CPU load is recorded beside the run's evidence for as long as it runs.
if (Test-Path -LiteralPath $testRoot) { throw "Disposable root already exists: $testRoot" }
$hostLoadPath = Join-Path $evidence 'host-load.json'
$hostLoad = Start-Job -ScriptBlock {
    param($path)
    $samples = New-Object System.Collections.Generic.List[object]
    while ($true) {
        try { $value = [Math]::Round((Get-Counter '\Processor(_Total)\% Processor Time' -SampleInterval 2 -MaxSamples 1).CounterSamples[0].CookedValue, 1) } catch { $value = $null }
        $samples.Add([ordered]@{ at = (Get-Date).ToString('o'); cpu_percent = $value })
        $null = New-Item -ItemType Directory -Force -Path (Split-Path -Parent $path)
        [IO.File]::WriteAllText($path, ($samples | ConvertTo-Json -Depth 3))
    }
} -ArgumentList $hostLoadPath

try {
    $installArguments = @{
        SourceDirectory = $sourceRoot
        ApplicationDirectory = $application
        DataDirectory = $data
        WorkspaceDirectory = $workspace
        StartMenuDirectory = $startMenu
        UninstallRegistryPath = $registry
        TestMode = $true
    }
    # The installer never builds (item 2eu). Without -SkipBuild the release
    # step's build runs here; with it, the source must already hold the exe and
    # its candidate-final.json.
    if (-not $SkipBuild) {
        & powershell.exe -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'build-candidate.ps1') -SourceDirectory $sourceRoot
        if ($LASTEXITCODE -ne 0) { throw "Candidate build failed with exit code $LASTEXITCODE." }
    }
    & (Join-Path $PSScriptRoot 'install-Agent_b.ps1') @installArguments
    if ($null -ne $LASTEXITCODE -and $LASTEXITCODE -ne 0) { throw "Disposable install failed with exit code $LASTEXITCODE." }

    if (-not $ReplayOnly) {
        $arguments = @(
            (Join-Path $PSScriptRoot 'chat-acceptance.mjs'),
            '--app', $application,
            '--data', $data,
            '--workspace', $workspace,
            '--evidence', $evidence,
            '--expected-commit', $expectedCommit,
            '--expected-dirty', $expectedDirty,
            '--headless', $Headless
        )
        if ($RealModel) {
            if ([string]::IsNullOrWhiteSpace($RealModelUrl) -or [string]::IsNullOrWhiteSpace($RealModelName)) {
                throw '-RealModel requires -RealModelUrl and -RealModelName.'
            }
            $arguments += @('--real-model-url', $RealModelUrl, '--real-model-name', $RealModelName)
        }
        & (Get-Command node.exe -ErrorAction Stop).Source @arguments
        if ($LASTEXITCODE -ne 0) { throw "Chat acceptance failed with exit code $LASTEXITCODE." }
    }
    if ($ReplayOnly -and [string]::IsNullOrWhiteSpace($ReplayPath)) { throw '-ReplayOnly requires -ReplayPath.' }
    if (-not [string]::IsNullOrWhiteSpace($ReplayPath)) {
        $replayArguments = @(
            (Join-Path $PSScriptRoot 'chat-replay-acceptance.mjs'),
            '--app', $(if ([string]::IsNullOrWhiteSpace($ReplayApplicationDirectory)) { $application } else { $ReplayApplicationDirectory }),
            '--data', $data,
            '--replay', $ReplayPath,
            '--evidence', $evidence
        )
        if ($ExpectStableShell) { $replayArguments += @('--expect-stable-shell', 'true') }
        & (Get-Command node.exe -ErrorAction Stop).Source @replayArguments
        if ($LASTEXITCODE -ne 0) { throw "Chat replay acceptance failed with exit code $LASTEXITCODE." }
    }
} finally {
    Stop-Job $hostLoad -ErrorAction SilentlyContinue
    Remove-Job $hostLoad -Force -ErrorAction SilentlyContinue
    if (Test-Path -LiteralPath $registry) { Remove-Item -LiteralPath $registry -Recurse -Force }
    # Item 2er: removal raced the disposable application's exit and left the root
    # behind on a loaded host; wait for every process that holds it to end: those
    # started from the root, and Edge, whose profile is inside the root.
    $rootPrefix = [IO.Path]::GetFullPath($testRoot) + '\'
    $holders = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {
        ($_.ExecutablePath -and $_.ExecutablePath.StartsWith($rootPrefix, [StringComparison]::OrdinalIgnoreCase)) -or
        ($_.CommandLine -and $_.CommandLine.IndexOf($rootPrefix.TrimEnd('\'), [StringComparison]::OrdinalIgnoreCase) -ge 0)
    } | ForEach-Object { Get-Process -Id $_.ProcessId -ErrorAction SilentlyContinue })
    foreach ($process in $holders) {
        if (-not $process.WaitForExit(20000)) { Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue; $null = $process.WaitForExit(10000) }
    }
    $resolvedTemp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
    $resolvedTest = [IO.Path]::GetFullPath($testRoot)
    if ($resolvedTest.StartsWith($resolvedTemp, [StringComparison]::OrdinalIgnoreCase) -and
        (Split-Path -Leaf $resolvedTest) -like 'Agent_b-chat-acceptance-*' -and
        (Test-Path -LiteralPath $resolvedTest)) {
        Remove-TreeWithinAllowedRoots -Path $resolvedTest -AllowedRoots @($resolvedTemp) -Purpose 'chat-acceptance disposable-root cleanup'
    }
    if (Test-Path -LiteralPath $testRoot) { Write-Warning "Disposable root was not removed: $testRoot" }
    elseif (Test-Path -LiteralPath $evidence) { Set-Content -LiteralPath (Join-Path $evidence 'root-freshness.txt') -Value "root $testRoot did not exist before the run and was removed after it, with its Edge profile" }
}
