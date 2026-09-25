# v1.3.0/W2 (2hc): the host-window smoke, measured rather than asserted.
#
# Starts Agent_b with -window on a disposable root and port, then checks from
# OUTSIDE the process: the host window exists, a WebView2 child window exists
# under it (the page is actually hosted, not an empty frame), and the frame's
# TOP INSET is zero - which is the whole claim, that the page's 32 px strip is
# the window's top edge rather than sitting under a title bar.
param(
    [Parameter(Mandatory = $true)][string]$Exe,
    [Parameter(Mandatory = $true)][string]$DataRoot,
    [Parameter(Mandatory = $true)][string]$AppRoot,
    [int]$WaitSeconds = 40
)
$ErrorActionPreference = 'Stop'

Add-Type -Namespace HostSmoke -Name Win -MemberDefinition @'
[DllImport("user32.dll")] public static extern bool EnumWindows(EnumWindowsProc lpEnumFunc, IntPtr lParam);
public delegate bool EnumWindowsProc(IntPtr hWnd, IntPtr lParam);
[DllImport("user32.dll")] public static extern bool EnumChildWindows(IntPtr hWnd, EnumWindowsProc lpEnumFunc, IntPtr lParam);
[DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint pid);
[DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetClassName(IntPtr h, System.Text.StringBuilder s, int n);
[DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
[DllImport("user32.dll")] public static extern bool GetClientRect(IntPtr h, out RECT r);
[DllImport("user32.dll")] public static extern bool ClientToScreen(IntPtr h, ref POINT p);
[DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
public struct RECT { public int left, top, right, bottom; }
public struct POINT { public int x, y; }
'@

function Get-WindowsOf {
    param([int]$ProcessId, [string]$ClassLike)
    $found = New-Object System.Collections.ArrayList
    $cb = [HostSmoke.Win+EnumWindowsProc]{
        param($h, $l)
        $owner = 0
        [void][HostSmoke.Win]::GetWindowThreadProcessId($h, [ref]$owner)
        if ($owner -eq $ProcessId) {
            $sb = New-Object System.Text.StringBuilder 256
            [void][HostSmoke.Win]::GetClassName($h, $sb, $sb.Capacity)
            if (-not $ClassLike -or $sb.ToString() -like $ClassLike) {
                [void]$found.Add([pscustomobject]@{ handle = $h; class = $sb.ToString() })
            }
        }
        return $true
    }
    [void][HostSmoke.Win]::EnumWindows($cb, [IntPtr]::Zero)
    return $found
}

function Get-ChildClasses {
    param([IntPtr]$Parent)
    $classes = New-Object System.Collections.ArrayList
    $cb = [HostSmoke.Win+EnumWindowsProc]{
        param($h, $l)
        $sb = New-Object System.Text.StringBuilder 256
        [void][HostSmoke.Win]::GetClassName($h, $sb, $sb.Capacity)
        [void]$classes.Add($sb.ToString())
        return $true
    }
    [void][HostSmoke.Win]::EnumChildWindows($Parent, $cb, [IntPtr]::Zero)
    return $classes
}

$probe = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
$probe.Start(); $port = $probe.LocalEndpoint.Port; $probe.Stop()
if ($port -eq 8790) { throw 'Refusing production''s port.' }

$null = New-Item -ItemType Directory -Force -Path (Join-Path $DataRoot 'logs')
$config = Join-Path $DataRoot 'harness.json'
@{ listen = "127.0.0.1:$port"; workspace = (Join-Path $DataRoot 'scratch'); log_dir = (Join-Path $DataRoot 'logs'); memory = @{ dir = (Join-Path $DataRoot 'memory') } } |
    ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $config -Encoding utf8

$result = [ordered]@{
    port = $port; started = $false; ready = $false; host_window = $false
    webview_child = $false; child_classes = @(); window_height = 0; client_height = 0
    top_inset_px = -1; strip_is_top_edge = $false; closed_cleanly = $false; log_tail = ''
}

$process = Start-Process -FilePath $Exe -ArgumentList @('-config', $config, '-data-root', $DataRoot, '-app-root', $AppRoot, '-window') -PassThru -WindowStyle Hidden
$result.started = $true
try {
    $deadline = (Get-Date).AddSeconds($WaitSeconds)
    while ((Get-Date) -lt $deadline) {
        try { $null = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$port/chat" -TimeoutSec 1; $result.ready = $true; break } catch { Start-Sleep -Milliseconds 250 }
    }

    $host_ = $null
    while ((Get-Date) -lt $deadline) {
        $windows = @(Get-WindowsOf -ProcessId $process.Id -ClassLike 'Agent_b-host-window')
        if ($windows.Count) { $host_ = $windows[0].handle; break }
        Start-Sleep -Milliseconds 250
    }
    if ($host_) {
        $result.host_window = $true
        # Give WebView2 a moment to parent its render widget and load the page.
        $childDeadline = (Get-Date).AddSeconds(20)
        while ((Get-Date) -lt $childDeadline) {
            $classes = @(Get-ChildClasses -Parent $host_)
            if ($classes.Count) {
                $result.child_classes = $classes | Select-Object -Unique
                if ($classes -match 'Chrome') { $result.webview_child = $true; break }
            }
            Start-Sleep -Milliseconds 400
        }

        $w = New-Object HostSmoke.Win+RECT
        $c = New-Object HostSmoke.Win+RECT
        [void][HostSmoke.Win]::GetWindowRect($host_, [ref]$w)
        [void][HostSmoke.Win]::GetClientRect($host_, [ref]$c)
        $origin = New-Object HostSmoke.Win+POINT
        [void][HostSmoke.Win]::ClientToScreen($host_, [ref]$origin)
        $result.window_height = $w.bottom - $w.top
        $result.client_height = $c.bottom - $c.top
        $result.top_inset_px = $origin.y - $w.top
        # The claim: nothing of Windows' own sits above the page.
        $result.strip_is_top_edge = ($result.top_inset_px -eq 0)
    }
} finally {
    if (-not $process.HasExited) {
        # Hard stop 12: by PID, never by name.
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        $result.closed_cleanly = $true
    }
    $log = Get-ChildItem -LiteralPath (Join-Path $DataRoot 'logs') -Filter '*.log' -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($log) { $result.log_tail = (Get-Content -Tail 8 -LiteralPath $log.FullName) -join ' | ' }
}

$result | ConvertTo-Json -Depth 5
