[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][ValidatePattern('^v\d+\.\d+\.\d+$')][string]$Tag,
    [string]$SigningThumbprint = '',
    [switch]$ResumeStagedCandidate
)

$ErrorActionPreference = 'Stop'
$releaseSteps = @('hosted-ci-green', 'privacy-scan', 'update-source', 'build-sign-release-key', 'installer-key-question', 'signed-build-short-run', 'publish', 'public-installer-answer')
$releaseClock = [Diagnostics.Stopwatch]::StartNew()
$releaseStepStarted = 0L
function Complete-ReleaseStep([string]$Name) {
    $finished = $releaseClock.ElapsedMilliseconds
    Write-Host "RELEASE STEP: $Name $($finished - $script:releaseStepStarted) ms"
    $script:releaseStepStarted = $finished
}
$repository = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
& (Join-Path $repository 'tools\owned-check.ps1')
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
$candidate = Join-Path (Join-Path $repository 'candidates') $Tag
$notesPath = Join-Path (Join-Path $repository 'release-notes') "$Tag.md"
$windowsPowerShell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
. (Join-Path $repository 'scripts\removal-guard.ps1')
. (Join-Path $PSScriptRoot 'deploy-candidate-state.ps1')
$gh = Get-Command gh.exe -ErrorAction SilentlyContinue
if (-not $gh) { throw 'DEPLOY REFUSED: GitHub CLI is unavailable; hosted CI cannot be proved.' }
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if ($principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'DEPLOY REFUSED: run deploy-release.ps1 from an ordinary, non-elevated console; it requests elevation once for signing only.'
}
Write-Host "DEPLOY PARENT: elevated=false; staging, verification, and publication stay at this token"
$commitOutput = @(& git -C $repository rev-parse "$Tag^{commit}" 2>&1)
$commitExit = $LASTEXITCODE
$commit = [string]($commitOutput | Select-Object -First 1)
if ($commitExit -ne 0 -or $commit.Trim() -notmatch '^[0-9a-fA-F]{40}$') { throw "DEPLOY REFUSED: tag $Tag does not resolve to a commit." }
$commit = $commit.Trim().ToLowerInvariant()
$ciOutput = @(& $gh.Source run list --commit $commit --workflow unit --limit 20 --json headSha,status,conclusion 2>&1)
if ($LASTEXITCODE -ne 0) { throw "DEPLOY REFUSED: hosted CI lookup exited $LASTEXITCODE." }
$ciRuns = @((($ciOutput -join "`n") | ConvertFrom-Json))
if (@($ciRuns | Where-Object { $_.headSha -ceq $commit -and $_.status -ceq 'completed' -and $_.conclusion -ceq 'success' }).Count -eq 0) {
    throw "DEPLOY REFUSED: hosted CI is not green on $commit."
}
Write-Host "HOSTED CI: green on $commit"
Complete-ReleaseStep 'hosted-ci-green'
if (-not (Test-Path -LiteralPath $notesPath -PathType Leaf)) { throw "DEPLOY REFUSED: release notes are missing: $notesPath" }
& node (Join-Path $repository 'tools\check-release-notes.mjs') $notesPath
if ($LASTEXITCODE -ne 0) { throw "DEPLOY REFUSED: the release notes do not pass the notes gate; see the lines above." }
$agentBHome = [Environment]::GetEnvironmentVariable('AGENTB_HOME', 'User')
if ([string]::IsNullOrWhiteSpace($agentBHome)) { $agentBHome = Join-Path $env:USERPROFILE '.agentb' }
$env:AGENTB_CLIENT_TERMS = Join-Path $agentBHome 'client-terms.txt'
& node (Join-Path $repository 'tools\check-client-terms.mjs') --releases $notesPath
if ($LASTEXITCODE -ne 0) { throw "DEPLOY REFUSED: the client-terms gate did not pass (exit $LASTEXITCODE); see the lines above." }
& node (Join-Path $repository 'tools\privacy-gate.mjs')
if ($LASTEXITCODE -ne 0) { throw "DEPLOY REFUSED: the privacy scan did not pass (exit $LASTEXITCODE); see the lines above." }
Complete-ReleaseStep 'privacy-scan'
if ($ResumeStagedCandidate) {
    Assert-RetainedStagedCandidate -Candidate $candidate -ExpectedTag $Tag -ExpectedCommit $commit
    Write-Host "RESUMED CANDIDATE: retained signed $Tag at $commit; staging, signing, and completed gates were not repeated"
} else {
if ($SigningThumbprint -notmatch '^[0-9A-Fa-f ]+$') { throw 'DEPLOY REFUSED: a signing thumbprint is required for a new candidate.' }
$headOutput = @(& git -C $repository rev-parse HEAD 2>&1)
$headExit = $LASTEXITCODE
$head = [string]($headOutput | Select-Object -First 1)
if ($headExit -ne 0 -or $head.Trim().ToLowerInvariant() -cne $commit) { throw "DEPLOY REFUSED: tag $Tag does not point at HEAD." }
$statusOutput = @(& git -C $repository status --porcelain --untracked-files=normal 2>&1)
$statusExit = $LASTEXITCODE
if ($statusExit -ne 0) { throw "DEPLOY REFUSED: git status exited $statusExit." }
if ($statusOutput.Count) { throw 'DEPLOY REFUSED: the repository is dirty.' }

& node (Join-Path $repository 'tools\check-invariants.mjs') --release-source $Tag
if ($LASTEXITCODE -ne 0) { throw "DEPLOY REFUSED: the candidate's update source is not usable." }
Complete-ReleaseStep 'update-source'

if (Test-Path -LiteralPath $candidate) {
    Remove-MatchingStagedCandidate -Candidate $candidate -CandidatesRoot (Join-Path $repository 'candidates') -ExpectedTag $Tag
}

& node (Join-Path $repository 'tools\stage-candidate.mjs') --tag $Tag
if ($LASTEXITCODE -ne 0) { throw "DEPLOY REFUSED: candidate staging exited $LASTEXITCODE." }

$signingScript = Join-Path $repository 'tools\sign-release.ps1'
$payloadSigningReport = Join-Path $candidate 'payload-signing-report.json'
& $windowsPowerShell -NoLogo -NoProfile -ExecutionPolicy Bypass -File $signingScript -Path $candidate -Thumbprint $SigningThumbprint -ReportPath $payloadSigningReport -PayloadOnly
$payloadSigningExit = $LASTEXITCODE
if ($payloadSigningExit -ne 0) { throw "DEPLOY REFUSED: payload signing is not available to this ordinary console; run the recorded one-time key grant first." }

# 2ki: capture signed payload first; sign the new executables last.
& node (Join-Path $repository 'tools\stage-candidate.mjs') --build $Tag --thumbprint $SigningThumbprint
if ($LASTEXITCODE -ne 0) { throw "DEPLOY REFUSED: candidate build exited $LASTEXITCODE." }

$signingReport = Join-Path $candidate 'signing-report.json'
& $windowsPowerShell -NoLogo -NoProfile -ExecutionPolicy Bypass -File $signingScript -Path $candidate -Thumbprint $SigningThumbprint -ReportPath $signingReport -BinaryOnly
$signingExit = $LASTEXITCODE
if (Test-Path -LiteralPath $signingReport -PathType Leaf) {
    $signingResult = Get-Content -Raw -LiteralPath $signingReport | ConvertFrom-Json
    Write-Host "SIGNING CHILD: identity=$($signingResult.identity) elevated=$($signingResult.elevated) outcome=$($signingResult.outcome) signed=$($signingResult.signed)/$($signingResult.signable)"
    if ($signingResult.reason) { Write-Host "SIGNING CHILD REASON: $($signingResult.reason)" }
}
if ($signingExit -ne 0) { throw "DEPLOY REFUSED: release signing is not available to this ordinary console; run the recorded one-time key grant first." }

& $windowsPowerShell -NoLogo -NoProfile -ExecutionPolicy Bypass -File (Join-Path $repository 'tools\verify-deploy-candidate.ps1') -CandidateDirectory $candidate -ExpectedTag $Tag -ExpectedCommit $commit
if ($LASTEXITCODE -ne 0) { throw "DEPLOY REFUSED: candidate verification exited $LASTEXITCODE." }
Complete-ReleaseStep 'build-sign-release-key'
}

