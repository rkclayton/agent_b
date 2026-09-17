# Request-AgentbGracefulStop asks one Agent_b process to stop the way it
# records as "asked to close". Since v0.65.0 (item 2eq) the process ignores
# WM_CLOSE, because any process on the desktop could send it; it listens on a
# named event in its session that only the operator's account and SYSTEM may
# set. A process without the event is an earlier version, which still stops on
# WM_CLOSE, so taskkill without /F remains the fallback for upgrades.
function Request-AgentbGracefulStop {
    param([Parameter(Mandatory = $true)][int]$ProcessId)
    $stopEvent = $null
    try {
        $stopEvent = [System.Threading.EventWaitHandle]::OpenExisting("Local\Agent_b-stop-$ProcessId")
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
    & (Join-Path $env:SystemRoot 'System32\taskkill.exe') /PID $ProcessId | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "Agent_b PID $ProcessId could not be asked to stop (taskkill exit $LASTEXITCODE)." }
    return 'WM_CLOSE (earlier version)'
}
