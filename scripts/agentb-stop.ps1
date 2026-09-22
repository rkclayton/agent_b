# Request-AgentbGracefulStop asks one Agent_b process to stop the way it
# records as "asked to close". Since v0.65.0 (item 2eq) the process ignores
# WM_CLOSE, because any process on the desktop could send it; it listens on a
# named event in its session that only the operator's account and SYSTEM may
# set. Item 2hw scopes that name to the canonical application root as well as
# the PID. Only an installer that already selected the exact installed path may
# opt into the unscoped earlier-version fallback.
function Get-AgentbStopEventName {
    param(
        [Parameter(Mandatory = $true)][string]$ApplicationRoot,
        [Parameter(Mandatory = $true)][int]$ProcessId
    )
    $canonical = [IO.Path]::GetFullPath([Environment]::ExpandEnvironmentVariables($ApplicationRoot)).TrimEnd('\').ToUpperInvariant()
    $sha256 = [Security.Cryptography.SHA256]::Create()
    try { $digest = $sha256.ComputeHash([Text.Encoding]::UTF8.GetBytes($canonical)) } finally { $sha256.Dispose() }
    $rootId = -join @($digest[0..11] | ForEach-Object { $_.ToString('x2') })
    return "Local\Agent_b-stop-$rootId-$ProcessId"
}

function Request-AgentbGracefulStop {
    param(
        [Parameter(Mandatory = $true)][string]$ApplicationRoot,
        [Parameter(Mandatory = $true)][int]$ProcessId,
        [switch]$AllowLegacy
    )
    $stopEvent = $null
    $eventName = Get-AgentbStopEventName -ApplicationRoot $ApplicationRoot -ProcessId $ProcessId
    try {
        $stopEvent = [System.Threading.EventWaitHandle]::OpenExisting($eventName)
    } catch [System.Threading.WaitHandleCannotBeOpenedException] {
        $stopEvent = $null
    } catch [System.UnauthorizedAccessException] {
        # The event admits only the operator's account and SYSTEM (item 2eq).
        throw "Agent_b PID $ProcessId can be stopped gracefully only by the operator's own account; run the installer as the operator or stop Agent_b first."
    }
    if ($stopEvent) {
        try { $null = $stopEvent.Set() } finally { $stopEvent.Dispose() }
        return 'stop event'
    }
    if (-not $AllowLegacy) {
        throw "Agent_b PID $ProcessId has no stop channel for application root $([IO.Path]::GetFullPath($ApplicationRoot))."
    }
    # The exact-path installer selection is the authority for this compatibility
    # branch. A general caller never gets an unscoped channel or WM_CLOSE fallback.
    try {
        $stopEvent = [System.Threading.EventWaitHandle]::OpenExisting("Local\Agent_b-stop-$ProcessId")
    } catch [System.Threading.WaitHandleCannotBeOpenedException] {
        $stopEvent = $null
    }
    if ($stopEvent) {
        try { $null = $stopEvent.Set() } finally { $stopEvent.Dispose() }
        return 'legacy stop event'
    }
    & (Join-Path $env:SystemRoot 'System32\taskkill.exe') /PID $ProcessId | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "Agent_b PID $ProcessId could not be asked to stop (taskkill exit $LASTEXITCODE)." }
    return 'WM_CLOSE (earlier version)'
}
