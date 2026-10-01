[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Exe,
    [Parameter(Mandatory = $true)][string]$Setup,
    [Parameter(Mandatory = $true)][string]$ApplicationRoot
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'suite-production-guard.ps1')
$repository = Split-Path -Parent $PSScriptRoot
. (Join-Path $repository 'scripts\removal-guard.ps1')
$temp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\')
$root = Join-Path $temp ('Agent_b-one-window-' + [Guid]::NewGuid().ToString('N'))
$data = Join-Path $root 'data'
$workspace = Join-Path $root 'workspace'
Assert-AgentBSuiteLaunch -ApplicationRoot $ApplicationRoot -DataRoot $data -SuiteRoots @($root, $ApplicationRoot)

function Get-PESubsystem([string]$Path) {
    $bytes = [IO.File]::ReadAllBytes($Path)
    $pe = [BitConverter]::ToInt32($bytes, 0x3c)
    return [BitConverter]::ToUInt16($bytes, $pe + 24 + 68)
}
foreach ($path in @($Exe, $Setup)) {
    $subsystem = Get-PESubsystem $path
    if ($subsystem -ne 2) { throw "$(Split-Path -Leaf $path) subsystem is $subsystem, expected WINDOWS_GUI (2)" }
}

Add-Type -Namespace AgentBInvariant -Name Desktop -MemberDefinition @'
public delegate bool EnumDesktopProc(System.IntPtr hwnd, System.IntPtr value);
[System.Runtime.InteropServices.StructLayout(System.Runtime.InteropServices.LayoutKind.Sequential, CharSet=System.Runtime.InteropServices.CharSet.Unicode)]
public struct STARTUPINFO { public int cb; public string reserved, desktop, title; public int x,y,xs,ys,xc,yc,fill,flags; public short show,cb2; public System.IntPtr reserved2,input,output,error; }
[System.Runtime.InteropServices.StructLayout(System.Runtime.InteropServices.LayoutKind.Sequential)]
public struct PROCESS_INFORMATION { public System.IntPtr process, thread; public int pid, tid; }
[System.Runtime.InteropServices.DllImport("user32.dll", CharSet=System.Runtime.InteropServices.CharSet.Unicode, SetLastError=true)] public static extern System.IntPtr CreateDesktop(string name, System.IntPtr device, System.IntPtr mode, uint flags, uint access, System.IntPtr attributes);
[System.Runtime.InteropServices.DllImport("user32.dll", SetLastError=true)] public static extern bool EnumDesktopWindows(System.IntPtr desktop, EnumDesktopProc callback, System.IntPtr value);
[System.Runtime.InteropServices.DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(System.IntPtr hwnd, out uint pid);
[System.Runtime.InteropServices.DllImport("user32.dll")] public static extern bool IsWindowVisible(System.IntPtr hwnd);
[System.Runtime.InteropServices.DllImport("user32.dll", CharSet=System.Runtime.InteropServices.CharSet.Unicode)] public static extern int GetClassName(System.IntPtr hwnd, System.Text.StringBuilder value, int length);
[System.Runtime.InteropServices.DllImport("user32.dll")] public static extern bool CloseDesktop(System.IntPtr desktop);
[System.Runtime.InteropServices.DllImport("kernel32.dll", CharSet=System.Runtime.InteropServices.CharSet.Unicode, SetLastError=true)] public static extern bool CreateProcess(string app, string command, System.IntPtr pa, System.IntPtr ta, bool inherit, uint flags, System.IntPtr environment, string cwd, ref STARTUPINFO startup, out PROCESS_INFORMATION process);
[System.Runtime.InteropServices.DllImport("kernel32.dll")] public static extern bool TerminateProcess(System.IntPtr process, uint exitCode);
[System.Runtime.InteropServices.DllImport("kernel32.dll", CharSet=System.Runtime.InteropServices.CharSet.Unicode)] public static extern System.IntPtr CreateJobObject(System.IntPtr attributes, string name);
[System.Runtime.InteropServices.DllImport("kernel32.dll")] public static extern bool AssignProcessToJobObject(System.IntPtr job, System.IntPtr process);
[System.Runtime.InteropServices.DllImport("kernel32.dll")] public static extern bool TerminateJobObject(System.IntPtr job, uint exitCode);
[System.Runtime.InteropServices.DllImport("kernel32.dll")] public static extern uint ResumeThread(System.IntPtr thread);
[System.Runtime.InteropServices.DllImport("kernel32.dll")] public static extern uint WaitForSingleObject(System.IntPtr handle, uint milliseconds);
[System.Runtime.InteropServices.DllImport("kernel32.dll")] public static extern bool CloseHandle(System.IntPtr handle);
'@

$desktopName = 'AgentBInvariant-' + [Guid]::NewGuid().ToString('N')
$desktop = [AgentBInvariant.Desktop]::CreateDesktop($desktopName, [IntPtr]::Zero, [IntPtr]::Zero, 0, 0x01ff, [IntPtr]::Zero)
if ($desktop -eq [IntPtr]::Zero) { throw "CreateDesktop failed: $([Runtime.InteropServices.Marshal]::GetLastWin32Error())" }
$process = [AgentBInvariant.Desktop+PROCESS_INFORMATION]::new()
$job = [AgentBInvariant.Desktop]::CreateJobObject([IntPtr]::Zero, $null)
if ($job -eq [IntPtr]::Zero) { throw 'CreateJobObject failed' }
try {
    New-Item -ItemType Directory -Force -Path $data, $workspace | Out-Null
    $config = Get-Content -Raw -LiteralPath (Join-Path $repository 'harness.example.json') | ConvertFrom-Json
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    try { $listener.Start(); $port = ([Net.IPEndPoint]$listener.LocalEndpoint).Port } finally { $listener.Stop() }
    $config.listen = "127.0.0.1:$port"
    $config.workspace = $workspace
    $config.log_dir = (Join-Path $data 'logs')
    $config.shell.service_account.enabled = $false
    $config.updates.auto_check = $false
    $configPath = Join-Path $data 'harness.json'
    [IO.File]::WriteAllText($configPath, ($config | ConvertTo-Json -Depth 100), [Text.UTF8Encoding]::new($false))
    $startup = [AgentBInvariant.Desktop+STARTUPINFO]::new()
    $startup.cb = [Runtime.InteropServices.Marshal]::SizeOf($startup)
    $startup.desktop = $desktopName
    $command = '"' + $Exe + '" -window -config "' + $configPath + '" -app-root "' + $ApplicationRoot + '" -data-root "' + $data + '"'
    if (-not [AgentBInvariant.Desktop]::CreateProcess($Exe, $command, [IntPtr]::Zero, [IntPtr]::Zero, $false, 0x404, [IntPtr]::Zero, $data, [ref]$startup, [ref]$process)) {
        throw "CreateProcess failed: $([Runtime.InteropServices.Marshal]::GetLastWin32Error())"
    }
    if (-not [AgentBInvariant.Desktop]::AssignProcessToJobObject($job, $process.process)) { throw 'AssignProcessToJobObject failed' }
    [AgentBInvariant.Desktop]::ResumeThread($process.thread) | Out-Null
    [AgentBInvariant.Desktop]::CloseHandle($process.thread) | Out-Null
    $classes = @()
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    do {
        $script:classes = [Collections.Generic.List[string]]::new()
        $callback = [AgentBInvariant.Desktop+EnumDesktopProc]{ param($window,$unused)
            $owner = 0; [void][AgentBInvariant.Desktop]::GetWindowThreadProcessId($window,[ref]$owner)
            if ($owner -eq $process.pid -and [AgentBInvariant.Desktop]::IsWindowVisible($window)) {
                $name = [Text.StringBuilder]::new(128); [void][AgentBInvariant.Desktop]::GetClassName($window,$name,$name.Capacity); $script:classes.Add($name.ToString())
            }; return $true
        }
        [void][AgentBInvariant.Desktop]::EnumDesktopWindows($desktop,$callback,[IntPtr]::Zero)
        $classes = @($script:classes)
        if ($classes -contains 'Agent_b-host-window') { break }
        Start-Sleep -Milliseconds 200
    } while ([DateTime]::UtcNow -lt $deadline)
    if ($classes.Count -ne 1 -or $classes[0] -ne 'Agent_b-host-window') { throw "isolated desktop windows: $($classes -join ', ')" }
    Write-Host "PASS one-window invariant: Agent_b.exe and Agent_b-setup.exe are WINDOWS_GUI; isolated desktop classes: $($classes -join ', ')"
} finally {
    if ($job -ne [IntPtr]::Zero) { [AgentBInvariant.Desktop]::TerminateJobObject($job, 0) | Out-Null }
    if ($process.process -ne [IntPtr]::Zero) { [AgentBInvariant.Desktop]::WaitForSingleObject($process.process, 15000) | Out-Null; [AgentBInvariant.Desktop]::CloseHandle($process.process) | Out-Null }
    if ($job -ne [IntPtr]::Zero) { [AgentBInvariant.Desktop]::CloseHandle($job) | Out-Null }
    [AgentBInvariant.Desktop]::CloseDesktop($desktop) | Out-Null
    if (Test-Path -LiteralPath $root) { Remove-TreeWithinAllowedRoots -Path $root -AllowedRoots @($temp) -Purpose 'one-window invariant cleanup' }
}
