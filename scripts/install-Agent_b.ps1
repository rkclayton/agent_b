[CmdletBinding(SupportsShouldProcess = $true)]
param(
    [string]$SourceDirectory,
    [string]$ApplicationDirectory = (Join-Path $env:ProgramFiles 'Agent_b'),
    [string]$DataDirectory,
    [string]$WorkspaceDirectory = (Join-Path $env:ProgramData 'Agent_b\workspace'),
    [string]$StartMenuDirectory = (Join-Path ([Environment]::GetFolderPath('StartMenu')) 'Programs'),
    [string]$UninstallRegistryPath = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\Agent_b',
    [string]$OperatorSid,
    [string]$OperatorLocalAppData,
    [string]$SigningThumbprint,
    # Test-only registry roots used by the singleton-registration scenarios.
    [string[]]$RegistrationSearchRoots,
    [switch]$TestMode,
    [switch]$ForcePostStopVerificationFailure,
    [string]$TranscriptPath,
    # Item 2gl (v1.2.6): the install writes its OWN progress. It used to be
    # written by the wrapper reading this script's output, so closing the window
    # the wrapper lived in stopped the readout even though the install carried
    # on - the operator saw a stalled Setup page for an install that was still
    # running. The file is the record; whoever is watching reads it.
    [string]$ProgressFile
)

$ErrorActionPreference = 'Stop'
$displayVersion = '1.5.0'

