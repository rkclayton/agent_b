[CmdletBinding(SupportsShouldProcess=$true, ConfirmImpact='High')]
param()

# Developer acceptance only. This creates a CurrentUser-scoped, non-exportable
# key that does not request interactive key consent, and trusts only its public
# certificate in this user's Root and TrustedPublisher stores. Production uses
# the separate administrator-gated LocalMachine identity.
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'signing-key-policy.ps1')
$subject = 'CN=Agent_b Disposable Test Signing'
$existing = @(Get-ChildItem Cert:\CurrentUser\My -CodeSigningCert | Where-Object {
    $_.Subject -eq $subject -and $_.HasPrivateKey -and $_.NotAfter -gt [DateTime]::Now
})
if ($existing.Count) {
    throw "A disposable test-signing certificate already exists ($($existing[0].Thumbprint)); refusing to create another."
}
if (-not $PSCmdlet.ShouldProcess('CurrentUser certificate stores', 'Create and trust a non-exportable disposable Agent_b test-signing certificate')) {
    return
}

$certificate = $null
$certificatePath = $null
$trustedPublisherPath = $null
$rootPath = $null
$temporaryCertificate = Join-Path ([IO.Path]::GetTempPath()) ('agentb-test-signing-' + [guid]::NewGuid().ToString('N') + '.cer')
try {
    $certificate = New-SelfSignedCertificate -Type CodeSigningCert `
        -Subject $subject `
        -FriendlyName 'Agent_b disposable test signing' `
        -CertStoreLocation 'Cert:\CurrentUser\My' `
        -Provider 'Microsoft Software Key Storage Provider' `
        -KeyAlgorithm RSA -KeyLength 3072 -HashAlgorithm SHA256 `
        -KeyExportPolicy NonExportable -KeyProtection None `
        -NotAfter ([DateTime]::Now.AddYears(1))
    $certificatePath = "Cert:\CurrentUser\My\$($certificate.Thumbprint)"

    # Prove the key policy is non-interactive before opening it for the signing probe.
    $null = Assert-SigningKeyNonInteractive -Certificate $certificate -Store 'Cert:\CurrentUser\My'
    $key = [Security.Cryptography.X509Certificates.RSACertificateExtensions]::GetRSAPrivateKey($certificate)
    if (-not $key) { throw 'The new certificate exposes no RSA private key.' }
    try {
        $probe = [Text.Encoding]::UTF8.GetBytes('Agent_b disposable signing-key probe')
        $null = $key.SignData($probe, [Security.Cryptography.HashAlgorithmName]::SHA256, [Security.Cryptography.RSASignaturePadding]::Pkcs1)
    } finally {
        $key.Dispose()
    }

    $null = Export-Certificate -Cert $certificate -FilePath $temporaryCertificate -Type CERT
    $trusted = Import-Certificate -FilePath $temporaryCertificate -CertStoreLocation 'Cert:\CurrentUser\TrustedPublisher'
    $trustedPublisherPath = "Cert:\CurrentUser\TrustedPublisher\$($trusted.Thumbprint)"
    $root = Import-Certificate -FilePath $temporaryCertificate -CertStoreLocation 'Cert:\CurrentUser\Root'
    $rootPath = "Cert:\CurrentUser\Root\$($root.Thumbprint)"

    Write-Host "CREATED DISPOSABLE TEST SIGNING CERTIFICATE: $($certificate.Thumbprint)"
} catch {
    # Roll back only the exact entries created by this invocation.
    foreach ($path in @($trustedPublisherPath, $rootPath, $certificatePath)) {
        if ($path -and (Test-Path -LiteralPath $path)) { Remove-Item -LiteralPath $path -Force }
    }
    throw
} finally {
    if (Test-Path -LiteralPath $temporaryCertificate) { Remove-Item -LiteralPath $temporaryCertificate -Force }
}
