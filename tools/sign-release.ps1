[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$Path,
    [string]$Thumbprint,
    [string]$PfxPath,
    [string]$PfxPasswordEnvironment = 'AGENTB_ORG_PFX_PASSWORD',
    [string]$TimestampUrl = 'http://timestamp.digicert.com',
    [string]$ReportPath,
    [switch]$Organization,
    [switch]$PayloadOnly,
    # Item 2lt: agentb.exe alone. The release path signs the payload BEFORE the
    # build, so the CLI does not exist yet when the payload pass runs -- and the
    # bundle, captured straight after the build, would embed an unsigned copy.
    # It carries no bundle of its own, so unlike Agent_b.exe it can be signed
    # between the build and the capture, which is what this is for.
    [switch]$CliOnly,
    [switch]$BinaryOnly
)

$ErrorActionPreference = 'Stop'
. (Join-Path (Split-Path -Parent $PSScriptRoot) 'scripts\signing-key-policy.ps1')
if ($Thumbprint -and $PfxPath) { throw 'supply either Thumbprint or PfxPath, not both' }

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
    if (-not [string]::IsNullOrWhiteSpace($PfxPath)) {
        $resolvedPfx = [IO.Path]::GetFullPath($PfxPath)
        if (-not (Test-Path -LiteralPath $resolvedPfx -PathType Leaf)) { throw "PFX not found: $resolvedPfx" }
        $password = [Environment]::GetEnvironmentVariable($PfxPasswordEnvironment)
        if ($null -eq $password) { throw "set $PfxPasswordEnvironment to the PFX password (an empty value is allowed)" }
        $flags = [Security.Cryptography.X509Certificates.X509KeyStorageFlags]::EphemeralKeySet
        $candidate = [Security.Cryptography.X509Certificates.X509Certificate2]::new($resolvedPfx, $password, $flags)
        return [pscustomobject]@{ Certificate = $candidate; Store = "PFX:$resolvedPfx" }
    }
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
    $reasons = @()
    # These are extension methods on the X509Certificate2 class, not members of
    # it: under Windows PowerShell 5.1 calling them on the instance fails with
    # "does not contain a method named", which would make a perfectly reachable
    # key look unreachable.
    foreach ($pair in @(
        @{ Name = 'RSA'; Type = [System.Security.Cryptography.X509Certificates.RSACertificateExtensions]; Method = 'GetRSAPrivateKey' },
        @{ Name = 'ECDsa'; Type = [System.Security.Cryptography.X509Certificates.ECDsaCertificateExtensions]; Method = 'GetECDsaPrivateKey' }
    )) {
        try {
            $key = $pair.Type::($pair.Method)($Certificate)
            if ($null -ne $key) { return [pscustomobject]@{ Usable = $true; Reason = '' } }
            $reasons += "no $($pair.Name) private key handle"
        } catch {
            $reasons += $_.Exception.Message
        }
    }
    return [pscustomobject]@{ Usable = $false; Reason = ($reasons -join '; ') }
}

