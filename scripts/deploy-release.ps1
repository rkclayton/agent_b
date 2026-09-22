[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][ValidatePattern('^v\d+\.\d+\.\d+$')][string]$Tag,
    [Parameter(Mandatory = $true)][string]$SigningThumbprint
)

$ErrorActionPreference = 'Stop'
$repository = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$candidate = Join-Path (Join-Path $repository 'candidates') $Tag
$windowsPowerShell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
$commit = [string](& git -C $repository rev-parse "$Tag^{commit}" | Select-Object -First 1)
if ($LASTEXITCODE -ne 0 -or $commit.Trim() -notmatch '^[0-9a-fA-F]{40}$') { throw "DEPLOY REFUSED: tag $Tag does not resolve to a commit." }
$commit = $commit.Trim().ToLowerInvariant()
$head = [string](& git -C $repository rev-parse HEAD | Select-Object -First 1)
if ($LASTEXITCODE -ne 0 -or $head.Trim().ToLowerInvariant() -cne $commit) { throw "DEPLOY REFUSED: tag $Tag does not point at HEAD." }
if (@(& git -C $repository status --porcelain --untracked-files=normal).Count) { throw 'DEPLOY REFUSED: the repository is dirty.' }

& node (Join-Path $repository 'scripts\stage-candidate.mjs') --tag $Tag
if ($LASTEXITCODE -ne 0) { throw "DEPLOY REFUSED: candidate staging exited $LASTEXITCODE." }

& $windowsPowerShell -NoLogo -NoProfile -ExecutionPolicy Bypass -File (Join-Path $repository 'scripts\sign-release.ps1') -Path $candidate -Thumbprint $SigningThumbprint
if ($LASTEXITCODE -ne 0) { throw "DEPLOY REFUSED: release signing exited $LASTEXITCODE." }

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
