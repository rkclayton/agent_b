function Get-AgentBGuardSha256 {
    param([Parameter(Mandatory)][string]$Path)
    $stream = [IO.FileStream]::new($Path, [IO.FileMode]::Open, [IO.FileAccess]::Read, ([IO.FileShare]::ReadWrite -bor [IO.FileShare]::Delete))
    $hasher = [Security.Cryptography.SHA256]::Create()
    try { return ([BitConverter]::ToString($hasher.ComputeHash($stream)) -replace '-', '').ToLowerInvariant() } finally { $hasher.Dispose(); $stream.Dispose() }
}

function Get-AgentBGuardFileIdentity {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        return [ordered]@{ path = $Path; exists = $false; sha256 = $null; mtime_utc = $null }
    }
    $item = Get-Item -LiteralPath $Path
    return [ordered]@{
        path = $item.FullName
        exists = $true
        sha256 = Get-AgentBGuardSha256 -Path $item.FullName
        mtime_utc = $item.LastWriteTimeUtc.ToString('o')
    }
}

function Get-AgentBSessionRegistryIdentity {
    param([string]$ChatsDirectory)
    if (-not (Test-Path -LiteralPath $ChatsDirectory -PathType Container)) {
        return [ordered]@{ path = $ChatsDirectory; exists = $false; sha256 = $null; files = 0 }
    }
    # The registry is the set of retained session journals, not their live
    # append-only contents. Hashing bytes would make an ordinary active turn
    # look like a production restart.
    $lines = @(Get-ChildItem -LiteralPath $ChatsDirectory -File -Filter '*.jsonl' | Sort-Object Name | ForEach-Object Name)
    $bytes = [Text.Encoding]::UTF8.GetBytes(($lines -join "`n"))
    $hasher = [Security.Cryptography.SHA256]::Create()
    try { $hash = ([BitConverter]::ToString($hasher.ComputeHash($bytes)) -replace '-', '').ToLowerInvariant() } finally { $hasher.Dispose() }
    return [ordered]@{ path = $ChatsDirectory; exists = $true; sha256 = $hash; files = @($lines).Count }
}

function Get-AgentBProductionIncarnation {
    [CmdletBinding()]
    param(
        [string]$ApplicationDirectory,
        [string]$DataDirectory = (Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'Agent_b'),
        [string]$StateUri = 'http://127.0.0.1:8790/api/state'
    )
    if ([string]::IsNullOrWhiteSpace($ApplicationDirectory)) {
        $perUser = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'Programs\Agent_b'
        $machine = Join-Path $env:ProgramFiles 'Agent_b'
        $ApplicationDirectory = if (Test-Path -LiteralPath (Join-Path $perUser 'Agent_b.exe') -PathType Leaf) { $perUser } else { $machine }
    }
    $executable = Join-Path $ApplicationDirectory 'Agent_b.exe'
    $processes = @()
    if (Test-Path -LiteralPath $executable -PathType Leaf) {
        $processes = @(Get-Process -Name Agent_b -ErrorAction SilentlyContinue | Where-Object {
            try { $_.Path -and [IO.Path]::GetFullPath($_.Path).Equals([IO.Path]::GetFullPath($executable), [StringComparison]::OrdinalIgnoreCase) } catch { $false }
        } | Sort-Object Id | ForEach-Object {
            [ordered]@{ pid = $_.Id; start_time_utc = $_.StartTime.ToUniversalTime().ToString('o'); executable_path = $_.Path }
        })
    }
    $commit = $null
    $serverStartedAt = $null
    $activeProfile = $null
    try {
        $state = Invoke-RestMethod -Uri $StateUri -TimeoutSec 2
        foreach ($candidate in @($state.build.commit, $state.commit, $state.build_commit)) {
            if (-not [string]::IsNullOrWhiteSpace([string]$candidate)) { $commit = [string]$candidate; break }
        }
        $serverStartedAt = [string]$state.server_started_at
        $activeProfile = [string]$state.profiles.active
    } catch { }
    $legacyChats = Join-Path $DataDirectory 'chats'
    $profileChats = if ([string]::IsNullOrWhiteSpace($activeProfile)) { $legacyChats } else { Join-Path (Join-Path (Join-Path $DataDirectory 'profiles') $activeProfile) 'chats' }
    $chatsDirectory = if (Test-Path -LiteralPath $profileChats -PathType Container) { $profileChats } else { $legacyChats }
    return [ordered]@{
        application_directory = [IO.Path]::GetFullPath($ApplicationDirectory).TrimEnd('\')
        processes = $processes
        api_commit = $commit
        server_started_at = $serverStartedAt
        executable = Get-AgentBGuardFileIdentity -Path $executable
        config = Get-AgentBGuardFileIdentity -Path (Join-Path $DataDirectory 'harness.json')
        session_registry = Get-AgentBSessionRegistryIdentity -ChatsDirectory $chatsDirectory
    }
}

function Assert-AgentBProductionIncarnationUnchanged {
    [CmdletBinding()]
    param($Before, $After, [string]$Suite = 'suite')
    $deltas = [Collections.Generic.List[string]]::new()
    foreach ($name in @('application_directory', 'processes', 'api_commit', 'server_started_at', 'executable', 'config', 'session_registry')) {
        $left = $Before[$name] | ConvertTo-Json -Compress -Depth 8
        $right = $After[$name] | ConvertTo-Json -Compress -Depth 8
        if ($left -cne $right) { $deltas.Add($name) }
    }
    if ($deltas.Count) { throw "PRODUCTION INCARNATION CHANGED during $Suite`: $($deltas -join ', ')" }
}

function Invoke-AgentBSuiteGuarded {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$Suite, [Parameter(Mandatory)][scriptblock]$Action)
    $before = Get-AgentBProductionIncarnation
    try { & $Action } finally {
        $after = Get-AgentBProductionIncarnation
        Assert-AgentBProductionIncarnationUnchanged -Before $before -After $after -Suite $Suite
    }
}
