function Invoke-AgentBPostReleaseSweep {
    param([Parameter(Mandatory = $true)][string]$Repository, [scriptblock]$Runner)
    try {
        if ($Runner) { & $Runner | Out-Null; return }
        $output = @(& powershell.exe -NoLogo -NoProfile -NonInteractive -WindowStyle Hidden -File (Join-Path $Repository 'tools\sweep-temp.ps1') -Apply -RepositoryRoot $Repository 2>&1)
        if ($LASTEXITCODE -ne 0) { throw [string]($output | Select-Object -Last 1) }
    } catch {
        $reason = [regex]::Replace($_.Exception.Message, '\s+', ' ').Trim()
        Write-Output "SWEEP FAILED: $reason"
    }
}

function Get-StagedCandidateState {
    param([Parameter(Mandatory = $true)][string]$Candidate)
    $root = [IO.Path]::GetFullPath($Candidate)
    $manifestPath = Join-Path $root 'candidate-final.json'
    $tag = ''
    $commit = ''
    if (Test-Path -LiteralPath $manifestPath -PathType Leaf) {
        try {
            $manifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json
            $tag = [string]$manifest.tag
            $commit = [string]$manifest.commit
        } catch {}
    }
    $signatures = @()
    foreach ($name in @('Agent_b.exe', 'Agent_b-setup.exe')) {
        $path = Join-Path $root $name
        $status = if (Test-Path -LiteralPath $path -PathType Leaf) { [string](Get-AuthenticodeSignature -LiteralPath $path).Status } else { 'missing' }
        $signatures += "$name=$status"
    }
    return [pscustomobject]@{ Tag = $tag; Commit = $commit; SignedState = ($signatures -join ', ') }
}

function Assert-RetainedStagedCandidate {
    param(
        [Parameter(Mandatory = $true)][string]$Candidate,
        [Parameter(Mandatory = $true)][string]$ExpectedTag,
        [Parameter(Mandatory = $true)][string]$ExpectedCommit
    )
    $root = [IO.Path]::GetFullPath($Candidate)
    $manifestPath = Join-Path $root 'candidate-final.json'
    if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) { throw "DEPLOY REFUSED: retained candidate has no candidate-final.json: $root" }
    try { $manifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json } catch { throw "DEPLOY REFUSED: retained candidate manifest is unreadable: $($_.Exception.Message)" }
    if ([string]$manifest.tag -cne $ExpectedTag -or [string]$manifest.commit -cne $ExpectedCommit) {
        throw "DEPLOY REFUSED: retained candidate identity is tag=$($manifest.tag) commit=$($manifest.commit), expected tag=$ExpectedTag commit=$ExpectedCommit."
    }
    foreach ($entry in @(
        @{ Name = 'Agent_b.exe'; Hash = [string]$manifest.exe_sha256 },
        @{ Name = 'Agent_b-setup.exe'; Hash = [string]$manifest.setup_sha256 }
    )) {
        $path = Join-Path $root $entry.Name
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "DEPLOY REFUSED: retained candidate is missing $($entry.Name)." }
        $actual = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
        if ([string]::IsNullOrWhiteSpace($entry.Hash) -or $actual -cne $entry.Hash.ToLowerInvariant()) {
            throw "DEPLOY REFUSED: retained candidate $($entry.Name) does not match candidate-final.json."
        }
        $signature = Get-AuthenticodeSignature -LiteralPath $path
        if ($signature.Status -cne 'Valid') { throw "DEPLOY REFUSED: retained candidate $($entry.Name) signature is $($signature.Status)." }
    }
}

# Agent_b.exe is a WINDOWS_GUI executable. A direct PowerShell invocation may
# return before such a child has written redirected output, so every installer
# question uses an owned hidden process and an explicit wait. Stdout and stderr
# are drained concurrently; Output is their bounded question text for callers
# that must relay the exact installer answer.
function Invoke-AgentBInstallerQuestion {
    param(
        [Parameter(Mandatory = $true)][string]$QuestionBinary,
        [Parameter(Mandatory = $true)][string]$Installer
    )
    $binaryPath = [IO.Path]::GetFullPath($QuestionBinary)
    $installerPath = [IO.Path]::GetFullPath($Installer)
    $start = New-Object Diagnostics.ProcessStartInfo
    $start.FileName = $binaryPath
    $start.Arguments = '--inspect-installer "' + $installerPath + '"'
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.WindowStyle = [Diagnostics.ProcessWindowStyle]::Hidden
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $process = New-Object Diagnostics.Process
    $process.StartInfo = $start
    try {
        if (-not $process.Start()) { throw "installer question did not start: $binaryPath" }
        $stdoutRead = $process.StandardOutput.ReadToEndAsync()
        $stderrRead = $process.StandardError.ReadToEndAsync()
        $process.WaitForExit()
        $stdout = [string]$stdoutRead.Result
        $stderr = [string]$stderrRead.Result
        $output = (@($stdout.Trim(), $stderr.Trim()) | Where-Object { $_ }) -join [Environment]::NewLine
        return [pscustomobject]@{ ExitCode = $process.ExitCode; Stdout = $stdout; Stderr = $stderr; Output = $output.Trim() }
    } finally {
        $process.Dispose()
    }
}

