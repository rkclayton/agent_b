[CmdletBinding()]
param(
    [Alias('RootDirectory')]
    [string]$ApplicationDirectory,
    [string]$DataDirectory,
    [string]$ConfigPath,
    [string]$SessionID,
    [ValidateRange(5, 300)]
    [int]$StartupTimeoutSeconds = 120,
    [switch]$NoBrowser,
    [switch]$Detached,
    [switch]$NoPause,
    [switch]$ShowFailure,
    [switch]$Console,
    [switch]$Check
)

$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($ApplicationDirectory)) {
    $ApplicationDirectory = Split-Path -Parent $PSScriptRoot
}
$applicationRoot = [IO.Path]::GetFullPath($ApplicationDirectory).TrimEnd('\')
if ([string]::IsNullOrWhiteSpace($DataDirectory)) {
    $DataDirectory = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'Agent_b'
}
$dataRoot = [IO.Path]::GetFullPath($DataDirectory).TrimEnd('\')
if ([string]::IsNullOrWhiteSpace($ConfigPath)) { $ConfigPath = Join-Path $dataRoot 'harness.json' }
$configPath = [IO.Path]::GetFullPath($ConfigPath)
$executable = Join-Path $applicationRoot 'Agent_b.exe'
$launcherErrorLog = Join-Path $dataRoot 'logs\launcher-errors.log'
$launcherLog = Join-Path $dataRoot 'logs\launcher.log'

function Show-AgentBLaunchFailure {
    param([string]$Message)
    if ([string]::IsNullOrWhiteSpace($Message) -and (Test-Path -LiteralPath $launcherErrorLog -PathType Leaf)) {
        $Message = [string](Get-Content -LiteralPath $launcherErrorLog -Tail 1)
    }
    if ([string]::IsNullOrWhiteSpace($Message)) { $Message = 'No failure detail was recorded.' }
    Write-Host "Agent_b launch failed: $Message"
    Write-Host 'Press any key to close.'
    if (-not $NoPause) { $null = [Console]::ReadKey($true) }
}

if ($ShowFailure) {
    Show-AgentBLaunchFailure
    exit 1
}

# Item 2ev: every launch decision is written down, so a second launch path
# (the sign-in start, a Start menu click) says what it did even when hidden.
function Write-LauncherRecord {
    param([string]$Message)
    Write-Host $Message
    try {
        $null = New-Item -ItemType Directory -Path (Split-Path -Parent $launcherLog) -Force
        Add-Content -LiteralPath $launcherLog -Encoding UTF8 -Value ('{0} {1}' -f [DateTime]::Now.ToString('yyyy-MM-dd HH:mm:ss zzz'), $Message)
    } catch { }
}

function Get-AgentBListener {
    param([string]$Url)
    $port = ([Uri]$Url).Port
    try {
        $owner = @(Get-NetTCPConnection -LocalAddress 127.0.0.1 -LocalPort $port -State Listen -ErrorAction Stop | Select-Object -First 1)
        if ($owner.Count) { return "PID $($owner[0].OwningProcess) answering on port $port" }
    } catch { }
    return "port $port"
}

trap {
    $message = ($_.Exception.Message -replace '[\r\n]+', ' ').Trim()
    $logged = $false
    try {
        $null = New-Item -ItemType Directory -Path (Split-Path -Parent $launcherErrorLog) -Force
        Add-Content -LiteralPath $launcherErrorLog -Encoding UTF8 -Value ('{0} {1}' -f [DateTime]::Now.ToString('yyyy-MM-dd HH:mm:ss zzz'), $message)
        $logged = $true
    } catch { }
    [Console]::Error.WriteLine("Agent_b launch failed: $message")
    if ($logged) {
        [Console]::Error.WriteLine("Details were appended to $launcherErrorLog")
    }
    if (-not $NoPause -and -not $Detached) {
        if ($env:AGENTB_HIDDEN_REENTRY) {
            $arguments = '-NoLogo -NoProfile -ExecutionPolicy Bypass -File "' + $PSCommandPath.Replace('"', '\"') +
                '" -ApplicationDirectory "' + $applicationRoot.Replace('"', '\"') +
                '" -DataDirectory "' + $dataRoot.Replace('"', '\"') +
                '" -ConfigPath "' + $configPath.Replace('"', '\"') + '" -ShowFailure'
            Start-Process -FilePath 'powershell.exe' -ArgumentList $arguments | Out-Null
        } else {
            Show-AgentBLaunchFailure -Message $message
        }
    }
    exit 1
}

function Get-AgentBUrl {
    if (-not (Test-Path -LiteralPath $configPath -PathType Leaf)) {
        return 'http://127.0.0.1:8790/'
    }
    try {
        $config = Get-Content -Raw -LiteralPath $configPath | ConvertFrom-Json
    } catch {
        $detail = ($_.Exception.Message -replace '[\r\n]+', ' ').Trim()
        if ($detail -match '(?i)access (?:is )?denied|permission denied|unauthorized') {
            throw "Permission error reading configuration ${configPath}: $detail"
        }
        throw "Configuration error in ${configPath}: $detail"
    }
    $listen = [string]$config.listen
    $separator = $listen.LastIndexOf(':')
    if ($separator -lt 0 -or $separator -eq $listen.Length - 1) {
        throw "The configured listen address is invalid: $listen"
    }
    $port = 0
    if (-not [int]::TryParse($listen.Substring($separator + 1), [ref]$port) -or $port -lt 1 -or $port -gt 65535) {
        throw "The configured listen port is invalid: $listen"
    }
    return "http://127.0.0.1:$port/"
}

function Test-AgentBEndpoint {
    param([string]$Url)
    try {
        $endpoint = [Uri]::new([Uri]$Url, 'chat')
        $response = Invoke-WebRequest -UseBasicParsing -Uri $endpoint -TimeoutSec 1
        return $response.StatusCode -eq 200
    } catch {
        return $false
    }
}

function Get-AgentBProcesses {
    return @(Get-CimInstance Win32_Process -Filter "Name='Agent_b.exe'" -ErrorAction SilentlyContinue | Where-Object {
        $_.ExecutablePath -and [IO.Path]::GetFullPath($_.ExecutablePath).Equals($executable, [StringComparison]::OrdinalIgnoreCase)
    })
}

# Item 2hc (v1.3.0/W2): the host window is the default way in when it can be
# opened, and the browser is the fallback when it cannot. The launcher decides
# BEFORE starting the server, from the same two facts the server itself checks:
# the pinned loader beside the exe, and an installed WebView2 runtime. The
# server re-checks and logs its own reason, so a disagreement degrades to the
# browser rather than to no window at all.
function Test-AgentBHostWindow {
    if ($env:AGENTB_NO_HOST_WINDOW) { return $false }
    $loader = Join-Path $applicationRoot 'WebView2Loader.dll'
    if (-not (Test-Path -LiteralPath $loader -PathType Leaf)) {
        Write-LauncherRecord 'HOST WINDOW: unavailable (WebView2Loader.dll is not beside the executable); opening the browser window instead'
        return $false
    }
    $clients = @(
        'HKLM:\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}',
        'HKLM:\SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}',
        'HKCU:\SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}'
    )
    foreach ($client in $clients) {
        try {
            $pv = (Get-ItemProperty -LiteralPath $client -Name pv -ErrorAction Stop).pv
            if ($pv) { return $true }
        } catch { }
    }
    Write-LauncherRecord 'HOST WINDOW: unavailable (the WebView2 runtime is not installed); opening the browser window instead'
    return $false
}

function Show-AgentBWindow {
    param([string]$Url)
    if ($NoBrowser -or $env:AGENTB_NO_BROWSER) {
        Write-Host "UI ready: $Url"
        return
    }

    Start-Process $Url
    Write-Host 'OPENED: Agent_b in the default browser'
}

function Invoke-AgentBActivation {
    $running = @(Get-AgentBProcesses | Select-Object -First 1)
    if (-not $running.Count) { throw 'Agent_b answered but its installed process could not be identified.' }
    & $executable -window -config $configPath -app-root $applicationRoot -data-root $dataRoot
    if ($LASTEXITCODE -ne 0) { throw "Agent_b activation handoff exited $LASTEXITCODE." }
    Write-LauncherRecord "activated existing window (PID $($running[0].ProcessId))"
}

function Wait-AgentBEndpoint {
    param([string]$Url, [Diagnostics.Process]$Process, [int]$Seconds)
    $deadline = [DateTime]::UtcNow.AddSeconds($Seconds)
    while ([DateTime]::UtcNow -lt $deadline) {
        if (Test-AgentBEndpoint -Url $Url) { return 'ready' }
        if ($Process -and $Process.HasExited) { return 'exited' }
        Start-Sleep -Milliseconds 250
    }
    return 'timeout'
}

function New-AgentBStartupCapture {
    $directory = Split-Path -Parent $launcherErrorLog
    $null = New-Item -ItemType Directory -Path $directory -Force
    $stamp = [DateTime]::Now.ToString('yyyyMMdd-HHmmss-fff') + '-' + [Guid]::NewGuid().ToString('N').Substring(0, 8)
    return Join-Path $directory "startup-$stamp.log"
}

function Get-AgentBStartupFailure {
    param([string]$StartupLogPath, [int]$Port)
    $lines = @()
    if ($StartupLogPath -and (Test-Path -LiteralPath $StartupLogPath -PathType Leaf)) {
        $lines = @(Get-Content -LiteralPath $StartupLogPath -ErrorAction SilentlyContinue | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
    }
    $detail = if ($lines.Count) { ([string]$lines[-1]).Trim() } else { 'the process produced no startup diagnostic' }
    if ($detail -match '(?i)bind:|address already in use|only one usage of each socket address') {
        return "listen port $Port is already in use: $detail"
    }
    if ($detail -match '(?i)access (?:is )?denied|permission denied|unauthorized') {
        return "permission error: $detail"
    }
    if ($detail -match '(?i)config|json|unmarshal|invalid character') {
        return "configuration error: $detail"
    }
    return "startup process error: $detail"
}

if (-not (Test-Path -LiteralPath $executable -PathType Leaf)) {
    throw "Agent_b executable is missing: $executable"
}
$url = Get-AgentBUrl
$chatPath = if ([string]::IsNullOrWhiteSpace($SessionID)) { 'chat' } else { 'chat?session=' + [Uri]::EscapeDataString($SessionID) }
$appUrl = [Uri]::new([Uri]$url, $chatPath).AbsoluteUri

if ($Check) {
    Write-Host 'Agent_b launcher check'
    Write-Host "Executable: $executable"
    Write-Host "Application root: $applicationRoot"
    Write-Host "Data root: $dataRoot"
    Write-Host "Configuration: $configPath"
    Write-Host "API base: $url"
    Write-Host "Application: $appUrl"
    Write-Host "Running instances: $((Get-AgentBProcesses).Count)"
    Write-Host "Endpoint ready: $(Test-AgentBEndpoint -Url $url)"
    Write-Host 'CHECK COMPLETE: nothing was started or stopped'
    exit 0
}

$created = $false
$mutex = [Threading.Mutex]::new($false, 'Local\Agent_b-Launch', [ref]$created)
$locked = $false
try {
    try { $locked = $mutex.WaitOne([TimeSpan]::FromSeconds(15)) } catch [Threading.AbandonedMutexException] { $locked = $true }
    if (-not $locked) {
        throw 'Another Agent_b launch is still being resolved. Wait a moment and try again.'
    }

    if (Test-AgentBEndpoint -Url $url) {
        Write-LauncherRecord "Agent_b is already running ($(Get-AgentBListener -Url $url)); no new server was started."
        if ($NoBrowser -or $env:AGENTB_NO_BROWSER) { Write-Host "UI ready: $appUrl" } else { Invoke-AgentBActivation }
        exit 0
    }

    $existing = Get-AgentBProcesses
    if ($existing.Count) {
        Write-LauncherRecord "Agent_b is already running as process $($existing[0].ProcessId) and not answering yet; waiting for its UI instead of starting another instance."
        $state = Wait-AgentBEndpoint -Url $url -Seconds $StartupTimeoutSeconds
        if ($state -eq 'ready') {
            if ($NoBrowser -or $env:AGENTB_NO_BROWSER) { Write-Host "UI ready: $appUrl" } else { Invoke-AgentBActivation }
            exit 0
        }
        $existing = Get-AgentBProcesses
        if ($existing.Count) {
            throw "Agent_b process $($existing[0].ProcessId) is still running but $url is not responding. It was not closed because it may own an active task. Close its console or end that process, then launch Agent_b again."
        }
        Write-Host 'The earlier Agent_b process exited; starting a fresh instance.'
    }

    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    if ($principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'Agent_b must be launched normally, not from an elevated terminal or Run as administrator.'
    }

    if ($Detached) {
        Write-Host 'Starting Agent_b in the background.'
    } else {
        Write-Host 'Starting Agent_b. Close this window or press Ctrl+C to stop it.'
    }
    # Item 2gw (v1.2.5): the startup log is written whichever way this is
    # launched. It used to be created only on the detached path, so a start
    # that failed in the foreground - or from Explorer - left no reason behind.
    $startupCapture = New-AgentBStartupCapture
    $configArgument = '-config "' + $configPath.Replace('"', '\"') + '" -app-root "' + $applicationRoot.Replace('"', '\"') + '" -data-root "' + $dataRoot.Replace('"', '\"') + '"'
    # Item 2hc: our own window, when it can be opened. The server re-checks and
    # logs its own reason, so this decision is a preference, not a promise.
    $script:hostWindow = (-not $NoBrowser -and -not $env:AGENTB_NO_BROWSER -and (Test-AgentBHostWindow))
    if ($script:hostWindow) { $configArgument += ' -window' }
    if ($startupCapture) {
        $configArgument += ' -startup-log "' + $startupCapture.Replace('"', '\"') + '"'
    }
    if ($Detached -or -not $Console) {
        # Item 2hg (v1.3.0/W6): a background server gets NO CONSOLE WINDOW and
        # INHERITS NO HANDLES. Both halves matter, and each was learned the
        # hard way.
        #
        # No console window, because -WindowStyle Hidden still gives a console
        # application one; taskkill without /F posts WM_CLOSE to every top-level
        # window, a WM_CLOSE on a console becomes CTRL_CLOSE_EVENT, and Go
        # delivers that as SIGTERM - so the server stopped even though item
        # 2eq's own window ignores WM_CLOSE. Measured: killed before the guard
        # window existed it died 12 times out of 12.
        #
        # No inherited handles, because the first fix used
        # [Diagnostics.Process]::Start with UseShellExecute=$false, and .NET
        # then hands the child the parent's stdout. A detached server therefore
        # held its launcher's output pipe open for as long as it ran, and any
        # caller capturing the launcher's output - the installer's own upgrade
        # path does exactly that - blocked until the server exited. The suite
        # hung there. Start-Process -WindowStyle Hidden never had that problem
        # because ShellExecute does not inherit; CreateProcess with
        # bInheritHandles = $false does not either, and unlike ShellExecute it
        # can also say CREATE_NO_WINDOW.
        Add-Type -Namespace AgentB -Name Spawn -MemberDefinition @'
[StructLayout(LayoutKind.Sequential)] public struct STARTUPINFO {
  public int cb; public string lpReserved, lpDesktop, lpTitle;
  public int dwX, dwY, dwXSize, dwYSize, dwXCountChars, dwYCountChars, dwFillAttribute, dwFlags;
  public short wShowWindow, cbReserved2; public IntPtr lpReserved2, hStdInput, hStdOutput, hStdError;
}
[StructLayout(LayoutKind.Sequential)] public struct PROCESS_INFORMATION {
  public IntPtr hProcess, hThread; public int dwProcessId, dwThreadId;
}
[DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
public static extern bool CreateProcess(string app, string commandLine, IntPtr pa, IntPtr ta,
  bool inherit, uint flags, IntPtr env, string cwd, ref STARTUPINFO si, out PROCESS_INFORMATION pi);
[DllImport("kernel32.dll", SetLastError=true)] public static extern bool CloseHandle(IntPtr h);
'@ -ErrorAction SilentlyContinue
        $startupInfo = New-Object AgentB.Spawn+STARTUPINFO
        $startupInfo.cb = [Runtime.InteropServices.Marshal]::SizeOf([type][AgentB.Spawn+STARTUPINFO])
        $processInfo = New-Object AgentB.Spawn+PROCESS_INFORMATION
        $commandLine = '"' + $executable + '" ' + $configArgument
        $CREATE_NO_WINDOW = 0x08000000
        if (-not [AgentB.Spawn]::CreateProcess($executable, $commandLine, [IntPtr]::Zero, [IntPtr]::Zero,
                $false, $CREATE_NO_WINDOW, [IntPtr]::Zero, $dataRoot, [ref]$startupInfo, [ref]$processInfo)) {
            throw "Starting Agent_b failed: CreateProcess reported $([Runtime.InteropServices.Marshal]::GetLastWin32Error())."
        }
        $null = [AgentB.Spawn]::CloseHandle($processInfo.hThread)
        $null = [AgentB.Spawn]::CloseHandle($processInfo.hProcess)
        $process = Get-Process -Id $processInfo.dwProcessId
    } else {
        # The foreground console start is unchanged: its window is the
        # operator's, and closing it is meant to stop the server.
        $process = Start-Process -FilePath $executable -ArgumentList $configArgument -WorkingDirectory $dataRoot -NoNewWindow -PassThru
    }
    $state = Wait-AgentBEndpoint -Url $url -Process $process -Seconds $StartupTimeoutSeconds
    if ($state -eq 'exited') {
        $process.WaitForExit()
        $detail = Get-AgentBStartupFailure -StartupLogPath $startupCapture -Port ([Uri]$url).Port
        throw "Agent_b failed to start: $detail (exit code $($process.ExitCode)). Diagnostics: $startupCapture"
    }
    if ($state -eq 'timeout') {
        Write-Warning "Agent_b process $($process.Id) is running, but $url did not become ready within $StartupTimeoutSeconds seconds. No browser was opened."
        Write-Host $(if ($Detached) { 'The process is being left running in the background; inspect logs or use -Check to confirm readiness.' } else { 'The process is being left running in this console so delayed startup remains visible.' })
    } else {
        Write-LauncherRecord "Agent_b is ready at $appUrl ($(Get-AgentBListener -Url $url)); started as process $($process.Id)."
        if ($script:hostWindow) {
            # The server opened its own window in its own process; opening a
            # browser too would give the operator two of the same thing.
            Write-LauncherRecord 'OPENED: Agent_b host window'
        } else {
            Show-AgentBWindow -Url $appUrl
        }
    }
} finally {
    if ($locked) { $mutex.ReleaseMutex() }
    $mutex.Dispose()
}

if ($Detached) {
    Write-Host "Agent_b is running in the background as process $($process.Id)."
    exit 0
}

$process.WaitForExit()
$exitCode = $process.ExitCode
if ($exitCode -ne 0) {
    throw "Agent_b stopped with exit code $exitCode."
}
Write-Host 'Agent_b stopped.'
exit 0
