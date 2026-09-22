[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][ValidatePattern('^v\d+\.\d+\.\d+$')][string]$Tag,
    [Parameter(Mandatory = $true)][ValidatePattern('^[0-9A-Fa-f ]+$')][string]$SigningThumbprint
)

$ErrorActionPreference = 'Stop'
$repository = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$candidate = Join-Path (Join-Path $repository 'candidates') $Tag
$windowsPowerShell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
. (Join-Path $PSScriptRoot 'removal-guard.ps1')
. (Join-Path $PSScriptRoot 'deploy-candidate-state.ps1')
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if ($principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'DEPLOY REFUSED: run deploy-release.ps1 from an ordinary, non-elevated console; it requests elevation once for signing only.'
}
Write-Host "DEPLOY PARENT: identity=$($identity.Name) elevated=false; staging, verification, and publication stay at this token"
$commitOutput = @(& git -C $repository rev-parse "$Tag^{commit}" 2>&1)
$commitExit = $LASTEXITCODE
$commit = [string]($commitOutput | Select-Object -First 1)
if ($commitExit -ne 0 -or $commit.Trim() -notmatch '^[0-9a-fA-F]{40}$') { throw "DEPLOY REFUSED: tag $Tag does not resolve to a commit." }
$commit = $commit.Trim().ToLowerInvariant()
$headOutput = @(& git -C $repository rev-parse HEAD 2>&1)
$headExit = $LASTEXITCODE
$head = [string]($headOutput | Select-Object -First 1)
if ($headExit -ne 0 -or $head.Trim().ToLowerInvariant() -cne $commit) { throw "DEPLOY REFUSED: tag $Tag does not point at HEAD." }
$statusOutput = @(& git -C $repository status --porcelain --untracked-files=normal 2>&1)
$statusExit = $LASTEXITCODE
if ($statusExit -ne 0) { throw "DEPLOY REFUSED: git status exited $statusExit." }
if ($statusOutput.Count) { throw 'DEPLOY REFUSED: the repository is dirty.' }

if (Test-Path -LiteralPath $candidate) {
    Remove-MatchingStagedCandidate -Candidate $candidate -CandidatesRoot (Join-Path $repository 'candidates') -ExpectedTag $Tag
}

& $windowsPowerShell -NoLogo -NoProfile -ExecutionPolicy Bypass -File (Join-Path $repository 'scripts\test-signing-key-policies.ps1')
if ($LASTEXITCODE -ne 0) { throw "DEPLOY REFUSED: signing key policy check exited $LASTEXITCODE." }

& node (Join-Path $repository 'scripts\stage-candidate.mjs') --tag $Tag
if ($LASTEXITCODE -ne 0) { throw "DEPLOY REFUSED: candidate staging exited $LASTEXITCODE." }

$signingReport = Join-Path $candidate 'signing-report.json'
$signingScript = Join-Path $repository 'scripts\sign-release.ps1'
$signingArguments = "-NoLogo -NoProfile -ExecutionPolicy Bypass -File `"$signingScript`" -Path `"$candidate`" -Thumbprint $SigningThumbprint -ReportPath `"$signingReport`""
$signing = Start-Process -FilePath $windowsPowerShell -ArgumentList $signingArguments -Verb RunAs -Wait -PassThru -WindowStyle Hidden
$signingExit = $signing.ExitCode
if (Test-Path -LiteralPath $signingReport -PathType Leaf) {
    $signingResult = Get-Content -Raw -LiteralPath $signingReport | ConvertFrom-Json
    Write-Host "SIGNING CHILD: identity=$($signingResult.identity) elevated=$($signingResult.elevated) outcome=$($signingResult.outcome) signed=$($signingResult.signed)/$($signingResult.signable)"
    if ($signingResult.reason) { Write-Host "SIGNING CHILD REASON: $($signingResult.reason)" }
}
if ($signingExit -ne 0) { throw "DEPLOY REFUSED: elevated release signing exited $signingExit." }

& $windowsPowerShell -NoLogo -NoProfile -ExecutionPolicy Bypass -File (Join-Path $repository 'scripts\verify-deploy-candidate.ps1') -CandidateDirectory $candidate -ExpectedTag $Tag -ExpectedCommit $commit
if ($LASTEXITCODE -ne 0) { throw "DEPLOY REFUSED: candidate verification exited $LASTEXITCODE." }

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

$repositoryName = 'rkclayton/agent_b'
$setupPath = Join-Path $candidate 'Agent_b-setup.exe'
$createLine = "gh release create $Tag --repo $repositoryName --verify-tag --title `"Agent_b $Tag`" --notes `"Agent_b $Tag`""
$uploadLine = "gh release upload $Tag `"$setupPath`" `"$releaseManifestPath`" --repo $repositoryName --clobber"
$gh = Get-Command gh.exe -ErrorAction SilentlyContinue
if (-not $gh) {
    Write-Host "PUBLISH CARD: $createLine"
    Write-Host "PUBLISH CARD: $uploadLine"
    throw 'DEPLOY REFUSED: GitHub CLI is unavailable; the release assets remain staged locally.'
}
& $gh.Source release create $Tag --repo $repositoryName --verify-tag --title "Agent_b $Tag" --notes "Agent_b $Tag"
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

Write-Host "DEPLOY COMPLETE: the required installer was built, signed, timestamped, verified, and published at $setupPath"
exit 0
