[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'removal-guard.ps1')
$deploy = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'deploy-release.ps1')
$verify = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'verify-deploy-candidate.ps1')
$sign = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'sign-release.ps1')
$stage = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'stage-candidate.mjs')

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
if (($deploy | Select-String -Pattern '\$windowsPowerShell .*sign-release\.ps1' -AllMatches).Matches.Count -ne 1 -or
    ($deploy | Select-String -Pattern '\$windowsPowerShell .*verify-deploy-candidate\.ps1' -AllMatches).Matches.Count -ne 1) {
    throw 'Deploy must run signing and final verification as child processes so their exit codes cannot bypass the remaining gate.'
}
foreach ($required in @('candidate-final.json', 'Agent_b.exe', 'Agent_b-setup.exe', 'setup_sha256', 'setup_bytes', 'Get-AuthenticodeSignature', 'TimeStamperCertificate', 'DEPLOY REFUSED')) {
    if ($verify -notmatch [regex]::Escape($required)) { throw "Deploy verifier does not require $required." }
}
if ($sign -notmatch "@\('Agent_b\.exe', 'Agent_b-setup\.exe'\)") { throw 'Release signing does not include the setup executable.' }
if ($stage -notmatch 'manifest\.setup_sha256' -or $stage -notmatch 'manifest\.setup_bytes') { throw 'Candidate staging does not record the setup artifact.' }
foreach ($required in @('windowsPowerShellEnvironment', 'PSModulePath', 'System32", "WindowsPowerShell", "v1.0", "powershell.exe')) {
    if ($stage -notmatch [regex]::Escape($required)) { throw "Candidate staging does not preserve native Windows PowerShell host repair $required." }
}

$fixture = Join-Path ([IO.Path]::GetTempPath()) ('Agent_b-deploy-gate-' + [Guid]::NewGuid().ToString('N'))
try {
    $null = New-Item -ItemType Directory -Path $fixture -Force
    [IO.File]::WriteAllBytes((Join-Path $fixture 'Agent_b.exe'), [byte[]](1, 2, 3))
    [IO.File]::WriteAllText((Join-Path $fixture 'candidate-final.json'), '{}', [Text.UTF8Encoding]::new($false))
    try {
        & (Join-Path $PSScriptRoot 'verify-deploy-candidate.ps1') -CandidateDirectory $fixture -ExpectedTag 'v9.9.9' -ExpectedCommit ('a' * 40)
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
        & (Join-Path $PSScriptRoot 'verify-deploy-candidate.ps1') -CandidateDirectory $fixture -ExpectedTag 'v9.9.9' -ExpectedCommit ('a' * 40)
        throw 'The verifier accepted an unsigned setup executable.'
    } catch {
        if ($_.Exception.Message -notmatch 'setup Authenticode status is .*expected Valid.*setup has no Authenticode timestamp') { throw }
    }
} finally {
    if (Test-Path -LiteralPath $fixture) { Remove-TreeWithinAllowedRoots -Path $fixture -AllowedRoots @([IO.Path]::GetTempPath()) -Purpose 'deploy-gate cleanup' }
}
Write-Host 'PASS: deploy requires a built, manifested, signed, timestamped Agent_b-setup.exe matching the release tag and commit'
