[CmdletBinding()]
param()
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'removal-guard.ps1')

# v0.61.0/W6 under r2. Prove scripts/sign-release.ps1 signs, timestamps and
# produces a chain that validates -- WITHOUT installing any root into the
# operator's stores. X509Chain with CustomRootTrust verifies against an anchor
# supplied in memory, which is what "chain valid for the probe" means.

$created = @()
function Remove-Probe {
    foreach ($item in $script:created) {
        $path = "Cert:\" + $item.Location + "\" + $item.Store + "\" + $item.Thumbprint
        if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path -Force }
        Write-Output ("REMOVED  Cert:\{0}\{1}  {2}" -f $item.Location, $item.Store, $item.Thumbprint)
    }
}

try {
    $cert = New-SelfSignedCertificate -Type CodeSigningCert -Subject 'CN=Agent_b Disposable Release Signing Probe' -CertStoreLocation 'Cert:\CurrentUser\My' -KeyAlgorithm RSA -KeyLength 3072 -HashAlgorithm SHA256 -NotAfter ([DateTime]::Now.AddDays(1))
    $created += [pscustomobject]@{ Location = 'CurrentUser'; Store = 'My'; Thumbprint = $cert.Thumbprint }
    Write-Output ("CREATED  Cert:\CurrentUser\My  {0}  {1}" -f $cert.Thumbprint, $cert.Subject)
    Write-Output "NO ROOT INSTALLED: neither CurrentUser\Root nor LocalMachine\Root was written"

    $stage = Join-Path ([IO.Path]::GetTempPath()) ('agentb-w6-' + [Guid]::NewGuid().ToString('N'))
    $null = New-Item -ItemType Directory -Path $stage -Force
    try {
        Copy-Item 'C:\projects\agentb\scripts\removal-guard.ps1' (Join-Path $stage 'a.ps1')
        Copy-Item 'C:\projects\agentb\scripts\build-icon.ps1' (Join-Path $stage 'b.ps1')
        Copy-Item 'C:\projects\agentb\harness.example.json' (Join-Path $stage 'harness.example.json')
        $untouchedBefore = (Get-FileHash (Join-Path $stage 'harness.example.json') -Algorithm SHA256).Hash

        $report = Join-Path $stage 'report.json'
        & powershell.exe -NoLogo -NoProfile -File 'C:\projects\agentb\scripts\sign-release.ps1' -Path $stage -Thumbprint $cert.Thumbprint -ReportPath $report 2>&1 | Out-String | Write-Output
        Write-Output ("sign-release exit: {0}  (3 = signed and timestamped, chain not trusted by the MACHINE store, which is expected for a disposable anchor)" -f $LASTEXITCODE)

        $allSigned = $true
        $allTimestamped = $true
        $allChainValid = $true
        foreach ($file in @('a.ps1', 'b.ps1')) {
            $path = Join-Path $stage $file
            $sig = Get-AuthenticodeSignature -LiteralPath $path
            $signed = [bool]$sig.SignerCertificate
            $stamped = [bool]$sig.TimeStamperCertificate
            if (-not $signed) { $allSigned = $false }
            if (-not $stamped) { $allTimestamped = $false }

            # The anchor is supplied explicitly; the machine's stores are not
            # consulted for trust. AllowUnknownCertificateAuthority is NOT set,
            # so the chain must actually build to the supplied anchor.
            $chain = New-Object Security.Cryptography.X509Certificates.X509Chain
            $chain.ChainPolicy.TrustMode = [Security.Cryptography.X509Certificates.X509ChainTrustMode]::CustomRootTrust
            $null = $chain.ChainPolicy.CustomTrustStore.Add($cert)
            $chain.ChainPolicy.RevocationMode = [Security.Cryptography.X509Certificates.X509RevocationMode]::NoCheck
            $built = $chain.Build($sig.SignerCertificate)
            $statuses = @($chain.ChainStatus | ForEach-Object { $_.Status })
            if (-not $built) { $allChainValid = $false }
            Write-Output ("  {0}: signed={1} timestamped={2} chainValidToSuppliedAnchor={3} status={4}" -f $file, $signed, $stamped, $built, $(if ($statuses.Count) { $statuses -join ',' } else { 'NoError' }))
            $chain.Dispose()
        }
        $untouchedAfter = (Get-FileHash (Join-Path $stage 'harness.example.json') -Algorithm SHA256).Hash
        Write-Output ("  harness.example.json: untouched={0}" -f ($untouchedBefore -eq $untouchedAfter))
        Copy-Item $report 'C:\projects\agentb\logs\evidence\2026-09-15-v0.61.0\w6-disposable-signing-report.json' -Force

        if ($allSigned -and $allTimestamped -and $allChainValid -and ($untouchedBefore -eq $untouchedAfter)) {
            Write-Output 'PASS: every signable is signed, timestamped and chain-valid to the supplied anchor; non-signables untouched; no root installed'
        } else {
            Write-Output 'FAIL: see the per-file lines above'
        }
    } finally {
        try { Remove-TreeWithinAllowedRoots -Path $stage -AllowedRoots @($stage) -Purpose 'signing-probe stage cleanup' } catch { Write-Warning $_.Exception.Message }
    }
} finally {
    Remove-Probe
    foreach ($store in 'Cert:\CurrentUser\My', 'Cert:\CurrentUser\Root', 'Cert:\LocalMachine\My', 'Cert:\LocalMachine\Root') {
        $left = @(Get-ChildItem $store -ErrorAction SilentlyContinue | Where-Object { $_.Subject -like '*Disposable Release Signing Probe*' })
        Write-Output ("{0}: {1} probe certificate(s) remaining" -f $store, $left.Count)
    }
}
