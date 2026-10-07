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
$repository = Split-Path -Parent $PSScriptRoot
$ownedCheck = Join-Path $repository 'tools\owned-check.ps1'
if (-not (Test-Path -LiteralPath $ownedCheck -PathType Leaf)) { throw 'The shared owned-things check is missing.' }
$ownedCall = $deploy.IndexOf("'tools\owned-check.ps1'")
$notesCall = $deploy.IndexOf("'tools\check-release-notes.mjs'")
if ($ownedCall -lt 0 -or $notesCall -lt 0 -or $ownedCall -gt $notesCall) { throw 'Deploy must run the owned-things check before release work.' }

$ownedFixture = Join-Path ([IO.Path]::GetTempPath()) ('Agent_b-owned-check-' + [Guid]::NewGuid().ToString('N'))
try {
    $defaultHome = Join-Path $ownedFixture '.agentb'
    $selectedHome = Join-Path $ownedFixture 'chosen'
    $null = New-Item -ItemType Directory -Path $defaultHome -Force
    $null = New-Item -ItemType Directory -Path $selectedHome -Force
    $first = Join-Path $ownedFixture 'first.dat'; $second = Join-Path $ownedFixture 'second.dat'
    $plantedValue = 'owned-secret-' + [Guid]::NewGuid().ToString('N')
    [IO.File]::WriteAllText($first, $plantedValue); [IO.File]::WriteAllText($second, 'fixture-two')
    $list = Join-Path $defaultHome 'OWNED-agent_b.md'
    $heading = "# OWNED $([char]0x2014) agent_b"
    $table = "$heading`n`n| name | kind | where | what | read by | stops | recreate | sha256 |`n| --- | --- | --- | --- | --- | --- | --- | --- |`n| first-file | file | $first | fixture | test:1 | test | recreate | |`n| second-file | file | $second | fixture | test:2 | test | recreate | |`n"
    [IO.File]::WriteAllText($list, $table, [Text.UTF8Encoding]::new($false))
    $savedProfile = $env:USERPROFILE; $env:USERPROFILE = $ownedFixture
    $savedHome = [Environment]::GetEnvironmentVariable('AGENTB_HOME', 'User')
    [Environment]::SetEnvironmentVariable('AGENTB_HOME', $null, 'User')
    function Invoke-OwnedFixture { $out = @(& powershell.exe -NoLogo -NoProfile -NonInteractive -File $ownedCheck 2>&1); return @{ Output=($out -join "`n"); Exit=$LASTEXITCODE } }
    $complete = Invoke-OwnedFixture
    if ($complete.Exit -ne 0 -or $complete.Output -cne 'owned: ok 2 rows') { throw "complete owned fixture differed: $($complete.Exit) $($complete.Output)" }
    $list = Join-Path $selectedHome 'OWNED-agent_b.md'
    [IO.File]::Move((Join-Path $defaultHome 'OWNED-agent_b.md'), $list)
    $signal = Join-Path $ownedFixture 'read-now'; $lateOutput = Join-Path $ownedFixture 'late.txt'
    $lateCommand = "while (-not (Test-Path -LiteralPath '$signal')) { Start-Sleep -Milliseconds 20 }; & '$ownedCheck'"
    $late = Start-Process powershell.exe -ArgumentList @('-NoLogo','-NoProfile','-NonInteractive','-Command',$lateCommand) -WindowStyle Hidden -RedirectStandardOutput $lateOutput -PassThru
    [Environment]::SetEnvironmentVariable('AGENTB_HOME', $selectedHome, 'User')
    [IO.File]::WriteAllText($signal, '')
    $late.WaitForExit()
    $late.Refresh()
    $lateText = [IO.File]::ReadAllText($lateOutput).Trim()
    # Windows PowerShell can leave ExitCode unset on a Start-Process object
    # after redirected output, even though WaitForExit completed. The exact
    # success line is emitted only by Complete-OwnedCheck immediately before
    # it exits zero, so pair it with HasExited instead of a nullable property.
    if (-not $late.HasExited -or $lateText -cne 'owned: ok 2 rows') { throw "A process started before AGENTB_HOME was set did not find the selected HOME: exited=$($late.HasExited), output '$lateText'." }
    [IO.File]::Move($second, "$second.renamed")
    $missing = Invoke-OwnedFixture
    if ($missing.Exit -ne 1 -or $missing.Output -cne 'owned: missing second-file') { throw "missing owned fixture differed: $($missing.Exit) $($missing.Output)" }
    [IO.File]::Delete($list)
    $absent = Invoke-OwnedFixture; $expectedAbsent = "owned: no list at $list"
    if ($absent.Exit -ne 2 -or $absent.Output -cne $expectedAbsent) { throw "absent owned fixture differed: $($absent.Exit) $($absent.Output)" }
    if (($complete.Output + $missing.Output + $absent.Output + $table + (& git -C $repository diff)) -match [regex]::Escape($plantedValue)) { throw 'The owned check exposed a planted value.' }
    $candidateBefore = Test-Path -LiteralPath (Join-Path $repository 'candidates\v9.9.9')
    $deployOutput = @(& powershell.exe -NoLogo -NoProfile -NonInteractive -File (Join-Path $repository 'tools\deploy-release.ps1') -Tag v9.9.9 -SigningThumbprint A 2>&1)
    if ($LASTEXITCODE -ne 2 -or ($deployOutput -join "`n") -cne $expectedAbsent) { throw 'Deploy did not relay the no-list refusal exactly.' }
    if ($candidateBefore -or (Test-Path -LiteralPath (Join-Path $repository 'candidates\v9.9.9'))) { throw 'Deploy staged after the owned check refused.' }
} finally {
    [Environment]::SetEnvironmentVariable('AGENTB_HOME', $savedHome, 'User')
    $env:USERPROFILE = $savedProfile
    if (Test-Path -LiteralPath $ownedFixture) { Remove-TreeWithinAllowedRoots -Path $ownedFixture -AllowedRoots @([IO.Path]::GetTempPath()) -Purpose 'owned-check fixture cleanup' }
}

foreach ($required in @('stage-candidate.mjs', 'sign-release.ps1', 'verify-deploy-candidate.ps1', 'Agent_b-setup.exe', 'DEPLOY COMPLETE')) {
    if ($deploy -notmatch [regex]::Escape($required)) { throw "Deploy entry point does not require $required." }
}
foreach ($required in @('release.json', 'setup_sha256', 'webview2_loader', 'release-notes', '--notes-file', 'release notes are missing', 'gh release create', 'gh release upload', 'someone/agent_b')) {
    if ($deploy -notmatch [regex]::Escape($required)) { throw "Deploy publication does not require $required." }
}
if ($deploy -match '--notes\s+"Agent_b') { throw 'Deploy still publishes a placeholder release body.' }
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

    $identity = "agentb-release-identity:v9.9.9:$('a' * 40)"
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
