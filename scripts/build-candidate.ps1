[CmdletBinding()]
param(
    # The tree to build in: a checkout, or a clean archive staged as a candidate.
    [string]$SourceDirectory,
    # The tag the exe must report. The release step passes the tag it is about to
    # push; a mismatch fails here, never at install time.
    [string]$ExpectedTag,
    # Required when the tree is not a git checkout (a staged archive).
    [string]$Commit,
    [ValidateSet('', 'true', 'false')]
    [string]$Dirty = ''
)

# Item 2eu: the release step builds the exe once, into the candidate, and records
# what it built. The installer never builds; it installs this exe only when its
# SHA-256 and embedded tag and commit equal this manifest.

$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($SourceDirectory)) { $SourceDirectory = Split-Path -Parent $PSScriptRoot }
$sourceRoot = [IO.Path]::GetFullPath($SourceDirectory)
$manifestPath = Join-Path $sourceRoot 'candidate-final.json'
$binary = Join-Path $sourceRoot 'Agent_b.exe'

function Find-Go {
    param([string]$Source)
    foreach ($candidate in @((Join-Path $Source '.tools\go\bin\go.exe'), (Join-Path (Split-Path -Parent $PSScriptRoot) '.tools\go\bin\go.exe'), 'C:\Go\bin\go.exe')) {
        if (Test-Path -LiteralPath $candidate -PathType Leaf) { return $candidate }
    }
    $command = Get-Command go.exe -ErrorAction SilentlyContinue
    if ($command) { return $command.Source }
    throw 'Go 1.24 or newer was not found; the release step builds the candidate and needs it.'
}

$tagSource = Get-Content -Raw -LiteralPath (Join-Path $sourceRoot 'internal\buildinfo\buildinfo.go')
$tagMatch = [regex]::Match($tagSource, '(?m)^\s*Tag\s*=\s*"([^"]+)"')
if (-not $tagMatch.Success) { throw 'internal\buildinfo\buildinfo.go declares no Tag.' }
$sourceTag = $tagMatch.Groups[1].Value
$installerSource = Get-Content -Raw -LiteralPath (Join-Path $sourceRoot 'scripts\install-Agent_b.ps1')
$versionMatch = [regex]::Match($installerSource, "(?m)^\`$displayVersion\s*=\s*'([^']+)'")
if (-not $versionMatch.Success) { throw 'scripts\install-Agent_b.ps1 declares no displayVersion.' }
if ($sourceTag -ne ('v' + $versionMatch.Groups[1].Value)) {
    throw "buildinfo Tag $sourceTag and installer displayVersion $($versionMatch.Groups[1].Value) disagree."
}

