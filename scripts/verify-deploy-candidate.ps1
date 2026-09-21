[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$CandidateDirectory,
    [Parameter(Mandatory = $true)][string]$ExpectedTag,
    [Parameter(Mandatory = $true)][string]$ExpectedCommit
)

$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath($CandidateDirectory)
$manifestPath = Join-Path $root 'candidate-final.json'
$binary = Join-Path $root 'Agent_b.exe'
$setup = Join-Path $root 'Agent_b-setup.exe'
foreach ($required in @($manifestPath, $binary, $setup)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
        throw "DEPLOY REFUSED: required release artifact is missing: $required"
    }
}

$manifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json
$expectedCommitValue = $ExpectedCommit.Trim().ToLowerInvariant()
$problems = @()
if ([string]$manifest.tag -cne $ExpectedTag) { $problems += "manifest tag $($manifest.tag), expected $ExpectedTag" }
if ([string]$manifest.commit -cne $expectedCommitValue) { $problems += "manifest commit $($manifest.commit), expected $expectedCommitValue" }
if ([bool]$manifest.dirty) { $problems += 'manifest is dirty' }

$exeSha = (Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash.ToLowerInvariant()
$setupSha = (Get-FileHash -LiteralPath $setup -Algorithm SHA256).Hash.ToLowerInvariant()
if ($exeSha -cne [string]$manifest.exe_sha256) { $problems += "Agent_b.exe hash $exeSha does not match the manifest" }
if ($setupSha -cne [string]$manifest.setup_sha256) { $problems += "Agent_b-setup.exe hash $setupSha does not match the manifest" }
if ((Get-Item -LiteralPath $setup).Length -ne [long]$manifest.setup_bytes) { $problems += 'Agent_b-setup.exe byte count does not match the manifest' }

$setupBytes = [IO.File]::ReadAllBytes($setup)
$setupText = [Text.Encoding]::GetEncoding(28591).GetString($setupBytes)
$embeddedTag = [regex]::Match($setupText, '-X harness/internal/buildinfo\.Tag=(v[0-9A-Za-z.+-]+)')
$embeddedCommit = [regex]::Match($setupText, '-X harness/internal/buildinfo\.Commit=([0-9a-f]{40})')
if (-not $embeddedTag.Success -or $embeddedTag.Groups[1].Value -cne $ExpectedTag) { $problems += 'setup executable does not embed the release tag' }
if (-not $embeddedCommit.Success -or $embeddedCommit.Groups[1].Value -cne $expectedCommitValue) { $problems += 'setup executable does not embed the release commit' }

$signature = Get-AuthenticodeSignature -LiteralPath $setup
if ($signature.Status -ne 'Valid') { $problems += "setup Authenticode status is $($signature.Status), expected Valid" }
if (-not $signature.TimeStamperCertificate) { $problems += 'setup has no Authenticode timestamp' }
if ($problems.Count) { throw "DEPLOY REFUSED: $($problems -join '; ')." }

Write-Host "DEPLOY READY: $setup"
Write-Host "IDENTITY: $ExpectedTag $expectedCommitValue sha256 $setupSha"
exit 0