# Item 2t5: the setup's outer signature being Valid does not prove that its
# embedded payload carries the pinned release key. Ask the staged product's
# read-only installer question before any tag or release byte is published.
$questionBinary = Join-Path $candidate 'Agent_b.exe'
$setupPath = Join-Path $candidate 'Agent_b-setup.exe'
$question = Invoke-AgentBInstallerQuestion -QuestionBinary $questionBinary -Installer $setupPath
$questionExit = $question.ExitCode
$questionLine = $question.Output
if ($questionExit -ne 0 -or $questionLine -notmatch 'payload signature: AgentB release key .* verified$') {
    throw "DEPLOY REFUSED: staged installer is not accepted by its own pinned-key question: $questionLine"
}
Write-Host "INSTALLER ACCEPTANCE: $questionLine"
Complete-ReleaseStep 'installer-key-question'

Invoke-AgentBShortReleaseRun -CandidateDirectory $candidate -ExpectedCommit $commit | Write-Host
Complete-ReleaseStep 'signed-build-short-run'

$manifest = Get-Content -Raw -LiteralPath (Join-Path $candidate 'candidate-final.json') | ConvertFrom-Json
$releaseManifest = [ordered]@{
    schema  = 1
    version = [string]$manifest.tag
    commit  = [string]$manifest.commit
    file    = 'Agent_b-setup.exe'
    sha256  = [string]$manifest.setup_sha256
    bytes   = [long]$manifest.setup_bytes
    exe_identity = [ordered]@{
        tag     = [string]$manifest.tag
        commit  = [string]$manifest.commit
        dirty   = [bool]$manifest.dirty
        sha256  = [string]$manifest.exe_sha256
        bytes   = [long]$manifest.exe_bytes
    }
    webview2_loader = $manifest.webview2_loader
}
$releaseManifestPath = Join-Path $candidate 'release.json'
[IO.File]::WriteAllText($releaseManifestPath, ($releaseManifest | ConvertTo-Json -Depth 8) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))