if ([string]::IsNullOrWhiteSpace($Commit)) {
    # Windows PowerShell 5.1 turns git's stderr into a terminating error under
    # Stop; outside a checkout that must reach the explanation below instead.
    $ErrorActionPreference = 'Continue'
    # Collect the whole output first: Select-Object -First stops the pipeline
    # early and can leave $LASTEXITCODE unset.
    $topLines = @(& git -C $sourceRoot rev-parse --show-toplevel 2>$null)
    $topExit = $LASTEXITCODE
    $top = [string]($topLines | Select-Object -First 1)
    $ErrorActionPreference = 'Stop'
    if ($topExit -ne 0 -or [string]::IsNullOrWhiteSpace($top) -or
        -not [IO.Path]::GetFullPath($top.Trim()).TrimEnd('\').Equals($sourceRoot.TrimEnd('\'), [StringComparison]::OrdinalIgnoreCase)) {
        # A tree nested inside another checkout would otherwise borrow that
        # checkout's HEAD, which is exactly how a candidate got a wrong identity.
        throw "$sourceRoot is not the top of a git checkout; pass -Commit and -Dirty for a staged archive."
    }
    $Commit = [string](& git -C $sourceRoot rev-parse HEAD | Select-Object -First 1)
    if ([string]::IsNullOrWhiteSpace($Dirty)) {
        $Dirty = if (@(& git -C $sourceRoot status --porcelain --untracked-files=normal).Count -gt 0) { 'true' } else { 'false' }
    }
}
$Commit = $Commit.Trim().ToLowerInvariant()
if ($Commit -notmatch '^[0-9a-f]{40}$') { throw "Commit must be a full 40-character hash: $Commit" }
if ([string]::IsNullOrWhiteSpace($Dirty)) { throw 'Dirty must be true or false for a staged archive.' }

$go = Find-Go $sourceRoot
if (Test-Path -LiteralPath $manifestPath) { Remove-Item -LiteralPath $manifestPath -Force }
$ldflags = "-X harness/internal/buildinfo.Tag=$sourceTag -X harness/internal/buildinfo.Commit=$Commit -X harness/internal/buildinfo.Dirty=$Dirty"
Push-Location $sourceRoot
try { & $go build -ldflags $ldflags -o $binary ./cmd/harness } finally { Pop-Location }
if ($LASTEXITCODE -ne 0) { throw "go build exited $LASTEXITCODE." }

$reported = (& $binary -version | Out-String) | ConvertFrom-Json
if ($LASTEXITCODE -ne 0) { throw "Agent_b.exe -version exited $LASTEXITCODE." }
$sha = (Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash.ToLowerInvariant()
$failures = @()
if ($reported.tag -ne $sourceTag) { $failures += "tag $($reported.tag) (expected $sourceTag)" }
if ($ExpectedTag -and $reported.tag -ne $ExpectedTag) { $failures += "tag $($reported.tag) (release is $ExpectedTag)" }
if ($reported.commit -ne $Commit) { $failures += "commit $($reported.commit) (expected $Commit)" }
if ([string]([bool]$reported.dirty).ToString().ToLowerInvariant() -ne $Dirty) { $failures += "dirty $($reported.dirty) (expected $Dirty)" }
if ($reported.source -ne 'ldflags') { $failures += "identity source $($reported.source) (expected ldflags)" }
if ($reported.executable_sha256 -ne $sha) { $failures += "reported sha256 $($reported.executable_sha256) (file is $sha)" }
if ($ExpectedTag -and [bool]$reported.dirty) { $failures += 'a dirty tree (a release is built from a clean commit)' }
# The installer reads the identity from the -ldflags text in the Go build
# information without running the exe; prove that text is there (it is not
# under -trimpath), so a candidate that passes here cannot fail there.
$text = [Text.Encoding]::GetEncoding(28591).GetString([IO.File]::ReadAllBytes($binary))
$embeddedTag = [regex]::Match($text, '-X harness/internal/buildinfo\.Tag=(v[0-9A-Za-z.+-]+)')
$embeddedCommit = [regex]::Match($text, '-X harness/internal/buildinfo\.Commit=([0-9a-f]{40})')
if (-not $embeddedTag.Success -or $embeddedTag.Groups[1].Value -ne $sourceTag -or -not $embeddedCommit.Success -or $embeddedCommit.Groups[1].Value -ne $Commit) {
    $failures += 'build information the installer can read without running it (no matching -ldflags text; was -trimpath set?)'
}
if ($failures.Count) { throw "CANDIDATE BUILD REFUSED: Agent_b.exe reports $($failures -join '; '). No manifest was written." }

$manifest = [ordered]@{
    schema     = 1
    tag        = $reported.tag
    commit     = $reported.commit
    dirty      = [bool]$reported.dirty
    display    = $reported.display
    exe_sha256 = $sha
    exe_bytes  = (Get-Item -LiteralPath $binary).Length
    built_at   = (Get-Date).ToUniversalTime().ToString('o')
}
[IO.File]::WriteAllText($manifestPath, ($manifest | ConvertTo-Json) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
Write-Host "CANDIDATE: Agent_b.exe $($manifest.tag) $($manifest.display) sha256 $sha"
Write-Host "MANIFEST: $manifestPath"
exit 0