function Get-SignableFiles {
    param([string]$Root)
    if ($PayloadOnly -and $BinaryOnly) { throw 'PayloadOnly and BinaryOnly are mutually exclusive.' }
    if ($Organization) {
        $files = @()
        foreach ($name in @('Agent_b-setup.exe', 'Agent_b.exe', 'agentb.exe')) {
            $candidate = Join-Path $Root $name
            if (Test-Path -LiteralPath $candidate -PathType Leaf) { $files += $candidate }
        }
        if (-not $files.Count) { throw 'no Agent_b PE files were found in the staged release' }
        return $files
    }
    if ($CliOnly) {
        $cli = Join-Path $Root 'agentb.exe'
        if (-not (Test-Path -LiteralPath $cli -PathType Leaf)) { throw "agentb.exe not found: $cli" }
        return @($cli)
    }
    $files = @()
    if (-not $PayloadOnly) {
        foreach ($name in @('Agent_b.exe', 'Agent_b-setup.exe')) {
            $binary = Join-Path $Root $name
            if (Test-Path -LiteralPath $binary -PathType Leaf) { $files += $binary }
        }
    }
    if (-not $BinaryOnly) {
        foreach ($relative in @(Get-AgentBRuntimeSigningPolicy -Root $Root).Signable) {
            $files += Join-Path $Root ($relative.Replace('/', '\'))
        }
    }
    # Item 2lt (a): agentb.exe is signed in EITHER pass, because it is both a
    # binary and a payload file. It travels in the install bundle like the loader
    # does, and the test candidate signs its payload with -PayloadOnly, which
    # skips binaries -- so listing it only among the binaries left it NotSigned
    # in every installed root. The installer matrix caught that. Test-Path means
    # each pass signs it only where it actually is.
    $cli = Join-Path $Root 'agentb.exe'
    if (Test-Path -LiteralPath $cli -PathType Leaf) { $files += $cli }
    return @($files | Sort-Object -Unique)
}

$root = [IO.Path]::GetFullPath($Path)
if (-not (Test-Path -LiteralPath $root -PathType Container)) { throw "staged candidate root not found: $root" }
if ($Organization -and -not $ReportPath) { $ReportPath = Join-Path $root 'org-signing-manifest.json' }

if ([string]::IsNullOrWhiteSpace($Thumbprint) -and [string]::IsNullOrWhiteSpace($PfxPath)) {
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
if ($certificate.NotAfter -le [DateTime]::Now.AddDays(30)) {
    Write-Host "SIGNING REFUSED: certificate $($certificate.Thumbprint) expires $($certificate.NotAfter.ToString('yyyy-MM-dd')); publisher keys must have more than 30 days remaining."
    exit 4
}
if (-not $PfxPath) { $null = Assert-SigningKeyNonInteractive -Certificate $certificate -Store $resolved.Store }
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
    files        = @()
}

if (-not $usable.Usable) {
    $report.outcome = 'unreachable'
    $report.reason = "The signing key for $($certificate.Thumbprint) is in $($resolved.Store) and cannot be opened as $($identity.Name)$(if (-not $elevated) { ' without elevation' }). Underlying: $($usable.Reason)"
    Write-Host "SIGNING REFUSED: certificate $($certificate.Thumbprint) is not usable without interaction by $($identity.Name); run the recorded one-time key grant first."
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
    $report.files += [ordered]@{
        path = $file.Substring($root.TrimEnd('\').Length + 1).Replace('\', '/')
        sha256 = (Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash.ToLowerInvariant()
        subject = $status.SignerCertificate.Subject
        thumbprint = $status.SignerCertificate.Thumbprint
        timestamped = $true
        timestamp_by = $status.TimeStamperCertificate.Subject
    }
}

# A signature changes Agent_b.exe's bytes but not the tag and commit in its build
# information, so the candidate manifest's SHA-256 follows the signed file.
$candidateManifest = Join-Path $root 'candidate-final.json'
if ((Test-Path -LiteralPath $candidateManifest -PathType Leaf) -and (Test-Path -LiteralPath (Join-Path $root 'Agent_b.exe') -PathType Leaf)) {
    $manifest = Get-Content -Raw -LiteralPath $candidateManifest | ConvertFrom-Json
    $manifest.exe_sha256 = (Get-FileHash -LiteralPath (Join-Path $root 'Agent_b.exe') -Algorithm SHA256).Hash.ToLowerInvariant()
    $manifest.exe_bytes = (Get-Item -LiteralPath (Join-Path $root 'Agent_b.exe')).Length
    $setup = Join-Path $root 'Agent_b-setup.exe'
    if (Test-Path -LiteralPath $setup -PathType Leaf) {
        $manifest | Add-Member -NotePropertyName setup_sha256 -NotePropertyValue ((Get-FileHash -LiteralPath $setup -Algorithm SHA256).Hash.ToLowerInvariant()) -Force
        $manifest | Add-Member -NotePropertyName setup_bytes -NotePropertyValue ((Get-Item -LiteralPath $setup).Length) -Force
    }
    [IO.File]::WriteAllText($candidateManifest, ($manifest | ConvertTo-Json) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
    Write-Host "MANIFEST: exe_sha256 updated to the signed Agent_b.exe"
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
