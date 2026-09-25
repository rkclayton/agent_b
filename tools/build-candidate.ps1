[CmdletBinding()]
param(
    # The tree to build in: a checkout, or a clean archive staged as a candidate.
    [string]$SourceDirectory,
    # The tag the exe must report. The release step passes the tag it is about to
    # push; a mismatch fails here, never at install time.
    [string]$ExpectedTag,
    # Required when the tree is not a git checkout (a staged archive).
    [string]$Commit,
    [ValidateSet('', 'true', 'false')]
    [string]$Dirty = '',
    # Acceptance tests opt in. Release builds keep their existing signing path.
    [switch]$SignForTest,
    # Defender recovery only: admit an existing, already-signed test executable
    # without asking Go to recreate the quarantined unsigned linker output.
    [switch]$UseExistingSignedBinary
)

# Item 2eu: the release step builds the exe once, into the candidate, and records
# what it built. The installer never builds; it installs this exe only when its
# SHA-256 and embedded tag and commit equal this manifest.

$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($SourceDirectory)) { $SourceDirectory = Split-Path -Parent $PSScriptRoot }
$sourceRoot = [IO.Path]::GetFullPath($SourceDirectory)
$manifestPath = Join-Path $sourceRoot 'candidate-final.json'
$binary = Join-Path $sourceRoot 'Agent_b.exe'

function Find-Go {
    param([string]$Source)
    foreach ($candidate in @((Join-Path $Source '.tools\go\bin\go.exe'), (Join-Path (Split-Path -Parent $PSScriptRoot) '.tools\go\bin\go.exe'), 'C:\Go\bin\go.exe')) {
        if (Test-Path -LiteralPath $candidate -PathType Leaf) { return $candidate }
    }
    $command = Get-Command go.exe -ErrorAction SilentlyContinue
    if ($command) { return $command.Source }
    throw 'Go 1.24 or newer was not found; the release step builds the candidate and needs it.'
}

