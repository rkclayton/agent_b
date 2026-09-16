[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$Path,
    [string]$Thumbprint,
    [string]$TimestampUrl = 'http://timestamp.digicert.com',
    [string]$ReportPath
)

$ErrorActionPreference = 'Stop'

# Sixteen releases reported "No provider was specified for the store or object"
# for every signable file while Settings signed the same certificate happily.
# The certificate is in LocalMachine\My; its key container is machine-scoped, so
# a non-elevated release context cannot open it, and Set-AuthenticodeSignature
# surfaces that as a provider error rather than an access one. Settings works
# because manage-signing.ps1 self-elevates before it signs.
#
# This step resolves the certificate exactly as the product does, then proves
# the key is usable before touching a single file. When it is not, it says which
# store and identity it tried instead of repeating the provider error, and it
# changes no hashes.

function Get-ReleaseCertificate {
    param([string]$Value)
    if ([string]::IsNullOrWhiteSpace($Value)) { throw 'no signing thumbprint was configured or supplied' }
    $clean = ($Value -replace '[^0-9A-Fa-f]', '').ToUpperInvariant()
    foreach ($store in @('Cert:\LocalMachine\My', 'Cert:\CurrentUser\My')) {
        $candidate = Get-ChildItem -LiteralPath (Join-Path $store $clean) -ErrorAction SilentlyContinue
        if ($candidate) { return [pscustomobject]@{ Certificate = $candidate; Store = $store } }
    }
    throw "certificate $clean is in neither LocalMachine\My nor CurrentUser\My"
}

# HasPrivateKey reports the certificate's property, not whether this identity can
# open the key. Only opening it answers that, so open it.
function Test-PrivateKeyUsable {
    param($Certificate)
    try {
        $key = [System.Security.Cryptography.X509Certificates.RSACertificateExtensions]::GetRSAPrivateKey($Certificate)
        if ($null -eq $key) { return [pscustomobject]@{ Usable = $false; Reason = 'the certificate exposes no RSA private key handle' } }
        return [pscustomobject]@{ Usable = $true; Reason = '' }
    } catch {
        return [pscustomobject]@{ Usable = $false; Reason = $_.Exception.Message }
    }
}

function Get-SignableFiles {
    param([string]$Root)
    $files = @()
    $binary = Join-Path $Root 'Agent_b.exe'
    if (Test-Path -LiteralPath $binary -PathType Leaf) { $files += $binary }
    $files += @(Get-ChildItem -LiteralPath $Root -Filter '*.ps1' -File -Recurse | ForEach-Object FullName)
    return @($files | Sort-Object -Unique)
}

$root = [IO.Path]::GetFullPath($Path)
if (-not (Test-Path -LiteralPath $root -PathType Container)) { throw "staged candidate root not found: $root" }

if ([string]::IsNullOrWhiteSpace($Thumbprint)) {
    $configPath = Join-Path $root 'harness.json'
    if (Test-Path -LiteralPath $configPath -PathType Leaf) {
        $config = Get-Content -Raw -LiteralPath $configPath | ConvertFrom-Json
        if ($config.signing) { $Thumbprint = [string]$config.signing.thumbprint }
    }
}

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
$elevated = $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)

$targets = Get-SignableFiles $root
$untrusted = @()
$before = @{}
foreach ($file in $targets) { $before[$file] = (Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash }

$resolved = Get-ReleaseCertificate -Value $Thumbprint
$certificate = $resolved.Certificate
$usable = Test-PrivateKeyUsable -Certificate $certificate

$report = [ordered]@{
    thumbprint   = $certificate.Thumbprint
    subject      = $certificate.Subject
    store        = $resolved.Store
    identity     = $identity.Name
    elevated     = $elevated
    signable     = $targets.Count
    signed       = 0
    outcome      = ''
    reason       = ''
    timestamp_url = $TimestampUrl
}

if (-not $usable.Usable) {
    $report.outcome = 'unreachable'
    # One sentence, naming the store and the identity, rather than the raw provider error.
    $report.reason = "The signing key for $($certificate.Thumbprint) is in $($resolved.Store) and cannot be opened as $($identity.Name)$(if (-not $elevated) { ' without elevation' }); Settings signs this same certificate because manage-signing.ps1 elevates first, so sign from Settings or move the certificate to Cert:\CurrentUser\My. Underlying: $($usable.Reason)"
    Write-Host "SIGNING UNREACHABLE: $($report.reason)"
    foreach ($file in $targets) {
        $after = (Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash
        if ($after -ne $before[$file]) { throw "a failed signing attempt changed $file" }
    }
    Write-Host "UNCHANGED: all $($targets.Count) signable hashes"
    if ($ReportPath) { [IO.File]::WriteAllText($ReportPath, ($report | ConvertTo-Json -Depth 5), [Text.UTF8Encoding]::new($false)) }
    exit 2
}

foreach ($file in $targets) {
    $signature = Set-AuthenticodeSignature -LiteralPath $file -Certificate $certificate -HashAlgorithm SHA256 -TimestampServer $TimestampUrl
    # A signature that was applied but whose chain this machine does not trust is
    # not a signing failure: the bytes are signed and timestamped, and the trust
    # anchor is a separate, local condition. Conflating the two is what made the
    # old message useless. Only an absent signer means the signing itself failed.
    if (-not $signature.SignerCertificate) {
        $report.outcome = 'failed'
        $report.reason = "Signing $([IO.Path]::GetFileName($file)) with $($certificate.Thumbprint) from $($resolved.Store) as $($identity.Name) applied no signature: $($signature.Status): $($signature.StatusMessage)"
        Write-Host "SIGNING FAILED: $($report.reason)"
        if ($ReportPath) { [IO.File]::WriteAllText($ReportPath, ($report | ConvertTo-Json -Depth 5), [Text.UTF8Encoding]::new($false)) }
        exit 1
    }
    if ($signature.Status -ne 'Valid') { $untrusted += ,([IO.Path]::GetFileName($file) + ': ' + $signature.Status + ' ' + $signature.StatusMessage) }
    $report.signed++
}

foreach ($file in $targets) {
    $status = Get-AuthenticodeSignature -LiteralPath $file
    if (-not $status.SignerCertificate) { throw "verification found no signature on $file after signing" }
    if (-not $status.TimeStamperCertificate) { throw "no timestamp on $file" }
}

if ($untrusted.Count) {
    $report.outcome = 'signed-untrusted-chain'
    $report.reason = "All $($report.signed) file(s) were signed and timestamped with $($certificate.Thumbprint) from $($resolved.Store), but this machine does not trust the chain: $($untrusted[0])"
    Write-Host "SIGNED, CHAIN NOT TRUSTED HERE: $($report.reason)"
    if ($ReportPath) { [IO.File]::WriteAllText($ReportPath, ($report | ConvertTo-Json -Depth 5), [Text.UTF8Encoding]::new($false)) }
    exit 3
}

$report.outcome = 'signed'
Write-Host "SIGNED: $($report.signed) file(s) with $($certificate.Thumbprint) from $($resolved.Store), timestamped and chain valid"
if ($ReportPath) { [IO.File]::WriteAllText($ReportPath, ($report | ConvertTo-Json -Depth 5), [Text.UTF8Encoding]::new($false)) }
exit 0
