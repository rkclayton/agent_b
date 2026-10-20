[CmdletBinding()]
param(
    [string[]]$Journeys = @(),
    [string[]]$RequiredJourneys = @(),
    [string]$PlantFailureJourney = '',
    [string]$EvidenceDirectory = '',
    [switch]$SkipBuild,
    [string]$ExpectedCommit = '',
    [ValidateSet('true', 'false')][string]$ExpectedDirty = 'false'
)

$ErrorActionPreference = 'Stop'
$repository = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
. (Join-Path $repository 'scripts\windows-tools.ps1')
$decodedManifest = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'acceptance\journeys.json') | ConvertFrom-Json
# Windows PowerShell 5.1 preserves ConvertFrom-Json's top-level array as one
# pipeline object; enumerate it once so the thirteen journey objects stay thirteen.
$manifest = @($decodedManifest | ForEach-Object { $_ })
$known = @($manifest | ForEach-Object name)
if (-not $Journeys.Count) { $Journeys = $known }
foreach ($name in @($Journeys + $RequiredJourneys)) { if ($name -notin $known) { throw "Unknown acceptance journey: $name" } }
if ([string]::IsNullOrWhiteSpace($EvidenceDirectory)) { $EvidenceDirectory = Join-Path $repository ('logs\evidence\journeys-' + [Guid]::NewGuid().ToString('N')) }
if (Test-Path -LiteralPath $EvidenceDirectory) { throw "EvidenceDirectory already exists: $EvidenceDirectory" }
$null = New-Item -ItemType Directory -Path $EvidenceDirectory
if ([string]::IsNullOrWhiteSpace($ExpectedCommit)) { $ExpectedCommit = [string](& git -C $repository rev-parse HEAD); $ExpectedCommit = $ExpectedCommit.Trim() }
$windowsPowerShell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
if (-not $SkipBuild) {
    & $windowsPowerShell -NoLogo -NoProfile -NonInteractive -WindowStyle Hidden -File (Join-Path $repository 'tools\build-candidate.ps1') -SourceDirectory $repository -SignForTest -TestSignatureWithoutTimestamp
    if ($LASTEXITCODE -ne 0) { throw "Acceptance build failed with exit code $LASTEXITCODE." }
}

$results = New-Object System.Collections.Generic.List[object]
foreach ($name in $Journeys) {
    $definition = $manifest | Where-Object name -eq $name
    $attempts = New-Object System.Collections.Generic.List[object]
    for ($attempt = 1; $attempt -le 2; $attempt++) {
        $started = [DateTime]::UtcNow
        $journeyEvidence = Join-Path $EvidenceDirectory ("{0}-attempt-{1}" -f $name, $attempt)
        $stdout = Join-Path $EvidenceDirectory ("{0}-attempt-{1}.stdout.log" -f $name, $attempt)
        $stderr = Join-Path $EvidenceDirectory ("{0}-attempt-{1}.stderr.log" -f $name, $attempt)
        if ($attempt -eq 1 -and $name -ceq $PlantFailureJourney) {
            [IO.File]::WriteAllText($stderr, 'planted journey failure', [Text.UTF8Encoding]::new($false)); $exitCode = 23
        } else {
            $arguments = @('-NoLogo','-NoProfile','-NonInteractive','-WindowStyle','Hidden','-File',(Join-Path $PSScriptRoot 'test-chat-acceptance.ps1'),'-SkipBuild','-ExpectedCommit',$ExpectedCommit,'-ExpectedDirty',$ExpectedDirty,'-Headless','true','-EvidenceDirectory',$journeyEvidence)
            $arguments += @('-Journey', $name)
            $savedModulePath = $env:PSModulePath
            try {
                if ($PSVersionTable.PSEdition -eq 'Core') {
                    $env:PSModulePath = @(
                        [Environment]::GetEnvironmentVariable('PSModulePath', 'User')
                        [Environment]::GetEnvironmentVariable('PSModulePath', 'Machine')
                    ) -join ';'
                }
                $child = Start-Quiet -FilePath $windowsPowerShell -ArgumentList $arguments -StandardOutput $stdout -StandardError $stderr
                $child.WaitForExit()
                $child.Refresh()
            } finally {
                $env:PSModulePath = $savedModulePath
            }
            $exitCode = [int]$child.ExitCode
            $nativeOutput = if (Test-Path -LiteralPath $stdout) { Get-Content -Raw -LiteralPath $stdout } else { '' }
            if ($exitCode -eq 0 -and $nativeOutput -notmatch "(?m)^JOURNEY PASS $([regex]::Escape($name)) [0-9]+ ms\r?$") {
                $exitCode = 24
                Add-Content -LiteralPath $stderr -Value "native terminal missing: JOURNEY PASS $name <milliseconds> ms"
            }
        }
        $attempts.Add([ordered]@{ attempt=$attempt; exit_code=$exitCode; duration_ms=[long]([DateTime]::UtcNow-$started).TotalMilliseconds; stderr=$stderr; evidence=$journeyEvidence })
        if ($exitCode -eq 0) { break }
    }
    $passed = $attempts[$attempts.Count - 1].exit_code -eq 0
    $status = if ($passed -and $attempts.Count -eq 2) { 'flaky' } elseif ($passed) { 'passed' } elseif ($name -in $RequiredJourneys) { 'failed-required' } else { 'known-failure' }
    $firstFailure = if ($attempts.Count -eq 2) {
        $failureLines = @(Get-Content -LiteralPath $attempts[0].stderr -ErrorAction SilentlyContinue)
        [string]($failureLines | Where-Object { $_ -match '(?i)(assertionerror|error:|fail(?:ed|ure)|timeout|exception)' } | Select-Object -First 1)
    } else { '' }
    $results.Add([ordered]@{ name=$name; required=$name -in $RequiredJourneys; status=$status; first_failure=$firstFailure; attempts=$attempts.ToArray(); steps=@($definition.steps) })
    $failureNote = if ($firstFailure) { "; first failure: $firstFailure" } else { '' }
    Write-Host "JOURNEY $name $status $($attempts[-1].duration_ms) ms$failureNote"
}
$report = [ordered]@{ schema=1; commit=$ExpectedCommit; built_once=$true; test_signature_timestamped=$false; results=$results.ToArray() }
[IO.File]::WriteAllText((Join-Path $EvidenceDirectory 'journeys.json'), ($report | ConvertTo-Json -Depth 8) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
if (@($results | Where-Object status -eq 'failed-required').Count) { exit 1 }
exit 0