$originOutput = @(& git -C $repository remote get-url origin 2>&1)
$originExit = $LASTEXITCODE
$origin = [string]($originOutput | Select-Object -First 1)
if ($originExit -ne 0 -or $origin.Trim() -notmatch '^(?:https://github\.com/|git@github\.com:)(?<repository>[^/]+/[^/]+?)(?:\.git)?$') {
    throw 'DEPLOY REFUSED: origin does not name one GitHub owner/repository pair.'
}
$repositoryName = [string]$Matches.repository
$createLine = "gh release create $Tag --repo $repositoryName --verify-tag --title `"Agent_b $Tag`" --notes-file `"$notesPath`""
$uploadLine = "gh release upload $Tag `"$setupPath`" `"$releaseManifestPath`" --repo $repositoryName --clobber"
& git -C $repository push origin $Tag
if ($LASTEXITCODE -ne 0) { throw "DEPLOY REFUSED: release tag push exited $LASTEXITCODE." }
& $gh.Source release create $Tag --repo $repositoryName --verify-tag --title "Agent_b $Tag" --notes-file $notesPath
if ($LASTEXITCODE -ne 0) {
    Write-Host "PUBLISH CARD: $createLine"
    Write-Host "PUBLISH CARD: $uploadLine"
    throw "DEPLOY REFUSED: GitHub Release creation exited $LASTEXITCODE; authenticate gh and run the two carded lines."
}
& $gh.Source release upload $Tag $setupPath $releaseManifestPath --repo $repositoryName --clobber
if ($LASTEXITCODE -ne 0) {
    Write-Host "PUBLISH CARD: $uploadLine"
    throw "DEPLOY REFUSED: GitHub Release asset upload exited $LASTEXITCODE; run the carded upload line."
}
Complete-ReleaseStep 'publish'

$publicCheckRoot = Join-Path ([IO.Path]::GetTempPath()) ("Agent_b-public-installer-check-" + [Guid]::NewGuid().ToString('N'))
try {
    $null = New-Item -ItemType Directory -Path $publicCheckRoot
    & $gh.Source release download $Tag --repo $repositoryName --pattern 'Agent_b-setup.exe' --dir $publicCheckRoot
    if ($LASTEXITCODE -ne 0) { throw "DEPLOY REFUSED: public installer download exited $LASTEXITCODE." }
    $publicQuestion = Invoke-AgentBInstallerQuestion -QuestionBinary $questionBinary -Installer (Join-Path $publicCheckRoot 'Agent_b-setup.exe')
    $publicExit = $publicQuestion.ExitCode
    $publicLine = $publicQuestion.Output
    if ($publicExit -ne 0 -or $publicLine -notmatch 'payload signature: AgentB release key .* verified$') {
        throw "DEPLOY REFUSED: public installer is not accepted by its own pinned-key question: $publicLine"
    }
    Write-Host "PUBLIC INSTALLER ACCEPTANCE: $publicLine"
    Complete-ReleaseStep 'public-installer-answer'
} finally {
    if (Test-Path -LiteralPath $publicCheckRoot) {
        Remove-TreeWithinAllowedRoots -Path $publicCheckRoot -AllowedRoots @([IO.Path]::GetTempPath()) -Purpose 'public installer acceptance cleanup'
    }
}

Write-Host "DEPLOY COMPLETE: the required installer was built, signed, timestamped, verified, and published at $setupPath"
exit 0
