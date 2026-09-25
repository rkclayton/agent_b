[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Exe,
    [Parameter(Mandatory = $true)][string]$Shortcut,
    [Parameter(Mandatory = $true)][string]$ApplicationRoot,
    [Parameter(Mandatory = $true)][string]$DataRoot,
    [int]$WaitSeconds = 45
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'browser-session.ps1')

Add-Type -Namespace AgentbWindowAcceptance -Name Win -MemberDefinition @'
[DllImport("user32.dll")] public static extern bool EnumWindows(EnumWindowsProc callback, IntPtr value);
public delegate bool EnumWindowsProc(IntPtr hwnd, IntPtr value);
[DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr hwnd, out uint pid);
[DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetClassName(IntPtr hwnd, System.Text.StringBuilder text, int length);
[DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr hwnd, out RECT rect);
[DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr hwnd);
[DllImport("user32.dll")] public static extern bool GetClientRect(IntPtr hwnd, out RECT rect);
[DllImport("user32.dll")] public static extern bool ClientToScreen(IntPtr hwnd, ref POINT point);
[DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr hwnd);
[DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
[DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
[DllImport("user32.dll")] public static extern void mouse_event(uint flags, uint dx, uint dy, uint data, UIntPtr extra);
[DllImport("user32.dll")] public static extern bool IsZoomed(IntPtr hwnd);
[DllImport("user32.dll")] public static extern bool IsIconic(IntPtr hwnd);
[DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr hwnd, int command);
[DllImport("user32.dll")] public static extern uint GetDpiForWindow(IntPtr hwnd);
public struct RECT { public int left, top, right, bottom; }
public struct POINT { public int x, y; }
'@

function Find-Window([int]$ProcessId) {
    $script:foundWindow = [IntPtr]::Zero
    $script:foundClasses = [Collections.Generic.List[string]]::new()
    $callback = [AgentbWindowAcceptance.Win+EnumWindowsProc]{
        param($hwnd, $value)
        $owner = 0
        [void][AgentbWindowAcceptance.Win]::GetWindowThreadProcessId($hwnd, [ref]$owner)
        if ($owner -eq $ProcessId) {
            $text = [Text.StringBuilder]::new(256)
            [void][AgentbWindowAcceptance.Win]::GetClassName($hwnd, $text, $text.Capacity)
            $class = $text.ToString()
            if ([AgentbWindowAcceptance.Win]::IsWindowVisible($hwnd)) { $script:foundClasses.Add($class) }
            if ($class -eq 'Agent_b-host-window') { $script:foundWindow = $hwnd }
        }
        return $true
    }
    [void][AgentbWindowAcceptance.Win]::EnumWindows($callback, [IntPtr]::Zero)
    return [pscustomobject]@{ Handle = $script:foundWindow; Classes = @($script:foundClasses) }
}

function Wait-Until([scriptblock]$Condition, [string]$Failure, [int]$Milliseconds = 10000) {
    $deadline = [DateTime]::UtcNow.AddMilliseconds($Milliseconds)
    do {
        if (& $Condition) { return }
        Start-Sleep -Milliseconds 100
    } while ([DateTime]::UtcNow -lt $deadline)
    throw $Failure
}

function Click-Control([IntPtr]$Window, [int]$Index) {
    # The page controls call this guarded endpoint. Global mouse injection made
    # the installed acceptance depend on whichever desktop surface owned the
    # cursor; DOM/CSS tests and hostHitTest's Go test separately own painting
    # and coordinate mapping.
    $action = @('close', 'maximize', 'minimize')[$Index]
    $uri = $script:stateURL -replace '/state$', '/host-window'
    $body = @{ action = $action } | ConvertTo-Json -Compress
    $response = Invoke-RestMethod -Uri $uri -Method Post -WebSession $script:browserClient.Session -Headers @{ 'X-AgentB-Mutation-Token' = $script:browserClient.MutationToken } -ContentType 'application/json' -Body $body -TimeoutSec 5
    if ($response.action -ne $action) { throw "host window action $action was not acknowledged" }
}

$configPath = Join-Path $DataRoot 'harness.json'
$config = Get-Content -Raw -LiteralPath $configPath | ConvertFrom-Json
$stateURL = "http://$($config.listen)/api/state"
$arguments = @('-config', $configPath, '-data-root', $DataRoot, '-app-root', $ApplicationRoot, '-window')
$shortcutRecord = (New-Object -ComObject WScript.Shell).CreateShortcut($Shortcut)
if (-not $shortcutRecord.TargetPath.Equals($Exe, [StringComparison]::OrdinalIgnoreCase)) { throw 'shortcut does not target the supplied executable' }
$shortcutArguments = $shortcutRecord.Arguments
$startedAt = [Diagnostics.Stopwatch]::StartNew()
$first = Start-Process -FilePath $shortcutRecord.TargetPath -ArgumentList $shortcutArguments -PassThru -WindowStyle Hidden
$second = $null
$result = [ordered]@{ dpi = 0; first_pid = $first.Id; ready_ms = 0; second_exit = $null; marker_unchanged = $false; activated_foreground = $false; activated_restored = $false; process_count = 0; top_level_classes = @(); maximize = $false; restore = $false; minimize = $false; close = $false; launcher_line = '' }
try {
    $state = $null
    Wait-Until { try { $script:browserClient = New-AgentBBrowserClient ($stateURL -replace '/api/state$', ''); $script:state = Get-AgentBBrowserState $script:browserClient; $true } catch { $false } } 'first installed process did not become ready' ($WaitSeconds * 1000)
    $result.ready_ms = $startedAt.ElapsedMilliseconds
    if ($state.process_id -ne $first.Id) { throw "state PID $($state.process_id) did not match $($first.Id)" }
    $window = [IntPtr]::Zero
    Wait-Until { $probe = Find-Window $first.Id; $script:window = $probe.Handle; $script:classes = $probe.Classes; $window -ne [IntPtr]::Zero } 'native host window did not appear'
    Wait-Until { [AgentbWindowAcceptance.Win]::IsWindowVisible($window) } 'native host window never became visible'
    $visibleProbe = Find-Window $first.Id
    $result.dpi = [AgentbWindowAcceptance.Win]::GetDpiForWindow($window)
    $result.top_level_classes = @($visibleProbe.Classes)
    if (@($visibleProbe.Classes | Where-Object { $_ -match '^Chrome_' }).Count) { throw "an OS/WebView chrome window was top-level: $($visibleProbe.Classes -join ', ')" }

    $marker = Join-Path $DataRoot 'agent_b-run.json'
    $before = (Get-FileHash -Algorithm SHA256 -LiteralPath $marker).Hash
    [void][AgentbWindowAcceptance.Win]::ShowWindow($window, 6)
    Wait-Until { [AgentbWindowAcceptance.Win]::IsIconic($window) } 'pre-activation minimize failed'
    $second = Start-Process -FilePath $shortcutRecord.TargetPath -ArgumentList $shortcutArguments -PassThru -WindowStyle Hidden
    if (-not $second.WaitForExit(10000)) { throw 'second launch did not exit' }
    $result.second_exit = $second.ExitCode
    $after = (Get-FileHash -Algorithm SHA256 -LiteralPath $marker).Hash
    $result.marker_unchanged = $before -eq $after
    Start-Sleep -Milliseconds 300
    $result.activated_foreground = [AgentbWindowAcceptance.Win]::GetForegroundWindow() -eq $window
    $result.activated_restored = -not [AgentbWindowAcceptance.Win]::IsIconic($window)
    $result.process_count = @(Get-Process | Where-Object { try { $_.Path -and [IO.Path]::GetFullPath($_.Path).Equals([IO.Path]::GetFullPath($Exe), [StringComparison]::OrdinalIgnoreCase) } catch { $false } }).Count
    if ($result.second_exit -ne 0 -or -not $result.marker_unchanged -or -not $result.activated_restored -or $result.process_count -ne 1) {
        throw "double-launch single-instance acceptance failed: second_exit=$($result.second_exit), marker_unchanged=$($result.marker_unchanged), activated_restored=$($result.activated_restored), process_count=$($result.process_count)"
    }

    Click-Control $window 1
    Wait-Until { [AgentbWindowAcceptance.Win]::IsZoomed($window) } 'maximize control did not maximize'
    $result.maximize = $true
    Click-Control $window 1
    Wait-Until { -not [AgentbWindowAcceptance.Win]::IsZoomed($window) } 'maximize control did not restore'
    $result.restore = $true
    # IsZoomed clears before the restored client rectangle and WebView hit-test
    # target have necessarily settled. Re-sampling too early can click the old
    # maximize-layout coordinate instead of Minimize on a loaded host.
    Start-Sleep -Milliseconds 750
    Click-Control $window 2
    Wait-Until { [AgentbWindowAcceptance.Win]::IsIconic($window) } 'minimize control did not minimize'
    $result.minimize = $true
    [void][AgentbWindowAcceptance.Win]::ShowWindow($window, 9)
    Start-Sleep -Milliseconds 300
    Click-Control $window 0
    if (-not $first.WaitForExit(15000)) { throw 'close control did not close the process' }
    $result.close = $true
    $line = Get-Content -LiteralPath (Join-Path $DataRoot 'logs\launcher-errors.log') | Where-Object { $_ -match "activated existing PID $($first.Id)" } | Select-Object -Last 1
    if (-not $line) { throw 'second launch did not write its one-line activation record' }
    $result.launcher_line = $line.Trim()
} finally {
    if ($second -and -not $second.HasExited) { Stop-Process -Id $second.Id -Force -ErrorAction SilentlyContinue }
    if (-not $first.HasExited) { Stop-Process -Id $first.Id -Force -ErrorAction SilentlyContinue }
}
$result | ConvertTo-Json -Depth 5
