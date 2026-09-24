[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)][string]$SourceDirectory,
    [string]$Thumbprint,
    [string]$TimestampUrl = 'http://timestamp.digicert.com'
)

# Disposable executable tests must not launch the raw, reputationless Go
# output. This signs only Agent_b.exe (never the source scripts) with an
# already trusted CurrentUser certificate. If a manifest already exists it is
# updated to follow the signed bytes. It creates or trusts no certificate and
# never elevates.
$ErrorActionPreference = 'Stop'
. (Join-Path (Split-Path -Parent $PSScriptRoot) 'scripts\signing-key-policy.ps1')
$root = [IO.Path]::GetFullPath($SourceDirectory)
$binary = Join-Path $root 'Agent_b.exe'
$manifestPath = Join-Path $root 'candidate-final.json'
if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) { throw "test candidate not found: $binary" }

$certificates = @(Get-ChildItem Cert:\CurrentUser\My -CodeSigningCert | Where-Object {
    $_.HasPrivateKey -and $_.Subject -eq 'CN=Agent_b Disposable Test Signing' -and
    (Test-Path -LiteralPath ("Cert:\CurrentUser\Root\{0}" -f $_.Thumbprint)) -and
    (Test-Path -LiteralPath ("Cert:\CurrentUser\TrustedPublisher\{0}" -f $_.Thumbprint))
})
if ($Thumbprint) {
    $clean = ($Thumbprint -replace '[^0-9A-Fa-f]', '').ToUpperInvariant()
    $certificates = @($certificates | Where-Object { $_.Thumbprint -eq $clean })
}
$certificate = $certificates | Sort-Object NotAfter, Thumbprint -Descending | Select-Object -First 1
if (-not $certificate) {
    throw 'No already-trusted CurrentUser Agent_b disposable test-signing certificate with a private key is available. Test candidate was not launched.'
}

$null = Assert-SigningKeyNonInteractive -Certificate $certificate -Store 'Cert:\CurrentUser\My'
try {
    $key = [System.Security.Cryptography.X509Certificates.RSACertificateExtensions]::GetRSAPrivateKey($certificate)
    if (-not $key) { throw 'the certificate exposes no RSA private key handle' }
    $key.Dispose()
} catch {
    throw "The CurrentUser signing key $($certificate.Thumbprint) is not usable without interaction: $($_.Exception.Message)"
}

$signature = Set-AuthenticodeSignature -LiteralPath $binary -Certificate $certificate -HashAlgorithm SHA256 -TimestampServer $TimestampUrl
if (-not $signature.SignerCertificate -or $signature.Status -ne 'Valid' -or -not $signature.TimeStamperCertificate) {
    throw "Test candidate signing failed: $($signature.Status) $($signature.StatusMessage)"
}
$verified = Get-AuthenticodeSignature -LiteralPath $binary
if ($verified.Status -ne 'Valid' -or $verified.SignerCertificate.Thumbprint -ne $certificate.Thumbprint -or -not $verified.TimeStamperCertificate) {
    throw 'Test candidate signature did not verify after signing.'
}

if (Test-Path -LiteralPath $manifestPath -PathType Leaf) {
    $manifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json
    $manifest.exe_sha256 = (Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash.ToLowerInvariant()
    $manifest.exe_bytes = (Get-Item -LiteralPath $binary).Length
    $manifest | Add-Member -Force -NotePropertyName test_signature -NotePropertyValue ([ordered]@{
        thumbprint = $certificate.Thumbprint
        subject = $certificate.Subject
        timestamped = $true
    })
    [IO.File]::WriteAllText($manifestPath, ($manifest | ConvertTo-Json -Depth 8) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
}
Write-Host "SIGNED TEST CANDIDATE: Agent_b.exe with $($certificate.Thumbprint), timestamped"
exit 0
