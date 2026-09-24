[CmdletBinding()]
param(
    # Test-only additive row. It cannot skip the real inventory; it proves the
    # release gate names and refuses a prompting thumbprint without creating a
    # certificate or opening a key.
    [string]$FixturePromptingThumbprint = ''
)

$ErrorActionPreference = 'Stop'
. (Join-Path (Split-Path -Parent $PSScriptRoot) 'scripts\signing-key-policy.ps1')

$count = 0
foreach ($store in @('Cert:\CurrentUser\My', 'Cert:\LocalMachine\My')) {
    foreach ($certificate in Get-ChildItem -LiteralPath $store -CodeSigningCert -ErrorAction Stop) {
        if (-not $certificate.HasPrivateKey) { continue }
        $null = Assert-SigningKeyNonInteractive -Certificate $certificate -Store $store
        $count++
    }
}
if ($FixturePromptingThumbprint) {
    $null = Assert-SigningKeyPolicy -Thumbprint $FixturePromptingThumbprint -Store 'Cert:\CurrentUser\My' -Policy ([pscustomobject]@{ State = 'prompting' })
}
Write-Host "SIGNING KEY POLICY PASS: $count code-signing private key(s) passed the silent policy gate"
