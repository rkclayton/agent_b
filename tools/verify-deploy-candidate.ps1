[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$CandidateDirectory,
    [Parameter(Mandatory = $true)][string]$ExpectedTag,
    [Parameter(Mandatory = $true)][string]$ExpectedCommit,
    [switch]$TestUnsignedInstalledFile
)

$ErrorActionPreference = 'Stop'
. (Join-Path (Split-Path -Parent $PSScriptRoot) 'scripts\removal-guard.ps1')
. (Join-Path (Split-Path -Parent $PSScriptRoot) 'scripts\signing-key-policy.ps1')
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

# Install the signed single-file setup without launching the product. TestMode
# pins every target below the disposable root; -NoStart prevents a listener.
$verifyRoot = Join-Path ([IO.Path]::GetTempPath()) ('Agent_b-deploy-verify-' + [Guid]::NewGuid().ToString('N'))
$registryPath = 'HKCU:\Software\Agent_b-Deploy-Verify-' + [Guid]::NewGuid().ToString('N')
$verificationPassed = $false
try {
    $application = Join-Path $verifyRoot 'Application\Agent_b'
    $data = Join-Path $verifyRoot 'Data\Agent_b'
    $workspace = Join-Path $verifyRoot 'ProgramData\Agent_b\workspace'
    # Windows PowerShell 5.1 turns any native stderr line into a terminating
    # NativeCommandError while the script-wide preference is Stop. The setup
    # intentionally writes its durable-log notice there, so capture both
    # streams under Continue and judge the native exit code ourselves.
    $savedErrorActionPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $output = (& $setup --quiet --install-data (Join-Path $verifyRoot 'Data') -NoStart -ApplicationDirectory $application -DataDirectory $data -WorkspaceDirectory $workspace -StartMenuDirectory (Join-Path $verifyRoot 'StartMenu') -UninstallRegistryPath $registryPath -TestMode 2>&1 | Out-String)
        $setupExit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $savedErrorActionPreference
    }
    if ($setupExit -ne 0 -or $output -notmatch 'AUTOSTART (?:DISABLED|SKIPPED): -NoStart') {
        throw "DEPLOY REFUSED: signed setup did not complete its disposable -NoStart install.`n$output"
    }
    # 2ki: verify the installed bytes against the central signing policy.
    $installedSignables = @((Join-Path $application 'Agent_b.exe'))
    foreach ($relative in @(Get-AgentBRuntimeSigningPolicy -Root $root).Signable) { $installedSignables += Join-Path $application ($relative.Replace('/', '\')) }
    if ($TestUnsignedInstalledFile) {
        $replace = $installedSignables | Where-Object { $_ -ne (Join-Path $application 'Agent_b.exe') } | Select-Object -First 1
        [IO.File]::WriteAllText($replace, 'unsigned replacement', [Text.UTF8Encoding]::new($false))
    }
    foreach ($installed in $installedSignables) {
        $installedSignature = Get-AuthenticodeSignature -LiteralPath $installed
        if ($installedSignature.Status -ne 'Valid' -or -not $installedSignature.TimeStamperCertificate) { throw "DEPLOY REFUSED: installed signable $installed is $($installedSignature.Status) or lacks a timestamp." }
    }
    Write-Host "INSTALLED SIGNATURES: $($installedSignables.Count)/$($installedSignables.Count) Valid and timestamped"
    $verificationPassed = $true
} finally {
    if (Test-Path -LiteralPath $registryPath) { Remove-Item -LiteralPath $registryPath -Recurse -Force }
    if ($verificationPassed -and (Test-Path -LiteralPath $verifyRoot)) {
        $resolved = [IO.Path]::GetFullPath($verifyRoot)
        $temporary = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
        if (-not $resolved.StartsWith($temporary, [StringComparison]::OrdinalIgnoreCase) -or (Split-Path -Leaf $resolved) -notlike 'Agent_b-deploy-verify-*') {
            throw "Refusing deploy verification cleanup outside its disposable root: $resolved"
        }
        Remove-TreeWithinAllowedRoots -Path $resolved -AllowedRoots @([IO.Path]::GetTempPath()) -Purpose 'deploy verification cleanup'
    } elseif (Test-Path -LiteralPath $verifyRoot) {
        Write-Warning "DEPLOY EVIDENCE RETAINED: $verifyRoot"
    }
}

Write-Host "DEPLOY READY: $setup"
Write-Host "IDENTITY: $ExpectedTag $expectedCommitValue sha256 $setupSha"
exit 0