function Remove-MatchingStagedCandidate {
    param(
        [Parameter(Mandatory = $true)][string]$Candidate,
        [Parameter(Mandatory = $true)][string]$CandidatesRoot,
        [Parameter(Mandatory = $true)][string]$ExpectedTag
    )
    $state = Get-StagedCandidateState -Candidate $Candidate
    $shownTag = if ($state.Tag) { $state.Tag } else { '<unknown>' }
    $shownCommit = if ($state.Commit) { $state.Commit } else { '<unknown>' }
    Write-Host "STALE CANDIDATE: path=$([IO.Path]::GetFullPath($Candidate)) tag=$shownTag commit=$shownCommit signed-state=$($state.SignedState)"
    if ($state.Tag -cne $ExpectedTag) {
        # rel-1.27.0/W4: the recovery command named THIS file, which does not declare
        # the guard function, so pasting it failed with "not recognized as a name of a
        # cmdlet". A recovery command that does not run is the dead end card 3 was
        # written to remove.
        $guardScript = [IO.Path]::GetFullPath((Join-Path (Split-Path -Parent (Split-Path -Parent $PSCommandPath)) 'scripts\removal-guard.ps1'))
        # rel-1.25.0 card 3: a deploy that fails mid-staging -- as one did at
        # rel-1.25.0 when the timestamp server timed out -- leaves a candidate
        # directory with copied sources, no manifest and no binaries. This guard
        # refuses to delete what it cannot identify, which is right, and the next
        # deploy then refuses too. Naming the recovery here is the difference
        # between a correct guard and a dead end.
        throw ("DEPLOY REFUSED: staged candidate does not identify itself as $ExpectedTag; it was retained. " +
               "If this is a partial candidate from a failed deploy (no candidate-final.json, no Agent_b.exe), remove it through the removal guard and deploy again:`n" +
               "  pwsh -NoProfile -Command "". '$guardScript'; Remove-TreeWithinAllowedRoots -Path '$Candidate' -AllowedRoots @('$CandidatesRoot') -Purpose 'partial candidate removal'""")
    }
    Remove-TreeWithinAllowedRoots -Path $Candidate -AllowedRoots @($CandidatesRoot) -Purpose "restage $ExpectedTag"
    Write-Host "STALE CANDIDATE REMOVED: $ExpectedTag"
}

function Invoke-AgentBShortReleaseRun {
    param(
        [Parameter(Mandatory = $true)][string]$CandidateDirectory,
        [Parameter(Mandatory = $true)][string]$ExpectedCommit,
        [scriptblock]$Runner
    )
    $root = Join-Path ([IO.Path]::GetTempPath()) ('Agent_b-release-short-' + [Guid]::NewGuid().ToString('N'))
    try {
        $data = Join-Path $root 'data'; $workspace = Join-Path $root 'workspace'; $evidence = Join-Path $root 'evidence'
        $null = New-Item -ItemType Directory -Path $data, $workspace
        if ($Runner) {
            $result = & $Runner $CandidateDirectory $data $workspace $evidence $ExpectedCommit
        } else {
            $arguments = @((Join-Path (Split-Path -Parent $PSScriptRoot) 'tests\chat-acceptance.mjs'), '--app', $CandidateDirectory, '--data', $data, '--workspace', $workspace, '--evidence', $evidence, '--expected-commit', $ExpectedCommit, '--expected-dirty', 'false', '--headless', 'true', '--release-short', 'true')
            $previousPreference = $ErrorActionPreference
            try {
                # Windows PowerShell wraps native stderr as an ErrorRecord. The
                # product's startup diagnostics are output, not a failed run.
                $ErrorActionPreference = 'Continue'
                $output = @(& node @arguments 2>&1); $exitCode = $LASTEXITCODE
            } finally { $ErrorActionPreference = $previousPreference }
            $result = [pscustomobject]@{ ExitCode = $exitCode; Output = ($output -join "`n") }
            $output | Write-Host
        }
        if ([int]$result.ExitCode -ne 0) { throw "SHORT RUN REFUSED: $($result.Output)" }
        return 'SHORT RELEASE RUN: passed'
    } finally {
        if (Test-Path -LiteralPath $root) { Remove-TreeWithinAllowedRoots -Path $root -AllowedRoots @([IO.Path]::GetTempPath()) -Purpose 'short release run cleanup' }
    }
}