# Write-InstallProgress appends one JSONL line the Setup page can render. It
# never fails the install: the install is the point, the readout is not.
function Write-InstallProgress {
    param([string]$Phase, [string]$Text, [switch]$Done, [switch]$OK)
    if ([string]::IsNullOrWhiteSpace($ProgressFile)) { return }
    try {
        $entry = [ordered]@{ at = (Get-Date).ToUniversalTime().ToString('o'); phase = $Phase; text = $Text }
        if ($Done) { $entry.done = $true }
        if ($OK) { $entry.ok = $true }
        $line = ($entry | ConvertTo-Json -Compress)
        $null = New-Item -ItemType Directory -Force -Path (Split-Path -Parent $ProgressFile) -ErrorAction SilentlyContinue
        [IO.File]::AppendAllText($ProgressFile, $line + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
    } catch { }
}
. (Join-Path $PSScriptRoot 'removal-guard.ps1')
. (Join-Path $PSScriptRoot 'agentb-stop.ps1')
. (Join-Path $PSScriptRoot 'install-registration.ps1')

function Get-FullPath {
    param([string]$Path)
    return [IO.Path]::GetFullPath([Environment]::ExpandEnvironmentVariables($Path)).TrimEnd('\')
}

function Stop-InstallTranscript {
    if (-not $script:installTranscriptStarted) { return }
    $savedWhatIf = $WhatIfPreference
    $WhatIfPreference = $false
    try { $null = Stop-Transcript } finally {
        $WhatIfPreference = $savedWhatIf
        $script:installTranscriptStarted = $false
    }
}

function Start-InstallTranscript {
    param([string]$Path)
    $parent = Split-Path -Parent $Path
    if (-not (Test-Path -LiteralPath $parent -PathType Container)) {
        $null = New-Item -ItemType Directory -Path $parent -Force
    }
    $savedWhatIf = $WhatIfPreference
    $WhatIfPreference = $false
    try {
        $null = Start-Transcript -LiteralPath $Path -Append
        $script:installTranscriptStarted = $true
    } finally { $WhatIfPreference = $savedWhatIf }
    Write-Host "Transcript: $Path"
}

function Test-IsAdministrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Add-CurrentUserCertificate {
    param(
        [Security.Cryptography.X509Certificates.X509Certificate2]$Certificate,
        [Security.Cryptography.X509Certificates.StoreName]$StoreName
    )
    $public = [Security.Cryptography.X509Certificates.X509Certificate2]::new($Certificate.RawData)
    $store = [Security.Cryptography.X509Certificates.X509Store]::new($StoreName, [Security.Cryptography.X509Certificates.StoreLocation]::CurrentUser)
    try {
        $store.Open([Security.Cryptography.X509Certificates.OpenFlags]::ReadWrite)
        $store.Add($public)
    } finally {
        $store.Close()
        $public.Dispose()
    }
}

function Quote-ProcessArgument {
    param([string]$Value)
    return '"' + $Value.Replace('"', '\"') + '"'
}

function Assert-SafeAgentBPath {
    param([string]$Path, [string]$Purpose)
    $full = Get-FullPath $Path
    $root = [IO.Path]::GetPathRoot($full).TrimEnd('\')
    if ($full -eq $root -or (Split-Path -Leaf $full) -notin @('Agent_b', 'workspace')) {
        throw "$Purpose must name a dedicated Agent_b or workspace directory: $full"
    }
    return $full
}

function Assert-SafeRegistryPath {
    param([string]$Path)
    $normalized = $Path.Replace('/', '\')
    $requiredPrefix = 'HKCU:\Software\'
    $leaf = $normalized.Substring($normalized.LastIndexOf('\') + 1)
    if (-not $normalized.StartsWith($requiredPrefix, [StringComparison]::OrdinalIgnoreCase) -or
        -not $leaf.StartsWith('Agent_b', [StringComparison]::Ordinal)) {
        throw "UninstallRegistryPath must be a dedicated Agent_b key below $requiredPrefix $Path"
    }
}

function Assert-ScriptExitCode {
    param([string]$Purpose, $Code)
    if ($null -eq $Code) { throw "$Purpose failed: no exit code. The invoked script returned without setting one." }
    if ($Code -ne 0) { throw "$Purpose failed with exit code $Code." }
}

function Assert-TestPath {
    param([string]$Path)
    if (-not $TestMode) { return }
    $full = Get-FullPath $Path
    $temp = Get-FullPath ([IO.Path]::GetTempPath())
    if (-not $full.StartsWith($temp + '\', [StringComparison]::OrdinalIgnoreCase)) {
        throw "TestMode paths must stay beneath the temporary directory: $full"
    }
}

function Test-PathInside {
    param([string]$Child, [string]$Parent)
    return $Child.StartsWith($Parent.TrimEnd('\') + '\', [StringComparison]::OrdinalIgnoreCase)
}

function Assert-DisjointRoots {
    param([string[]]$Roots)
    for ($left = 0; $left -lt $Roots.Count; $left++) {
        for ($right = $left + 1; $right -lt $Roots.Count; $right++) {
            if ($Roots[$left].Equals($Roots[$right], [StringComparison]::OrdinalIgnoreCase) -or
                (Test-PathInside $Roots[$left] $Roots[$right]) -or
                (Test-PathInside $Roots[$right] $Roots[$left])) {
                throw 'Application, operator-data, and workspace directories must be three disjoint trees.'
            }
        }
    }
}

# Item 2eu: the installer never builds. The release step (build-candidate.ps1)
# builds the exe once and writes candidate-final.json beside it; the installer
# installs that exe only when its SHA-256 and the tag and commit embedded in its
# Go build information equal the manifest, and refuses before stopping anything.
function Get-CandidateExeIdentity {
    param([string]$Path)
    $bytes = [IO.File]::ReadAllBytes($Path)
    $hasher = [Security.Cryptography.SHA256]::Create()
    try { $sha = ([BitConverter]::ToString($hasher.ComputeHash($bytes)) -replace '-', '').ToLowerInvariant() } finally { $hasher.Dispose() }
    # The -ldflags the exe was built with are plain text in its Go build
    # information, so the identity is read without running the exe.
    $text = [Text.Encoding]::GetEncoding(28591).GetString($bytes)
    $tag = [regex]::Match($text, '-X harness/internal/buildinfo\.Tag=(v[0-9A-Za-z.+-]+)')
    $commit = [regex]::Match($text, '-X harness/internal/buildinfo\.Commit=([0-9a-f]{40})')
    return [pscustomobject]@{
        Tag = $(if ($tag.Success) { $tag.Groups[1].Value } else { 'no-embedded-tag' })
        Commit = $(if ($commit.Success) { $commit.Groups[1].Value } else { 'no-embedded-commit' })
        Sha256 = $sha
    }
}

# Item 2hc (v1.3.0/W2): the one native file Agent_b ships that it did not
# build. The WebView2 runtime does not provide a loader, so every WebView2
# application carries its own; ours is Microsoft's, from their SDK package, and
# it is verified by BOTH its SHA-256 and its Authenticode signature BEFORE it is
# copied. Either check failing refuses the install outright rather than
# installing something unverified beside the exe - a DLL beside an executable is
# loaded before anything on the search path, so this is the file an attacker
# would most want to replace.
function Assert-WebView2Loader {
    param([string]$SourceRoot, [switch]$AfterStop)
    $pinPath = Join-Path $SourceRoot 'scripts\webview2-loader.json'
    $rule = $(if ($AfterStop) { 'Agent_b was already stopped; the previous version is restored and restarted.' } else { 'Nothing was stopped or changed.' })
    if (-not (Test-Path -LiteralPath $pinPath -PathType Leaf)) {
        throw "LOADER REFUSED: scripts\webview2-loader.json is missing, so the WebView2 loader cannot be verified. $rule"
    }
    $pin = Get-Content -Raw -LiteralPath $pinPath | ConvertFrom-Json
    $loader = Join-Path $SourceRoot $pin.file
    if (-not (Test-Path -LiteralPath $loader -PathType Leaf)) {
        throw "LOADER REFUSED: $($pin.file) is missing from the candidate. $rule"
    }
    # NOT Get-FileHash: under -WhatIf it returns nothing and the .Hash below
    # would be a method call on null, which is how the WhatIf install first
    # failed. Get-CandidateExeIdentity reads the bytes itself for the same
    # reason; this follows it.
    $loaderBytes = [IO.File]::ReadAllBytes($loader)
    $hasher = [Security.Cryptography.SHA256]::Create()
    try { $sha = ([BitConverter]::ToString($hasher.ComputeHash($loaderBytes)) -replace '-', '').ToLowerInvariant() } finally { $hasher.Dispose() }
    if ($sha -ne ([string]$pin.sha256).ToLowerInvariant()) {
        throw "LOADER REFUSED: $($pin.file) is sha256 $sha; the pin names $($pin.sha256). $rule"
    }
    $signature = Get-AuthenticodeSignature -LiteralPath $loader
    if ($signature.Status -ne 'Valid') {
        throw "LOADER REFUSED: $($pin.file) Authenticode status is $($signature.Status), expected Valid. $rule"
    }
    $thumbprint = [string]$signature.SignerCertificate.Thumbprint
    if ($thumbprint -ne [string]$pin.signature.thumbprint) {
        throw "LOADER REFUSED: $($pin.file) is signed by thumbprint $thumbprint; the pin names $($pin.signature.thumbprint). $rule"
    }
    if ([string]$signature.SignerCertificate.Subject -ne [string]$pin.signature.subject) {
        throw "LOADER REFUSED: $($pin.file) signer is $($signature.SignerCertificate.Subject); the pin names $($pin.signature.subject). $rule"
    }
    Write-Host "PROOF WebView2 loader: $($pin.file) $($pin.file_version) sha256 $sha, signed by $($pin.signature.subject), matches scripts\webview2-loader.json"
    return $pin
}

function Assert-CandidateIdentity {
    param([string]$SourceRoot, [string]$Binary, [string]$Version, [switch]$AfterStop)
    $expectedTag = 'v' + $Version
    $manifestPath = Join-Path $SourceRoot 'candidate-final.json'
    $rule = 'The installer never builds: the release step (scripts\build-candidate.ps1) builds Agent_b.exe and writes candidate-final.json beside it. ' + $(if ($AfterStop) { 'Agent_b was already stopped; the previous version is restored and restarted.' } else { 'Nothing was stopped or changed.' })
    if (-not (Test-Path -LiteralPath $Binary -PathType Leaf)) { throw "CANDIDATE REFUSED: $Binary is missing. $rule" }
    $found = Get-CandidateExeIdentity -Path $Binary
    $foundText = "$($found.Tag) $($found.Commit) sha256 $($found.Sha256)"
    if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
        throw "CANDIDATE REFUSED: expected a candidate-final.json beside $Binary; found Agent_b.exe $foundText and no manifest. $rule"
    }
    try { $manifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json } catch {
        throw "CANDIDATE REFUSED: $manifestPath is not valid JSON ($($_.Exception.Message)); found Agent_b.exe $foundText. $rule"
    }
    $expectedText = "$($manifest.tag) $($manifest.commit) sha256 $($manifest.exe_sha256)"
    $problems = @()
    if ($manifest.schema -ne 1) { $problems += "manifest schema $($manifest.schema) is not 1" }
    if ([string]$manifest.tag -ne $expectedTag) { $problems += "the manifest names $($manifest.tag) but this installer is $expectedTag" }
    if ([string]$manifest.exe_sha256 -ne $found.Sha256) { $problems += 'the SHA-256 differs' }
    if ($found.Tag -ne [string]$manifest.tag) { $problems += 'the embedded tag differs' }
    if ($found.Commit -ne [string]$manifest.commit) { $problems += 'the embedded commit differs' }
    if ($problems.Count) {
        throw "CANDIDATE REFUSED: expected Agent_b.exe $expectedText (candidate-final.json; installer $expectedTag); found $foundText at $Binary; $($problems -join '; '). $rule"
    }
    Write-Host "CANDIDATE: Agent_b.exe $($manifest.tag) $($manifest.display) sha256 $($found.Sha256) matches candidate-final.json"
}

# v0.65.0's installer suite failed once copying Agent_b.exe a moment after the
# old process reported STOPPED ("being used by another process"). The copy
# waits, bounded, for the file lock instead.
function Wait-FileUnlocked {
    param([string]$Path, [int]$Seconds = 20)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return }
    $deadline = [DateTime]::UtcNow.AddSeconds($Seconds)
    $waited = $false
    while ($true) {
        try {
            $stream = [IO.File]::Open($Path, [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
            $stream.Dispose()
            if ($waited) { Write-Host "UNLOCKED: $Path" }
            return
        } catch {
            if ([DateTime]::UtcNow -ge $deadline) { throw "$Path is still in use by another process after $Seconds seconds." }
            if (-not $waited) { Write-Host "WAITING: $Path is in use by another process; waiting up to $Seconds seconds."; $waited = $true }
            Start-Sleep -Milliseconds 250
        }
    }
}

function Get-InstalledProcesses {
    param([string]$Executable)
    $matches = @()
    foreach ($process in Get-Process -Name 'Agent_b' -ErrorAction SilentlyContinue) {
        try {
            if ((Get-FullPath $process.Path).Equals((Get-FullPath $Executable), [StringComparison]::OrdinalIgnoreCase)) {
                $matches += $process
            }
        } catch { }
    }
    return @($matches)
}

function Stop-InstalledProcesses {
    param([System.Diagnostics.Process[]]$Processes)
    if (-not $Processes.Count) { return }
    foreach ($process in $Processes) {
        Write-InstallProgress -Phase 'stopping the running application' -Text "STOPPING: Agent_b PID $($process.Id)"
        Write-Host "STOPPING: Agent_b PID $($process.Id)"
        try {
            $channel = Request-AgentbGracefulStop -ProcessId $process.Id
        } catch {
            throw "Agent_b PID $($process.Id) could not be stopped gracefully ($($_.Exception.Message)). Installation was not changed."
        }
        Write-Host "STOP REQUESTED: Agent_b PID $($process.Id) through the $channel"
    }
    $deadline = [DateTime]::UtcNow.AddSeconds(15)
    do {
        $remaining = @($Processes | Where-Object { Get-Process -Id $_.Id -ErrorAction SilentlyContinue })
        if (-not $remaining.Count) { break }
        Start-Sleep -Milliseconds 200
    } while ([DateTime]::UtcNow -lt $deadline)
    if ($remaining.Count) {
        throw "Agent_b PID(s) $(@($remaining.Id) -join ', ') did not exit after the graceful stop signal. Installation was not changed."
    }
    Write-Host "STOPPED: Agent_b PID(s) $(@($Processes.Id) -join ', ')"
}

function Copy-ProgramDirectory {
    param([string]$Name, [string]$Source, [string]$Destination, [string[]]$AllowedRemovalRoots)
    $from = Join-Path $Source $Name
    $to = Join-Path $Destination $Name
    if (-not (Test-Path -LiteralPath $from -PathType Container)) { throw "Required program directory is missing: $from" }
    if (-not (Test-Path -LiteralPath $to -PathType Container)) {
        $null = New-Item -ItemType Directory -Path $to -Force
    } else {
        foreach ($item in Get-ChildItem -LiteralPath $to -Force) {
            Remove-TreeWithinAllowedRoots -Path $item.FullName -AllowedRoots $AllowedRemovalRoots -Purpose 'installer program-directory cleanup'
        }
    }
    foreach ($item in Get-ChildItem -LiteralPath $from -Force) {
        Copy-Item -LiteralPath $item.FullName -Destination $to -Recurse -Force
    }
}

function Copy-ApplicationTree {
    param([string]$Source, [string]$Destination, [string[]]$AllowedRemovalRoots)
    if (-not (Test-Path -LiteralPath $Source -PathType Container)) { throw "Application-tree source is missing: $Source" }
    $null = New-Item -ItemType Directory -Path $Destination -Force
    foreach ($item in Get-ChildItem -LiteralPath $Destination -Force) {
        if (-not (Test-Path -LiteralPath (Join-Path $Source $item.Name))) {
            Remove-TreeWithinAllowedRoots -Path $item.FullName -AllowedRoots $AllowedRemovalRoots -Purpose 'installer application-tree restore cleanup'
        }
    }
    foreach ($item in Get-ChildItem -LiteralPath $Source -Force) {
        if ($item.PSIsContainer) {
            Copy-ProgramDirectory -Name $item.Name -Source $Source -Destination $Destination -AllowedRemovalRoots $AllowedRemovalRoots
        } else {
            Copy-FileWhenReleased -Source $item.FullName -Destination (Join-Path $Destination $item.Name)
        }
    }
}

# v0.70.1: a freshly written executable can be held open for a moment by
# another process (a scanner opening a new binary) right after Agent_b has
# exited, and a rollback that meets it would leave a half-restored install. A
# sharing violation is retried for at most five seconds; anything else, or a
# lock that outlasts that, fails as before.
function Copy-FileWhenReleased {
    param([string]$Source, [string]$Destination)
    $deadline = [DateTime]::UtcNow.AddSeconds(5)
    $attempts = 0
    while ($true) {
        $attempts++
        try {
            Copy-Item -LiteralPath $Source -Destination $Destination -Force -ErrorAction Stop
            if ($attempts -gt 1) { Write-Host "COPY RETRIED: $([IO.Path]::GetFileName($Destination)) was held open by another process; copied on attempt $attempts" }
            return
        } catch [System.IO.IOException] {
            if ([DateTime]::UtcNow -ge $deadline) { throw }
            Start-Sleep -Milliseconds 250
        }
    }
}

function Get-InstalledDisplayVersion {
    param([string]$ApplicationRoot)
    $installerPath = Join-Path $ApplicationRoot 'scripts\install-Agent_b.ps1'
    if (-not (Test-Path -LiteralPath $installerPath -PathType Leaf)) { return 'unknown version' }
    $source = Get-Content -Raw -LiteralPath $installerPath
    $match = [regex]::Match($source, "(?m)^\`$displayVersion\s*=\s*'([^']+)'")
    if (-not $match.Success) { return 'unknown version' }
    return 'v' + $match.Groups[1].Value
}

function Remove-InstallerRollbackRoot {
    param([string]$Path)
    if ([string]::IsNullOrWhiteSpace($Path) -or -not (Test-Path -LiteralPath $Path)) { return }
    $full = Get-FullPath $Path
    $temporary = Get-FullPath ([IO.Path]::GetTempPath())
    if (-not $full.StartsWith($temporary + '\', [StringComparison]::OrdinalIgnoreCase) -or
        -not (Split-Path -Leaf $full).StartsWith('Agent_b-install-rollback-', [StringComparison]::Ordinal)) {
        throw "Refusing to remove unexpected installer rollback root: $full"
    }
    Remove-TreeWithinAllowedRoots -Path $full -AllowedRoots @($temporary) -Purpose 'installer rollback-root cleanup'
}

function Set-PrivateDirectoryAcl {
    param([string]$Path, [Security.Principal.SecurityIdentifier]$Owner)
    $acl = [Security.AccessControl.DirectorySecurity]::new()
    $acl.SetAccessRuleProtection($true, $false)
    $inherit = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit
    $propagate = [Security.AccessControl.PropagationFlags]::None
    $allow = [Security.AccessControl.AccessControlType]::Allow
    foreach ($sid in @(
        $Owner,
        [Security.Principal.SecurityIdentifier]::new([Security.Principal.WellKnownSidType]::LocalSystemSid, $null),
        [Security.Principal.SecurityIdentifier]::new([Security.Principal.WellKnownSidType]::BuiltinAdministratorsSid, $null)
    )) {
        $null = $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($sid, [Security.AccessControl.FileSystemRights]::FullControl, $inherit, $propagate, $allow))
    }
    $acl.SetOwner($Owner)
    Set-Acl -LiteralPath $Path -AclObject $acl
}

function Set-ApplicationDirectoryAcl {
    param([string]$Path, [Security.Principal.SecurityIdentifier]$Owner)
    $acl = [Security.AccessControl.DirectorySecurity]::new()
    $acl.SetAccessRuleProtection($true, $false)
    $inherit = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit
    $propagate = [Security.AccessControl.PropagationFlags]::None
    $allow = [Security.AccessControl.AccessControlType]::Allow
    foreach ($sid in @(
        [Security.Principal.SecurityIdentifier]::new([Security.Principal.WellKnownSidType]::LocalSystemSid, $null),
        [Security.Principal.SecurityIdentifier]::new([Security.Principal.WellKnownSidType]::BuiltinAdministratorsSid, $null)
    )) {
        $null = $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($sid, [Security.AccessControl.FileSystemRights]::FullControl, $inherit, $propagate, $allow))
    }
    $users = [Security.Principal.SecurityIdentifier]::new([Security.Principal.WellKnownSidType]::BuiltinUsersSid, $null)
    $null = $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($users, [Security.AccessControl.FileSystemRights]::ReadAndExecute, $inherit, $propagate, $allow))
    if ($TestMode) {
        $null = $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($Owner, [Security.AccessControl.FileSystemRights]::FullControl, $inherit, $propagate, $allow))
    }
    $applicationOwner = if ($TestMode) { $Owner } else { [Security.Principal.SecurityIdentifier]::new([Security.Principal.WellKnownSidType]::BuiltinAdministratorsSid, $null) }
    $acl.SetOwner($applicationOwner)
    Set-Acl -LiteralPath $Path -AclObject $acl
}

$script:installTranscriptStarted = $false
$script:installTranscriptPath = $null
$script:stoppedInstalledVersion = $false
$script:rollbackRoot = $null
$script:rollbackVersion = $null
$script:rollbackApplicationRoot = $null
trap {
    $reason = $_.Exception.Message
    if (-not $script:installTranscriptStarted -and $script:installTranscriptPath) {
        try { Start-InstallTranscript -Path $script:installTranscriptPath } catch { }
    }
    if ($script:stoppedInstalledVersion -and $script:rollbackRoot -and (Test-Path -LiteralPath $script:rollbackRoot -PathType Container)) {
        try {
            Copy-ApplicationTree -Source $script:rollbackRoot -Destination $script:rollbackApplicationRoot -AllowedRemovalRoots @($script:rollbackApplicationRoot)
            Write-Host "ROLLBACK: restored $($script:rollbackVersion) application files after installation failure."
            Write-Host "RESTART VERSION: $($script:rollbackVersion)"
            Write-Host "RESTART REASON: $(if ($reason -match 'verif') { 'verification failure' } else { 'installation failure' })"
        } catch {
            Write-Host "ROLLBACK FAILED: $($_.Exception.Message)"
        }
    }
    try { Remove-InstallerRollbackRoot -Path $script:rollbackRoot } catch { Write-Host "ROLLBACK CLEANUP FAILED: $($_.Exception.Message)" }
    Write-Host "INSTALLATION FAILED: $reason"
    Write-InstallProgress -Phase 'failed' -Text "INSTALLATION FAILED: $reason" -Done
    if ($script:installTranscriptPath) { Write-Host "Transcript: $script:installTranscriptPath" }
    Stop-InstallTranscript
    exit 1
}

if ($env:OS -ne 'Windows_NT') { throw 'Agent_b installation is supported only on Windows.' }
if ([string]::IsNullOrWhiteSpace($SourceDirectory)) { $SourceDirectory = Split-Path -Parent $PSScriptRoot }
if ([string]::IsNullOrWhiteSpace($OperatorSid)) { $OperatorSid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value }
if ([string]::IsNullOrWhiteSpace($OperatorLocalAppData)) { $OperatorLocalAppData = [Environment]::GetFolderPath('LocalApplicationData') }
if ([string]::IsNullOrWhiteSpace($DataDirectory)) { $DataDirectory = Join-Path $OperatorLocalAppData 'Agent_b' }
if ($WhatIfPreference) {
    $TranscriptPath = Join-Path ([IO.Path]::GetTempPath()) ("Agent_b-whatif-installer-{0}.log" -f [DateTime]::Now.ToString('yyyyMMdd-HHmmss-fff'))
} elseif ([string]::IsNullOrWhiteSpace($TranscriptPath)) {
    $TranscriptPath = Join-Path (Join-Path $DataDirectory 'logs') ("installer-{0}.log" -f [DateTime]::Now.ToString('yyyyMMdd-HHmmss-fff'))
}
$script:installTranscriptPath = Get-FullPath $TranscriptPath
Start-InstallTranscript -Path $script:installTranscriptPath

$sourceRoot = Get-FullPath $SourceDirectory
$applicationRoot = Assert-SafeAgentBPath $ApplicationDirectory 'ApplicationDirectory'
$dataRoot = Assert-SafeAgentBPath $DataDirectory 'DataDirectory'
$workspaceRoot = Assert-SafeAgentBPath $WorkspaceDirectory 'WorkspaceDirectory'
Assert-SafeRegistryPath $UninstallRegistryPath
Assert-TestPath $applicationRoot
Assert-TestPath $dataRoot
Assert-TestPath $workspaceRoot
if ($ForcePostStopVerificationFailure -and -not $TestMode) { throw 'ForcePostStopVerificationFailure is available only with TestMode.' }
if ($RegistrationSearchRoots.Count -and -not $TestMode) { throw 'RegistrationSearchRoots is available only with TestMode.' }
Assert-DisjointRoots @($applicationRoot, $dataRoot, $workspaceRoot)
if ($sourceRoot.Equals($applicationRoot, [StringComparison]::OrdinalIgnoreCase)) { throw 'SourceDirectory and ApplicationDirectory must be different.' }
if (-not $TestMode) {
	$expectedApplicationRoot = Get-FullPath (Join-Path $env:ProgramFiles 'Agent_b')
	$expectedDataRoot = Get-FullPath (Join-Path $OperatorLocalAppData 'Agent_b')
	$expectedWorkspaceRoot = Get-FullPath (Join-Path $env:ProgramData 'Agent_b\workspace')
    if (-not $applicationRoot.Equals($expectedApplicationRoot, [StringComparison]::OrdinalIgnoreCase)) {
        throw "ApplicationDirectory must be the admin-protected Program Files location: $expectedApplicationRoot"
    }
    if (-not $dataRoot.Equals($expectedDataRoot, [StringComparison]::OrdinalIgnoreCase)) {
        throw "DataDirectory must be the launching operator's LocalAppData Agent_b directory: $expectedDataRoot"
    }
    if (-not $workspaceRoot.Equals($expectedWorkspaceRoot, [StringComparison]::OrdinalIgnoreCase)) {
        throw "WorkspaceDirectory must be the machine-scoped ProgramData location: $expectedWorkspaceRoot"
    }
}

if (-not $TestMode) {
    $RegistrationSearchRoots = @(
        'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall',
        'HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall',
        'HKLM:\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall'
    )
}
$relatedRegistrations = @(Get-AgentBInstallRegistrations -Roots $RegistrationSearchRoots -CanonicalRegistryPath $UninstallRegistryPath)
$staleRegistrations = @(Get-AgentBRegistrationPreflight -Registrations $relatedRegistrations)
$alternateBinaryRoots = if ($TestMode) { @() } else {
    @(
        (Join-Path $OperatorLocalAppData 'Programs\Agent_b'),
        $(if (${env:ProgramFiles(x86)}) { Join-Path ${env:ProgramFiles(x86)} 'Agent_b' })
    ) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
}
foreach ($alternateRoot in $alternateBinaryRoots) {
    $alternateBinary = Join-Path $alternateRoot 'Agent_b.exe'
    if ((-not $alternateRoot.Equals($applicationRoot, [StringComparison]::OrdinalIgnoreCase)) -and
        (Test-Path -LiteralPath $alternateBinary -PathType Leaf)) {
        throw "Installation refused: another Agent_b executable exists at $alternateBinary. Remove that installation before installing this copy."
    }
}

try { $null = [Security.Principal.SecurityIdentifier]::new($OperatorSid) } catch {
    throw "OperatorSid is not a valid Windows SID: $OperatorSid"
}
$preflightConfigPath = Join-Path $dataRoot 'harness.json'
$preflightConfig = $null
if (Test-Path -LiteralPath $preflightConfigPath -PathType Leaf) {
    try { $preflightConfig = Get-Content -Raw -LiteralPath $preflightConfigPath | ConvertFrom-Json } catch {
        throw "Existing operator configuration is not valid JSON: $preflightConfigPath ($($_.Exception.Message))"
    }
}

$sourceBinary = Join-Path $sourceRoot 'Agent_b.exe'
$installedBinary = Join-Path $applicationRoot 'Agent_b.exe'
foreach ($directory in @('web', 'prompts', 'scripts', 'docs')) {
    $required = Join-Path $sourceRoot $directory
    if (-not (Test-Path -LiteralPath $required -PathType Container)) { throw "Required program directory is missing: $required" }
}
foreach ($file in @('harness.example.json', 'SECURITY.md', 'LICENSE', 'NOTICE', 'scripts\launch-installed.cmd')) {
    $required = Join-Path $sourceRoot $file
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) { throw "Required program file is missing: $required" }
}
Assert-CandidateIdentity -SourceRoot $sourceRoot -Binary $sourceBinary -Version $displayVersion
$null = Assert-WebView2Loader -SourceRoot $sourceRoot
$preflightProcesses = @(Get-InstalledProcesses $installedBinary)
if ($preflightProcesses.Count) {
    Write-Host "PREFLIGHT: running Agent_b PID(s) $(@($preflightProcesses.Id) -join ', ') will be stopped after elevation."
}
Write-Host 'PREFLIGHT COMPLETE'

if ((-not (Test-IsAdministrator) -or $PSVersionTable.PSEdition -ne 'Desktop') -and -not $WhatIfPreference -and -not $TestMode) {
    $arguments = @(
        '-NoLogo', '-NoProfile', '-File', $PSCommandPath,
        '-SourceDirectory', $sourceRoot,
        '-ApplicationDirectory', $applicationRoot,
        '-DataDirectory', $dataRoot,
        '-WorkspaceDirectory', $workspaceRoot,
        '-StartMenuDirectory', $StartMenuDirectory,
		'-UninstallRegistryPath', $UninstallRegistryPath,
		'-OperatorSid', $OperatorSid,
		'-OperatorLocalAppData', $OperatorLocalAppData,
        '-TranscriptPath', $script:installTranscriptPath
	)
    if ($SigningThumbprint) { $arguments += @('-SigningThumbprint', $SigningThumbprint) }
    $windowsPowerShell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
    Stop-InstallTranscript
    if (Test-IsAdministrator) {
        & $windowsPowerShell @arguments
        exit $LASTEXITCODE
    }
    $process = Start-Process -FilePath $windowsPowerShell -ArgumentList (($arguments | ForEach-Object { Quote-ProcessArgument $_ }) -join ' ') -Verb RunAs -Wait -PassThru
    exit $process.ExitCode
}

$currentSid = [Security.Principal.WindowsIdentity]::GetCurrent().User
if (-not $currentSid.Value.Equals($OperatorSid, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Installation refused: elevation changed identity from $OperatorSid to $($currentSid.Value). Use same-user UAC; over-the-shoulder administrator credentials would select the wrong LocalAppData and DPAPI owner."
}

Write-InstallProgress -Phase 'preflight' -Text "Installing Agent_b $displayVersion"
Write-Host 'Agent_b admin-protected program installation with per-operator registration and data'
Write-InstallProgress -Phase 'copying the application' -Text "Application: $applicationRoot"
Write-Host "Application: $applicationRoot"
Write-Host "Operator data: $dataRoot"
Write-Host "Legacy workspace (preserved when present): $workspaceRoot"
Write-Host "Operator SID: $OperatorSid"

if ($WhatIfPreference) {
    Write-Host 'Mode: WhatIf; no build, file, ACL, shortcut, or registry change will be made.'
    $null = $PSCmdlet.ShouldProcess($sourceBinary, 'Install the verified candidate Agent_b.exe')
    $null = $PSCmdlet.ShouldProcess($applicationRoot, 'Install or upgrade admin-only program files')
    $null = $PSCmdlet.ShouldProcess($dataRoot, 'Create or preserve private operator data')
	if (Test-Path -LiteralPath $workspaceRoot -PathType Container) { $null = $PSCmdlet.ShouldProcess($workspaceRoot, 'Preserve legacy service workspace') }
    Stop-InstallTranscript
    exit 0
}

$installedProcesses = @(Get-InstalledProcesses $installedBinary)
$preflightAclEnabled = $preflightConfig -and $preflightConfig.shell.service_account -and [bool]$preflightConfig.shell.service_account.enabled
if ($preflightAclEnabled) {
    $preflightAclAccount = if ($preflightConfig.shell.service_account.account) { [string]$preflightConfig.shell.service_account.account } else { 'agentb-svc' }
    $preflightExchangeRoot = Get-FullPath $(if ($preflightConfig.deliver -and $preflightConfig.deliver.exchange_folder) { [string]$preflightConfig.deliver.exchange_folder } else { '%USERPROFILE%\Agent_b' })
    $sourceAclScript = Join-Path $sourceRoot 'scripts\apply-acls.ps1'
    Write-Host 'PRESTOP POLICY: applying and verifying host policy before stopping Agent_b.'
    & $sourceAclScript -AccountName $preflightAclAccount -ApplicationDirectory $applicationRoot -DataDirectory $dataRoot -WorkspaceDirectory $workspaceRoot -ExchangeDirectory $preflightExchangeRoot -NoPrompt -Confirm:$false
    Assert-ScriptExitCode -Purpose 'Pre-stop ACL policy apply' -Code $LASTEXITCODE
    & $sourceAclScript -AccountName $preflightAclAccount -ApplicationDirectory $applicationRoot -DataDirectory $dataRoot -WorkspaceDirectory $workspaceRoot -ExchangeDirectory $preflightExchangeRoot -Verify
    Assert-ScriptExitCode -Purpose 'Pre-stop ACL policy verification' -Code $LASTEXITCODE
    Write-Host 'PRESTOP POLICY PASS: Agent_b is still running.'
}
if ($installedProcesses.Count) {
    $script:rollbackRoot = Join-Path ([IO.Path]::GetTempPath()) ('Agent_b-install-rollback-' + [Guid]::NewGuid().ToString('N'))
    $script:rollbackVersion = Get-InstalledDisplayVersion -ApplicationRoot $applicationRoot
    $script:rollbackApplicationRoot = $applicationRoot
    Copy-Item -LiteralPath $applicationRoot -Destination $script:rollbackRoot -Recurse -Force
    Write-Host "ROLLBACK READY: preserved $($script:rollbackVersion) application files before stop."
}
Stop-InstalledProcesses -Processes $installedProcesses
if ($installedProcesses.Count) { $script:stoppedInstalledVersion = $true }

$applicationCreated = -not (Test-Path -LiteralPath $applicationRoot -PathType Container)
$dataCreated = -not (Test-Path -LiteralPath $dataRoot -PathType Container)
$null = New-Item -ItemType Directory -Path $applicationRoot -Force
if ($applicationCreated) { Set-ApplicationDirectoryAcl -Path $applicationRoot -Owner $currentSid }
foreach ($directory in @('web', 'prompts', 'scripts', 'docs')) {
    Copy-ProgramDirectory -Name $directory -Source $sourceRoot -Destination $applicationRoot -AllowedRemovalRoots @($applicationRoot)
}
Wait-FileUnlocked -Path $installedBinary
# Item 2hc (v1.3.0/W2): WebView2Loader.dll travels with the exe. This list is
# curated rather than a tree copy, so a file that is not named here simply does
# not reach the installation - which is how the loader first arrived verified
# and then went missing from the installed root.
foreach ($file in @('Agent_b.exe', 'WebView2Loader.dll', 'harness.example.json', 'SECURITY.md', 'LICENSE', 'NOTICE')) {
    $from = Join-Path $sourceRoot $file
    if (-not (Test-Path -LiteralPath $from -PathType Leaf)) { throw "Required program file is missing: $from" }
    Copy-Item -LiteralPath $from -Destination (Join-Path $applicationRoot $file) -Force
}
Copy-Item -LiteralPath (Join-Path $sourceRoot 'scripts\launch-installed.cmd') -Destination (Join-Path $applicationRoot 'Agent_b.cmd') -Force
# The copy that will run is checked, after the copy and before it is signed:
# the candidate could have changed between the pre-stop check and the copy.
# A failure here rolls back and restarts the previous version.
Assert-CandidateIdentity -SourceRoot $sourceRoot -Binary $installedBinary -Version $displayVersion -AfterStop
$null = Assert-WebView2Loader -SourceRoot $applicationRoot -AfterStop

$null = New-Item -ItemType Directory -Path $dataRoot -Force
if ($dataCreated) { Set-PrivateDirectoryAcl -Path $dataRoot -Owner $currentSid }
foreach ($directory in @('logs', 'memory')) { $null = New-Item -ItemType Directory -Path (Join-Path $dataRoot $directory) -Force }

$configPath = Join-Path $dataRoot 'harness.json'
$writeConfig = $false
if (Test-Path -LiteralPath $configPath -PathType Leaf) {
	Write-Host 'PRESERVED: existing operator configuration'
	$config = Get-Content -Raw -LiteralPath $configPath | ConvertFrom-Json
} else {
    $templatePath = Join-Path $applicationRoot 'harness.example.json'
    $config = Get-Content -Raw -LiteralPath $templatePath | ConvertFrom-Json
    $config.workspace = Join-Path $dataRoot 'scratch'
    $config.log_dir = Join-Path $dataRoot 'logs'
	$config.memory.dir = Join-Path $dataRoot 'memory'
	$writeConfig = $true
	Write-Host 'CREATED: operator configuration from the installed template'
}
if (-not $config.deliver) {
	$config | Add-Member -NotePropertyName deliver -NotePropertyValue ([pscustomobject]@{ mode = 'both'; exchange_folder = '%USERPROFILE%\Agent_b' })
	$writeConfig = $true
}
if ([string]::IsNullOrWhiteSpace([string]$config.deliver.mode)) { $config.deliver.mode = 'both'; $writeConfig = $true }
if ([string]::IsNullOrWhiteSpace([string]$config.deliver.exchange_folder)) { $config.deliver.exchange_folder = '%USERPROFILE%\Agent_b'; $writeConfig = $true }
$exchangeRoot = Get-FullPath ([string]$config.deliver.exchange_folder)
if (-not $TestMode -and -not $SigningThumbprint -and (-not $config.signing -or [string]::IsNullOrWhiteSpace([string]$config.signing.thumbprint))) {
	$SigningThumbprint = 'auto'
}
if ($SigningThumbprint -eq 'auto') {
	Import-Module PKI -ErrorAction Stop
	$certificate = Get-ChildItem -LiteralPath 'Cert:\LocalMachine\My' | Where-Object {
		$_.Subject -eq 'CN=Agent_b Operator Code Signing' -and $_.HasPrivateKey -and $_.NotAfter -gt [DateTime]::Now -and
		@($_.EnhancedKeyUsageList | Where-Object { ([string]$_.ObjectId) -eq '1.3.6.1.5.5.7.3.3' }).Count -gt 0
	} | Sort-Object NotAfter -Descending | Select-Object -First 1
	if ($certificate) {
		Write-Host "REUSED: administrator-gated signing certificate $($certificate.Thumbprint)"
	} else {
		$certificate = New-SelfSignedCertificate -Type CodeSigningCert -Subject 'CN=Agent_b Operator Code Signing' -CertStoreLocation 'Cert:\LocalMachine\My' -KeyAlgorithm RSA -KeyLength 3072 -HashAlgorithm SHA256 -KeyExportPolicy NonExportable -NotAfter ([DateTime]::Now.AddYears(3))
		Write-Host "CREATED: administrator-gated signing certificate $($certificate.Thumbprint)"
	}
	Add-CurrentUserCertificate -Certificate $certificate -StoreName TrustedPublisher
	Add-CurrentUserCertificate -Certificate $certificate -StoreName Root
	$SigningThumbprint = $certificate.Thumbprint
}
if ($SigningThumbprint) {
	if (-not $config.signing) { $config | Add-Member -NotePropertyName signing -NotePropertyValue ([pscustomobject]@{ thumbprint = ''; timestamp_url = 'http://timestamp.digicert.com' }) }
	$config.signing.thumbprint = ($SigningThumbprint -replace '[^0-9A-Fa-f]', '').ToUpperInvariant()
	$writeConfig = $true
}
if ($writeConfig) {
	[IO.File]::WriteAllText($configPath, ($config | ConvertTo-Json -Depth 100) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
}

# Re-sign every deployed signable artifact when the operator configured a
# Store-backed code-signing certificate. The PFX and private key never enter
# the installer; the certificate is resolved by thumbprint from the machine or user store.
if ($config.signing -and -not [string]::IsNullOrWhiteSpace([string]$config.signing.thumbprint)) {
	$thumbprint = ([string]$config.signing.thumbprint -replace '[^0-9A-Fa-f]', '').ToUpperInvariant()
	$signingStore = 'Cert:\LocalMachine\My'
	$signingIdentity = [Security.Principal.WindowsIdentity]::GetCurrent()
	$certificate = Get-ChildItem -LiteralPath ("Cert:\LocalMachine\My\{0}" -f $thumbprint) -ErrorAction SilentlyContinue
	if (-not $certificate) { $signingStore = 'Cert:\CurrentUser\My'; $certificate = Get-ChildItem -LiteralPath ("Cert:\CurrentUser\My\{0}" -f $thumbprint) -ErrorAction Stop }
	if (-not $certificate.HasPrivateKey -or @($certificate.EnhancedKeyUsageList | Where-Object { ([string]$_.ObjectId) -eq '1.3.6.1.5.5.7.3.3' }).Count -eq 0) {
		throw "Configured certificate $thumbprint is not a usable LocalMachine or CurrentUser code-signing certificate."
	}
	$signTargets = @($installedBinary) + @(Get-ChildItem -LiteralPath $applicationRoot -Filter '*.ps1' -File -Recurse | ForEach-Object FullName)
	foreach ($target in $signTargets) {
		$signature = Set-AuthenticodeSignature -LiteralPath $target -Certificate $certificate -HashAlgorithm SHA256 -TimestampServer ([string]$config.signing.timestamp_url)
		# A raw provider error says nothing actionable. Name the store and the
		# identity that could not open the key, which is what actually differs
		# between this context and the elevated one Settings signs from.
		if (-not $signature.SignerCertificate) {
			throw "Signing $target with $thumbprint from $signingStore as $($signingIdentity.Name) applied no signature: $($signature.Status) $($signature.StatusMessage). Settings signs this certificate because manage-signing.ps1 elevates first."
		}
		if ($signature.Status -ne 'Valid') { Write-Host "SIGNED, CHAIN NOT TRUSTED HERE: $target`: $($signature.Status) $($signature.StatusMessage)" }
	}
	Write-Host "SIGNED: Agent_b.exe and $($signTargets.Count - 1) PowerShell scripts with $thumbprint"
}

if ($config.shell.service_account -and [bool]$config.shell.service_account.enabled) {
	$aclAccount = if ($config.shell.service_account.account) { [string]$config.shell.service_account.account } else { 'agentb-svc' }
	$installedAclScript = Join-Path $applicationRoot 'scripts\apply-acls.ps1'
	& $installedAclScript -AccountName $aclAccount -ApplicationDirectory $applicationRoot -DataDirectory $dataRoot -WorkspaceDirectory $workspaceRoot -ExchangeDirectory $exchangeRoot -NoPrompt -Confirm:$false
	Write-Host 'VERIFY: installed root, plans/scratch exceptions, workspace, and exchange-folder ACL policy'
	& $installedAclScript -AccountName $aclAccount -ApplicationDirectory $applicationRoot -DataDirectory $dataRoot -WorkspaceDirectory $workspaceRoot -ExchangeDirectory $exchangeRoot -Verify
	if ($LASTEXITCODE -ne 0) { throw "Installed ACL policy verification failed with exit code $LASTEXITCODE." }
	Write-Host 'PASS: installed root, plans/scratch exceptions, workspace, and exchange-folder ACL policy'
}
if ($ForcePostStopVerificationFailure) { throw 'Forced post-stop verification failure.' }

$iconPath = Join-Path $applicationRoot 'web\assets\Agent_b.ico'
if (-not (Test-Path -LiteralPath $iconPath -PathType Leaf)) { throw "Installed icon is missing: $iconPath" }
$null = New-Item -ItemType Directory -Path $StartMenuDirectory -Force
$shortcutPath = Join-Path $StartMenuDirectory 'Agent_b.lnk'
$shell = New-Object -ComObject WScript.Shell
$shortcut = $shell.CreateShortcut($shortcutPath)
$shortcut.TargetPath = Join-Path $env:SystemRoot 'System32\wscript.exe'
$hiddenLauncher = Join-Path $applicationRoot 'scripts\launch-hidden.vbs'
$batchLauncher = Join-Path $applicationRoot 'Agent_b.cmd'
$shortcut.Arguments = '//B "' + $hiddenLauncher + '" "' + $batchLauncher + '"'
$shortcut.WorkingDirectory = $dataRoot
$shortcut.IconLocation = "$iconPath,0"
$shortcut.Description = 'Open Agent_b'
$shortcut.Save()

# Production returns at the operator's next sign-in without a Start menu click
# (item 2em). A Fast Startup shutdown logs the user off, which ends every process
# in the session. The launcher starts nothing when Agent_b is already running.
$startupDirectory = Join-Path $StartMenuDirectory 'Startup'
$null = New-Item -ItemType Directory -Path $startupDirectory -Force
$startupPath = Join-Path $startupDirectory 'Agent_b.lnk'
$startup = $shell.CreateShortcut($startupPath)
$startup.TargetPath = Join-Path $env:SystemRoot 'System32\wscript.exe'
# v0.65.0/W9: the arguments pass through WScript.Shell.Run and cmd's `call`, and
# both expand %VAR%. The default data root is the launcher's own default, so it
# is not passed at all; any other root is passed only when it holds no `%`.
$startupArguments = '//B "' + $hiddenLauncher + '" "' + $batchLauncher + '" -Detached -NoBrowser -NoPause'
$defaultDataRoot = [IO.Path]::GetFullPath((Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'Agent_b')).TrimEnd('\')
if (-not $dataRoot.Equals($defaultDataRoot, [StringComparison]::OrdinalIgnoreCase)) {
    if ($dataRoot.Contains('%')) { throw "The data directory contains '%', which the sign-in start would expand as a variable: $dataRoot" }
    $startupArguments += ' -DataDirectory "' + $dataRoot + '"'
}
$startup.Arguments = $startupArguments
$startup.WorkingDirectory = $dataRoot
$startup.IconLocation = "$iconPath,0"
$startup.Description = 'Start Agent_b in the background at sign-in'
$startup.Save()

$uninstallScript = Join-Path $applicationRoot 'scripts\uninstall-Agent_b.ps1'
$powershell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
$uninstallArguments = @(
    '-NoLogo', '-NoProfile', '-File', $uninstallScript,
    '-ApplicationDirectory', $applicationRoot,
    '-DataDirectory', $dataRoot,
    '-WorkspaceDirectory', $workspaceRoot,
    '-StartMenuDirectory', $StartMenuDirectory,
    '-UninstallRegistryPath', $UninstallRegistryPath,
    '-ExpectedOperatorSid', $OperatorSid,
    '-ExpectedOperatorLocalAppData', $OperatorLocalAppData
)
if ($TestMode) { $uninstallArguments += '-TestMode' }
$uninstallCommand = (Quote-ProcessArgument $powershell) + ' ' + (($uninstallArguments | ForEach-Object { Quote-ProcessArgument $_ }) -join ' ')
$null = New-Item -Path $UninstallRegistryPath -Force
$properties = [ordered]@{
	DisplayName = 'Agent_b'
    DisplayVersion = $displayVersion
    Publisher = 'rkclayton'
    DisplayIcon = $iconPath
    InstallLocation = $applicationRoot
    UninstallString = $uninstallCommand
    QuietUninstallString = "$uninstallCommand -Quiet"
    URLInfoAbout = 'https://github.com/rkclayton/agent_b'
    InstallDate = (Get-Date -Format 'yyyyMMdd')
    OperatorSid = $OperatorSid
    DataLocation = $dataRoot
    WorkspaceLocation = $workspaceRoot
}
foreach ($entry in $properties.GetEnumerator()) {
    $null = New-ItemProperty -Path $UninstallRegistryPath -Name $entry.Key -Value $entry.Value -PropertyType String -Force
}
$estimatedKB = [int][Math]::Ceiling(((Get-ChildItem -LiteralPath $applicationRoot -File -Recurse | Measure-Object Length -Sum).Sum) / 1KB)
$null = New-ItemProperty -Path $UninstallRegistryPath -Name EstimatedSize -Value $estimatedKB -PropertyType DWord -Force
$null = New-ItemProperty -Path $UninstallRegistryPath -Name NoModify -Value 1 -PropertyType DWord -Force
$null = New-ItemProperty -Path $UninstallRegistryPath -Name NoRepair -Value 1 -PropertyType DWord -Force
foreach ($staleRegistration in $staleRegistrations) {
    if ($PSCmdlet.ShouldProcess($staleRegistration.RegistryPath, "Remove stale $($staleRegistration.DisplayName) Installed apps registration")) {
        Remove-AgentBStaleRegistrations -Registrations @($staleRegistration)
        Write-Host "Removed stale Installed apps registration: $($staleRegistration.DisplayName) ($($staleRegistration.InstallLocation))"
    }
}

# Item 2gl (v1.3.0/W3): THE PWA CLAUSE IS OUT. v1.2.7 wrote an Edge
# WebAppInstallForceList entry here so Edge would install the app silently and
# the window controls overlay could give us the top edge. The operator installed
# that build elevated on 2026-09-21 and measured the overlay: it does not
# activate on a policy-installed app either. So the entry bought nothing, and a
# machine-wide policy that buys nothing is not worth writing.
#
# The REMOVAL stays, in uninstall-Agent_b.ps1, because a v1.2.7 install may have
# written one on this machine and uninstalling must take it away.
Write-Host ''
Write-InstallProgress -Phase 'finished' -Text "Agent_b $displayVersion is installed." -Done -OK
Write-Host 'INSTALLATION COMPLETE'
Write-Host "Start Menu: $shortcutPath"
Write-Host "At sign-in: $startupPath"
Write-Host 'Registration: HKCU and the operator Start Menu, matching the LocalAppData configuration and user-scoped DPAPI owner.'
Write-Host 'Settings: created once in LocalAppData and preserved on upgrades'
Write-Host "Transcript: $script:installTranscriptPath"
Stop-InstallTranscript
Remove-InstallerRollbackRoot -Path $script:rollbackRoot
$script:rollbackRoot = $null
$script:stoppedInstalledVersion = $false
exit 0
