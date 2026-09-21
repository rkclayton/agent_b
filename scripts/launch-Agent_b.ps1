[CmdletBinding()]
param(
    [Alias('RootDirectory')]
    [string]$ApplicationDirectory,
    [string]$DataDirectory,
    [string]$ConfigPath,
    [ValidateRange(5, 300)]
    [int]$StartupTimeoutSeconds = 120,
    [switch]$NoBrowser,
    [switch]$Detached,
    [switch]$NoPause,
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
        $endpoint = [Uri]::new([Uri]$Url, 'api/state')
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

function Show-AgentBWindow {
    param([string]$Url)
    if ($NoBrowser -or $env:AGENTB_NO_BROWSER) {
        Write-Host "UI ready: $Url"
        return
    }

    # Item 2ev: only a window opened on this installation's own URL is reused. A
    # title match used to bring forward, or close, any "Agent_b" window, which
    # could be a retired instance on another port or an ordinary browser window
    # sharing the process. Nothing is closed: a window left open across an
    # upgrade reloads itself when its event stream reaches the new server.
    $origin = [Uri]::new([Uri]$Url, '/').AbsoluteUri

    # Item 2gl: the app id, looked up fresh, because Edge installs the app on
    # its own schedule after the policy is written rather than when we ask.
    # Refused unless the profile's own record carries our url; $null means
    # --app=, which is what production does today, so doubt costs nothing.
    $appId = $null
    try {
        . (Join-Path $PSScriptRoot 'pwa-policy.ps1')
        $appId = Find-AgentBPwaAppId -Url (Get-AgentBPwaUrl -Origin $origin)
    } catch { $appId = $null }

    $ownWindows = @{}
    foreach ($candidate in @(Get-CimInstance Win32_Process -Filter "Name='msedge.exe' OR Name='chrome.exe'" -ErrorAction SilentlyContinue)) {
        $commandLine = [string]$candidate.CommandLine
        if (-not $commandLine) { continue }
        # Either shape of our own window counts as ours to bring forward.
        $mine = $commandLine.Contains('--app=' + $origin)
        if (-not $mine -and $appId) { $mine = $commandLine.Contains('--app-id=' + $appId) }
        if ($mine) { $ownWindows[[int]$candidate.ProcessId] = $true }
    }
    $shell = New-Object -ComObject WScript.Shell
    foreach ($name in @('msedge', 'chrome')) {
        foreach ($browser in Get-Process -Name $name -ErrorAction SilentlyContinue) {
            if ($ownWindows.ContainsKey($browser.Id) -and $browser.MainWindowHandle -ne [IntPtr]::Zero -and $shell.AppActivate($browser.Id)) {
                Write-Host 'REUSED: existing Agent_b browser window'
                return
            }
        }
    }

    $edgeCandidates = @(
        $(if (${env:ProgramFiles(x86)}) { Join-Path ${env:ProgramFiles(x86)} 'Microsoft\Edge\Application\msedge.exe' }),
        $(if ($env:ProgramFiles) { Join-Path $env:ProgramFiles 'Microsoft\Edge\Application\msedge.exe' })
    ) | Where-Object { $_ -and (Test-Path -LiteralPath $_ -PathType Leaf) }
    $edge = $edgeCandidates | Select-Object -First 1
    if (-not $edge) {
        $edgeCommand = Get-Command msedge.exe -ErrorAction SilentlyContinue
        if ($edgeCommand) { $edge = $edgeCommand.Source }
    }
    if ($edge) {
        # Item 2gl: the installed app when Edge has one, the flag until it does.
        # --app-id= opens the app Edge installed from our policy entry, which is
        # the window the controls overlay needs; --app= opens the same URL in an
        # app window that is not an installed app. The id is looked up fresh on
        # every launch because the install happens on Edge's schedule, not ours,
        # and it is refused unless the profile's own record carries our url.
        # Anything doubtful falls back to --app=, which is what we ship today.
        if ($appId) {
            Start-Process -FilePath $edge -ArgumentList "--app-id=$appId"
            Write-LauncherRecord "OPENED: Agent_b installed app window (--app-id=$appId)"
        } else {
            Start-Process -FilePath $edge -ArgumentList "--app=$Url"
            Write-LauncherRecord 'OPENED: Agent_b application window (--app=; Edge has not installed the app yet)'
        }
        return
    }
    Start-Process $Url
    Write-Host 'OPENED: Agent_b in the default browser'
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
$appUrl = [Uri]::new([Uri]$url, 'chat').AbsoluteUri

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
        Show-AgentBWindow -Url $appUrl
        exit 0
    }

    $existing = Get-AgentBProcesses
    if ($existing.Count) {
        Write-LauncherRecord "Agent_b is already running as process $($existing[0].ProcessId) and not answering yet; waiting for its UI instead of starting another instance."
        $state = Wait-AgentBEndpoint -Url $url -Seconds $StartupTimeoutSeconds
        if ($state -eq 'ready') {
            Show-AgentBWindow -Url $appUrl
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
    if ($startupCapture) {
        $configArgument += ' -startup-log "' + $startupCapture.Replace('"', '\"') + '"'
    }
    $start = @{
        FilePath = $executable
        ArgumentList = $configArgument
        WorkingDirectory = $dataRoot
        PassThru = $true
    }
    if ($Detached -or -not $Console) {
        $start.WindowStyle = 'Hidden'
    } else {
        $start.NoNewWindow = $true
    }
    $process = Start-Process @start
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
        Show-AgentBWindow -Url $appUrl
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
