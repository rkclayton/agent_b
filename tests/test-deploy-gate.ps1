[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
. (Join-Path (Split-Path -Parent $PSScriptRoot) 'scripts\removal-guard.ps1')
. (Join-Path (Split-Path -Parent $PSScriptRoot) 'tools\deploy-candidate-state.ps1')
$deploy = Get-Content -Raw -LiteralPath (Join-Path (Split-Path -Parent $PSScriptRoot) 'tools\deploy-release.ps1')
$verify = Get-Content -Raw -LiteralPath (Join-Path (Split-Path -Parent $PSScriptRoot) 'tools\verify-deploy-candidate.ps1')
$sign = Get-Content -Raw -LiteralPath (Join-Path (Split-Path -Parent $PSScriptRoot) 'tools\sign-release.ps1')
if ($sign -notmatch 'NotAfter -le \[DateTime\]::Now\.AddDays\(30\)' -or $sign -notmatch 'SIGNING REFUSED:.+more than 30 days remaining') {
    throw 'release signing no longer refuses a publisher key within 30 days of expiry'
}
$stage = Get-Content -Raw -LiteralPath (Join-Path (Split-Path -Parent $PSScriptRoot) 'tools\stage-candidate.mjs')

foreach ($required in @('stage-candidate.mjs', 'sign-release.ps1', 'verify-deploy-candidate.ps1', 'Agent_b-setup.exe', 'DEPLOY COMPLETE')) {
    if ($deploy -notmatch [regex]::Escape($required)) { throw "Deploy entry point does not require $required." }
}
foreach ($required in @('release.json', 'setup_sha256', 'webview2_loader', 'gh release create', 'gh release upload', 'rkclayton/agent_b')) {
    if ($deploy -notmatch [regex]::Escape($required)) { throw "Deploy publication does not require $required." }
}
foreach ($required in @('$commitExit', '$headExit', '$statusExit', 'test-signing-key-policies.ps1')) {
    if ($deploy -notmatch [regex]::Escape($required)) { throw "Deploy entry point does not preserve host repair $required." }
}
if ($deploy -match 'rev-parse[^\r\n]*\|\s*Select-Object') {
    throw 'Deploy still reads a piped git command through inherited LASTEXITCODE.'
}
if ($deploy -match 'Start-Process[^\r\n]+-Verb RunAs' -or
    $deploy -notmatch '& \$windowsPowerShell[^\r\n]+\$signingScript' -or
    $deploy -notmatch '\$signingExit = \$LASTEXITCODE') {
    throw 'Deploy must sign in the ordinary console without raising an elevation child.'
}
$elevationRefusal = $deploy.IndexOf('run deploy-release.ps1 from an ordinary, non-elevated console')
$stagingCall = $deploy.IndexOf("'tools\stage-candidate.mjs'")
if ($elevationRefusal -lt 0 -or $stagingCall -lt 0 -or $elevationRefusal -gt $stagingCall) {
    throw 'Deploy must refuse an elevated parent before staging.'
}
if ($deploy -notmatch 'Remove-MatchingStagedCandidate' -or $deploy -notmatch 'signing-report\.json') {
    throw 'Deploy does not report/recover a matching stale candidate or relay the signing identity.'
}
foreach ($required in @('candidate-final.json', 'Agent_b.exe', 'Agent_b-setup.exe', 'setup_sha256', 'setup_bytes', 'Get-AuthenticodeSignature', 'TimeStamperCertificate', 'DEPLOY REFUSED')) {
    if ($verify -notmatch [regex]::Escape($required)) { throw "Deploy verifier does not require $required." }
}
foreach ($required in @('-NoStart', '-TestMode', 'AUTOSTART (?:DISABLED|SKIPPED): -NoStart', '$verificationPassed', 'DEPLOY EVIDENCE RETAINED', '$TestUnsignedInstalledFile')) {
    if ($verify -notmatch [regex]::Escape($required)) { throw "Deploy verifier does not run its signed setup preflight with $required." }
}
if ($verify -match '\-TestMode\s+\-WhatIf') { throw 'Deploy verifier still makes its installed-file assertion impossible with -WhatIf.' }
foreach ($required in @('Agent_b.exe', 'Agent_b-setup.exe', 'Get-AgentBRuntimeSigningPolicy', 'PayloadOnly', 'BinaryOnly')) {
    if ($sign -notmatch [regex]::Escape($required)) { throw "Release signing policy does not include $required." }
}
if ($stage -notmatch 'manifest\.setup_sha256' -or $stage -notmatch 'manifest\.setup_bytes') { throw 'Candidate staging does not record the setup artifact.' }
foreach ($required in @('windowsPowerShellEnvironment', 'PSModulePath', 'System32", "WindowsPowerShell", "v1.0", "powershell.exe')) {
    if ($stage -notmatch [regex]::Escape($required)) { throw "Candidate staging does not preserve native Windows PowerShell host repair $required." }
}
if ($deploy.IndexOf('-PayloadOnly') -gt $deploy.IndexOf("--build `$Tag") -or
    $deploy.IndexOf("--build `$Tag") -gt $deploy.IndexOf('-BinaryOnly')) {
    throw 'Deploy must sign payload, then capture it, then sign the executables.'
}
foreach ($required in @('Get-AgentBRuntimeSigningPolicy', 'INSTALLED SIGNATURES', 'TimeStamperCertificate')) {
    if ($verify -notmatch [regex]::Escape($required)) { throw "Deploy verifier does not inspect installed payload rule $required." }
}

$fixture = Join-Path ([IO.Path]::GetTempPath()) ('Agent_b-deploy-gate-' + [Guid]::NewGuid().ToString('N'))
try {
    $null = New-Item -ItemType Directory -Path $fixture -Force
    [IO.File]::WriteAllBytes((Join-Path $fixture 'Agent_b.exe'), [byte[]](1, 2, 3))
    [IO.File]::WriteAllText((Join-Path $fixture 'candidate-final.json'), '{}', [Text.UTF8Encoding]::new($false))
    try {
        & (Join-Path (Split-Path -Parent $PSScriptRoot) 'tools\verify-deploy-candidate.ps1') -CandidateDirectory $fixture -ExpectedTag 'v9.9.9' -ExpectedCommit ('a' * 40)
        throw 'The verifier accepted a candidate with no setup executable.'
    } catch {
        if ($_.Exception.Message -notmatch 'required release artifact is missing:.*Agent_b-setup\.exe') { throw }
    }

    $identity = "-X harness/internal/buildinfo.Tag=v9.9.9 -X harness/internal/buildinfo.Commit=$('a' * 40)"
    [IO.File]::WriteAllText((Join-Path $fixture 'Agent_b-setup.exe'), $identity, [Text.Encoding]::GetEncoding(28591))
    $exeSha = (Get-FileHash -LiteralPath (Join-Path $fixture 'Agent_b.exe') -Algorithm SHA256).Hash.ToLowerInvariant()
    $setupSha = (Get-FileHash -LiteralPath (Join-Path $fixture 'Agent_b-setup.exe') -Algorithm SHA256).Hash.ToLowerInvariant()
    $manifest = [ordered]@{ tag='v9.9.9'; commit=('a' * 40); dirty=$false; exe_sha256=$exeSha; setup_sha256=$setupSha; setup_bytes=(Get-Item (Join-Path $fixture 'Agent_b-setup.exe')).Length }
    [IO.File]::WriteAllText((Join-Path $fixture 'candidate-final.json'), ($manifest | ConvertTo-Json), [Text.UTF8Encoding]::new($false))
    try {
        & (Join-Path (Split-Path -Parent $PSScriptRoot) 'tools\verify-deploy-candidate.ps1') -CandidateDirectory $fixture -ExpectedTag 'v9.9.9' -ExpectedCommit ('a' * 40)
        throw 'The verifier accepted an unsigned setup executable.'
    } catch {
        if ($_.Exception.Message -notmatch 'setup Authenticode status is .*expected Valid.*setup has no Authenticode timestamp') { throw }
    }

    $stale = Join-Path $fixture 'v9.9.9'
    $null = New-Item -ItemType Directory -Path $stale
    [IO.File]::WriteAllBytes((Join-Path $stale 'Agent_b.exe'), [byte[]](1, 2, 3))
    [IO.File]::WriteAllBytes((Join-Path $stale 'Agent_b-setup.exe'), [byte[]](1, 2, 3))
    [IO.File]::WriteAllText((Join-Path $stale 'candidate-final.json'), '{"tag":"v9.9.9","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}', [Text.UTF8Encoding]::new($false))
    Remove-MatchingStagedCandidate -Candidate $stale -CandidatesRoot $fixture -ExpectedTag 'v9.9.9'
    if (Test-Path -LiteralPath $stale) { throw 'A matching stale candidate was not removed for restaging.' }

    $foreign = Join-Path $fixture 'v9.9.8'
    $null = New-Item -ItemType Directory -Path $foreign
    [IO.File]::WriteAllText((Join-Path $foreign 'candidate-final.json'), '{"tag":"v9.9.7","commit":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}', [Text.UTF8Encoding]::new($false))
    try {
        Remove-MatchingStagedCandidate -Candidate $foreign -CandidatesRoot $fixture -ExpectedTag 'v9.9.8'
        throw 'A mismatched stale candidate was removed.'
    } catch {
        if ($_.Exception.Message -notmatch 'does not identify itself as v9\.9\.8; it was retained') { throw }
    }
    if (-not (Test-Path -LiteralPath $foreign)) { throw 'A mismatched stale candidate was not retained.' }
} finally {
    if (Test-Path -LiteralPath $fixture) { Remove-TreeWithinAllowedRoots -Path $fixture -AllowedRoots @([IO.Path]::GetTempPath()) -Purpose 'deploy-gate cleanup' }
}
Write-Host 'PASS: deploy signs in the ordinary console, refuses elevated staging, safely recovers matching stale candidates, and requires a signed setup matching the release tag and commit'
