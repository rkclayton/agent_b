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

Write-Host "DEPLOY COMPLETE: the required installer was built, signed, timestamped, and verified at $candidate\Agent_b-setup.exe"
exit 0
