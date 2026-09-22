[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'signing-key-policy.ps1')
. (Join-Path $PSScriptRoot 'removal-guard.ps1')

$root = Join-Path ([IO.Path]::GetTempPath()) ('agentb-key-policy-' + [guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $root
$target = Join-Path $root 'unchanged.bin'
try {
    [IO.File]::WriteAllText($target, 'hash must stay unchanged', [Text.UTF8Encoding]::new($false))
    $before = (Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash
    $message = ''
    try {
        $null = Assert-SigningKeyPolicy -Thumbprint '001122AABB' -Store 'Cert:\CurrentUser\My' -Policy ([pscustomobject]@{ State = 'prompting' })
        throw 'the prompting policy was accepted'
    } catch {
        $message = $_.Exception.Message
    }
    $expected = 'SIGNING KEY REFUSED: certificate 001122AABB in Cert:\CurrentUser\My may require interactive private-key UI; no private key was opened.'
    if ($message -ne $expected) { throw "unexpected refusal: $message" }
    $after = (Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash
    if ($after -ne $before) { throw 'policy refusal changed the target hash' }
    Write-Host $message
    $gate = Join-Path $PSScriptRoot 'test-signing-key-policies.ps1'
    $savedPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $gateOutput = @(& (Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe') -NoLogo -NoProfile -ExecutionPolicy Bypass -File $gate -FixturePromptingThumbprint 'AABBCCDD' 2>&1)
        $gateExit = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $savedPreference
    }
    if ($gateExit -eq 0) { throw 'the release-gate policy check accepted a prompting fixture' }
    if (($gateOutput -join "`n") -notmatch 'SIGNING KEY REFUSED: certificate AABBCCDD') {
        throw "the release-gate refusal did not name the fixture thumbprint: $($gateOutput -join ' | ')"
    }
    Write-Host 'PASS: prompting policy refused before private-key access; target hash unchanged; no certificate or dialog created'
    Write-Host 'PASS: release-gate policy check failed and named the prompting fixture thumbprint'
} finally {
    Remove-TreeWithinAllowedRoots -Path $root -AllowedRoots @([IO.Path]::GetTempPath()) -Purpose 'signing policy fixture cleanup'
}