function Add-EmbeddedInstallBundle {
    param([string]$Root, [string]$Executable)
    Add-Type -AssemblyName System.IO.Compression
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $zipPath = Join-Path ([IO.Path]::GetTempPath()) ('Agent_b-bundle-' + [Guid]::NewGuid().ToString('N') + '.zip')
    try {
        $zip = [IO.Compression.ZipFile]::Open($zipPath, [IO.Compression.ZipArchiveMode]::Create)
        try {
            $files = @()
            foreach ($directory in @('web', 'prompts', 'docs')) {
                $files += @(Get-ChildItem -LiteralPath (Join-Path $Root $directory) -File -Recurse)
            }
            $shipManifest = Join-Path $Root 'runtime-scripts.txt'
            if (-not (Test-Path -LiteralPath $shipManifest -PathType Leaf)) { throw 'runtime-scripts.txt is missing.' }
            foreach ($relative in @(Get-Content -LiteralPath $shipManifest | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })) {
                if ($relative -notmatch '^scripts/[A-Za-z0-9_.-]+$') { throw "Invalid runtime script manifest entry: $relative" }
                $file = Join-Path $Root ($relative.Replace('/', '\'))
                if (-not (Test-Path -LiteralPath $file -PathType Leaf)) { throw "Runtime script is missing: $relative" }
                $files += Get-Item -LiteralPath $file
            }
            foreach ($name in @('WebView2Loader.dll', 'harness.example.json', 'SECURITY.md', 'LICENSE', 'NOTICE', 'runtime-scripts.txt')) {
                $files += Get-Item -LiteralPath (Join-Path $Root $name)
            }
            foreach ($file in $files) {
                $relative = $file.FullName.Substring($Root.TrimEnd('\').Length + 1).Replace('\', '/')
                $entry = $zip.CreateEntry($relative, [IO.Compression.CompressionLevel]::Optimal)
                $input = [IO.File]::OpenRead($file.FullName)
                $output = $entry.Open()
                try { $input.CopyTo($output) } finally { $output.Dispose(); $input.Dispose() }
            }
        } finally { $zip.Dispose() }
        $bundle = [IO.File]::ReadAllBytes($zipPath)
        $hasher = [Security.Cryptography.SHA256]::Create()
        try { $bundleHash = $hasher.ComputeHash($bundle) } finally { $hasher.Dispose() }
        $stream = [IO.File]::Open($Executable, [IO.FileMode]::Append, [IO.FileAccess]::Write, [IO.FileShare]::None)
        try {
            $stream.Write($bundle, 0, $bundle.Length)
            $magic = [Text.Encoding]::ASCII.GetBytes('AGENTBUNDLE0001!')
            $length = [BitConverter]::GetBytes([Int64]$bundle.Length)
            $stream.Write($magic, 0, $magic.Length)
            $stream.Write($length, 0, $length.Length)
            $stream.Write($bundleHash, 0, $bundleHash.Length)
        } finally { $stream.Dispose() }
        Write-Host "BUNDLE: embedded $($files.Count) files, $($bundle.Length) compressed bytes"
    } finally { Remove-Item -LiteralPath $zipPath -Force -ErrorAction SilentlyContinue }
}

$tagSource = Get-Content -Raw -LiteralPath (Join-Path $sourceRoot 'internal\buildinfo\buildinfo.go')
$tagMatch = [regex]::Match($tagSource, '(?m)^\s*Tag\s*=\s*"([^"]+)"')
if (-not $tagMatch.Success) { throw 'internal\buildinfo\buildinfo.go declares no Tag.' }
$sourceTag = $tagMatch.Groups[1].Value
$installerSource = Get-Content -Raw -LiteralPath (Join-Path $sourceRoot 'scripts\install-Agent_b.ps1')
$versionMatch = [regex]::Match($installerSource, "(?m)^\`$displayVersion\s*=\s*'([^']+)'")
if (-not $versionMatch.Success) { throw 'scripts\install-Agent_b.ps1 declares no displayVersion.' }
if ($sourceTag -ne ('v' + $versionMatch.Groups[1].Value)) {
    throw "buildinfo Tag $sourceTag and installer displayVersion $($versionMatch.Groups[1].Value) disagree."
}

if ([string]::IsNullOrWhiteSpace($Commit)) {
    # Windows PowerShell 5.1 turns git's stderr into a terminating error under
    # Stop; outside a checkout that must reach the explanation below instead.
    $ErrorActionPreference = 'Continue'
    # Collect the whole output first: Select-Object -First stops the pipeline
    # early and can leave $LASTEXITCODE unset.
    $topLines = @(& git -C $sourceRoot rev-parse --show-toplevel 2>$null)
    $topExit = $LASTEXITCODE
    $top = [string]($topLines | Select-Object -First 1)
    $ErrorActionPreference = 'Stop'
    if ($topExit -ne 0 -or [string]::IsNullOrWhiteSpace($top) -or
        -not [IO.Path]::GetFullPath($top.Trim()).TrimEnd('\').Equals($sourceRoot.TrimEnd('\'), [StringComparison]::OrdinalIgnoreCase)) {
        # A tree nested inside another checkout would otherwise borrow that
        # checkout's HEAD, which is exactly how a candidate got a wrong identity.
        throw "$sourceRoot is not the top of a git checkout; pass -Commit and -Dirty for a staged archive."
    }
    $Commit = [string](& git -C $sourceRoot rev-parse HEAD | Select-Object -First 1)
    if ([string]::IsNullOrWhiteSpace($Dirty)) {
        $Dirty = if (@(& git -C $sourceRoot status --porcelain --untracked-files=normal).Count -gt 0) { 'true' } else { 'false' }
    }
}
$Commit = $Commit.Trim().ToLowerInvariant()
if ($Commit -notmatch '^[0-9a-f]{40}$') { throw "Commit must be a full 40-character hash: $Commit" }
if ([string]::IsNullOrWhiteSpace($Dirty)) { throw 'Dirty must be true or false for a staged archive.' }

$go = Find-Go $sourceRoot
if (Test-Path -LiteralPath $manifestPath) { Remove-Item -LiteralPath $manifestPath -Force }
$ldflags = "-X harness/internal/buildinfo.Tag=$sourceTag -X harness/internal/buildinfo.Commit=$Commit -X harness/internal/buildinfo.Dirty=$Dirty"
if ($UseExistingSignedBinary) {
    if (-not $SignForTest) { throw '-UseExistingSignedBinary requires -SignForTest.' }
    if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) { throw "Existing signed test candidate not found: $binary" }
    $existingSignature = Get-AuthenticodeSignature -LiteralPath $binary
    if ($existingSignature.Status -ne 'Valid' -or -not $existingSignature.TimeStamperCertificate -or
        $existingSignature.SignerCertificate.Subject -ne 'CN=Agent_b Disposable Test Signing') {
        throw 'EXISTING TEST CANDIDATE REFUSED: a valid, timestamped disposable-test signature is required.'
    }
} else {
    # `go build -o` may leave an existing, newer output in place when the
    # package cache says no rebuild is needed. That would append a second
    # bundle (and possibly append it after an old Authenticode certificate),
    # producing an invalid setup. The binary is generated output owned by this
    # script, so always begin the build path without it.
    if (Test-Path -LiteralPath $binary -PathType Leaf) {
        Remove-Item -LiteralPath $binary -Force
    }
    Push-Location $sourceRoot
    try { & $go build -ldflags $ldflags -o $binary ./cmd/harness } finally { Pop-Location }
    if ($LASTEXITCODE -ne 0) { throw "go build exited $LASTEXITCODE." }
    $payloadRoot = $sourceRoot
    $testPayloadRoot = $null
    if ($SignForTest) {
        # Never Authenticode-sign tracked working-tree scripts in place. Build
        # the disposable payload from a private copy, sign that copy, and then
        # capture exactly those signed bytes.
        $testPayloadRoot = Join-Path ([IO.Path]::GetTempPath()) ('Agent_b-signed-payload-' + [Guid]::NewGuid().ToString('N'))
        $null = New-Item -ItemType Directory -Path $testPayloadRoot
        foreach ($directory in @('web', 'prompts', 'docs')) {
            Copy-Item -LiteralPath (Join-Path $sourceRoot $directory) -Destination (Join-Path $testPayloadRoot $directory) -Recurse
        }
        foreach ($name in @('WebView2Loader.dll', 'harness.example.json', 'SECURITY.md', 'LICENSE', 'NOTICE', 'runtime-scripts.txt')) {
            Copy-Item -LiteralPath (Join-Path $sourceRoot $name) -Destination (Join-Path $testPayloadRoot $name)
        }
        foreach ($relative in @(Get-Content -LiteralPath (Join-Path $sourceRoot 'runtime-scripts.txt'))) {
            $from = Join-Path $sourceRoot ($relative.Replace('/', '\'))
            $to = Join-Path $testPayloadRoot ($relative.Replace('/', '\'))
            $null = New-Item -ItemType Directory -Path (Split-Path -Parent $to) -Force
            Copy-Item -LiteralPath $from -Destination $to
        }
        & (Join-Path $sourceRoot 'tools\sign-test-candidate.ps1') -SourceDirectory $testPayloadRoot -PayloadOnly
        if ($LASTEXITCODE -ne 0) { throw "Disposable payload signing exited $LASTEXITCODE." }
        $payloadRoot = $testPayloadRoot
    }
    try { Add-EmbeddedInstallBundle -Root $payloadRoot -Executable $binary } finally {
        if ($testPayloadRoot -and (Test-Path -LiteralPath $testPayloadRoot)) {
            $resolvedPayload = [IO.Path]::GetFullPath($testPayloadRoot)
            if ((Split-Path -Leaf $resolvedPayload) -notlike 'Agent_b-signed-payload-*') { throw "Refusing unexpected payload cleanup: $resolvedPayload" }
            Remove-Item -LiteralPath $resolvedPayload -Recurse -Force
        }
    }

    # A test build must be signed before anything executes it, including the
    # identity probe below. This ordering is intentional: signing after
    # -version would still expose the raw Go output to real-time protection.
    if ($SignForTest) {
        & (Join-Path $sourceRoot 'tools\sign-test-candidate.ps1') -SourceDirectory $sourceRoot -BinaryOnly
    }
}

$reported = (& $binary -version | Out-String) | ConvertFrom-Json
if ($LASTEXITCODE -ne 0) { throw "Agent_b.exe -version exited $LASTEXITCODE." }
$sha = (Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash.ToLowerInvariant()
$failures = @()
if ($reported.tag -ne $sourceTag) { $failures += "tag $($reported.tag) (expected $sourceTag)" }
if ($ExpectedTag -and $reported.tag -ne $ExpectedTag) { $failures += "tag $($reported.tag) (release is $ExpectedTag)" }
if ($reported.commit -ne $Commit) { $failures += "commit $($reported.commit) (expected $Commit)" }
if ([string]([bool]$reported.dirty).ToString().ToLowerInvariant() -ne $Dirty) { $failures += "dirty $($reported.dirty) (expected $Dirty)" }
if ($reported.source -ne 'ldflags') { $failures += "identity source $($reported.source) (expected ldflags)" }
if ($reported.executable_sha256 -ne $sha) { $failures += "reported sha256 $($reported.executable_sha256) (file is $sha)" }
if ($ExpectedTag -and [bool]$reported.dirty) { $failures += 'a dirty tree (a release is built from a clean commit)' }
# The installer reads the identity from the -ldflags text in the Go build
# information without running the exe; prove that text is there (it is not
# under -trimpath), so a candidate that passes here cannot fail there.
$text = [Text.Encoding]::GetEncoding(28591).GetString([IO.File]::ReadAllBytes($binary))
$embeddedTag = [regex]::Match($text, '-X harness/internal/buildinfo\.Tag=(v[0-9A-Za-z.+-]+)')
$embeddedCommit = [regex]::Match($text, '-X harness/internal/buildinfo\.Commit=([0-9a-f]{40})')
if (-not $embeddedTag.Success -or $embeddedTag.Groups[1].Value -ne $sourceTag -or -not $embeddedCommit.Success -or $embeddedCommit.Groups[1].Value -ne $Commit) {
    $failures += 'build information the installer can read without running it (no matching -ldflags text; was -trimpath set?)'
}
if ($failures.Count) { throw "CANDIDATE BUILD REFUSED: Agent_b.exe reports $($failures -join '; '). No manifest was written." }

# Item 2hc (v1.3.0/W2): the release step pins the one native file we ship and
# did not build. A candidate whose loader does not match scripts/webview2-loader
# .json by BOTH hash and Authenticode signature is refused here, before it can
# reach an installer - the installer checks again, because the candidate can be
# copied between machines after this step.
$loaderPinPath = Join-Path $sourceRoot 'scripts\webview2-loader.json'
if (-not (Test-Path -LiteralPath $loaderPinPath -PathType Leaf)) { throw 'scripts\webview2-loader.json is missing; the WebView2 loader cannot be pinned.' }
$loaderPin = Get-Content -Raw -LiteralPath $loaderPinPath | ConvertFrom-Json
$loaderPath = Join-Path $sourceRoot $loaderPin.file
if (-not (Test-Path -LiteralPath $loaderPath -PathType Leaf)) { throw "$($loaderPin.file) is missing from $sourceRoot." }
$loaderSha = (Get-FileHash -LiteralPath $loaderPath -Algorithm SHA256).Hash.ToLowerInvariant()
if ($loaderSha -ne ([string]$loaderPin.sha256).ToLowerInvariant()) {
    throw "LOADER REFUSED: $($loaderPin.file) is sha256 $loaderSha; scripts\webview2-loader.json names $($loaderPin.sha256)."
}
$loaderSignature = Get-AuthenticodeSignature -LiteralPath $loaderPath
if ($loaderSignature.Status -ne 'Valid') { throw "LOADER REFUSED: $($loaderPin.file) Authenticode status is $($loaderSignature.Status), expected Valid." }
if ([string]$loaderSignature.SignerCertificate.Thumbprint -ne [string]$loaderPin.signature.thumbprint) {
    throw "LOADER REFUSED: $($loaderPin.file) signer thumbprint is $($loaderSignature.SignerCertificate.Thumbprint); the pin names $($loaderPin.signature.thumbprint)."
}
Write-Host "LOADER: $($loaderPin.file) $($loaderPin.file_version) sha256 $loaderSha, $($loaderPin.signature.subject)"

$manifest = [ordered]@{
    schema     = 1
    tag        = $reported.tag
    commit     = $reported.commit
    dirty      = [bool]$reported.dirty
    display    = $reported.display
    exe_sha256 = $sha
    exe_bytes  = (Get-Item -LiteralPath $binary).Length
    built_at   = (Get-Date).ToUniversalTime().ToString('o')
    webview2_loader = [ordered]@{
        file       = [string]$loaderPin.file
        version    = [string]$loaderPin.file_version
        sha256     = $loaderSha
        package    = [string]$loaderPin.package + ' ' + [string]$loaderPin.package_version
        thumbprint = [string]$loaderPin.signature.thumbprint
    }
}
if ($SignForTest) {
    $testSignature = Get-AuthenticodeSignature -LiteralPath $binary
    if ($testSignature.Status -ne 'Valid' -or -not $testSignature.TimeStamperCertificate) {
        throw 'SIGNED TEST CANDIDATE REFUSED: Authenticode signature or timestamp no longer verifies.'
    }
    $manifest['test_signature'] = [ordered]@{
        thumbprint = $testSignature.SignerCertificate.Thumbprint
        subject = $testSignature.SignerCertificate.Subject
        timestamped = $true
    }
}
[IO.File]::WriteAllText($manifestPath, ($manifest | ConvertTo-Json) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
Write-Host "CANDIDATE: Agent_b.exe $($manifest.tag) $($manifest.display) sha256 $sha"
Write-Host "MANIFEST: $manifestPath"
exit 0
