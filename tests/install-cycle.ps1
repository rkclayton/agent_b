[CmdletBinding()]
param(
    # A built candidate folder: Agent_b.exe and candidate-final.json from tools\build-candidate.ps1.
    [string]$Candidate
)

# Item 2x0 (e): the path a person takes, on a clean machine, every step asserted:
# install and launch; a configuration from a release too old to read, then the
# installer over the top of the running copy; uninstall keeping the data; a fresh
# install over the kept data. Disposable roots under the temp folder, a free port
# that is never production's, no window, and the operator's install never touched.

$ErrorActionPreference = 'Stop'
if (-not $Candidate) { $Candidate = Split-Path -Parent $PSScriptRoot }
. (Join-Path $PSScriptRoot 'suite-production-guard.ps1')
. (Join-Path (Split-Path -Parent $PSScriptRoot) 'scripts\removal-guard.ps1')
$root = Join-Path ([IO.Path]::GetTempPath()) ('Agent_b-installer-test-' + [Guid]::NewGuid().ToString('N'))
$application = Join-Path $root 'Application\Agent_b'
$data = Join-Path $root 'Data\Agent_b'
$registry = 'HKCU:\Software\Agent_b-Installer-Test-' + [Guid]::NewGuid().ToString('N')
$setup = Join-Path $Candidate 'Agent_b.exe'
Assert-AgentBSuiteLaunch -ApplicationRoot $application -DataRoot $data -SuiteRoots @($root)
$env:AGENTB_NO_HOST_WINDOW = '1'
$env:AGENT_B_INSTALL_NO_BROWSER = '1'
$env:AGENT_B_INSTALL_LOG = Join-Path $root 'install.log'

function Get-FreePort {
    while ($true) {
        $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
        try { $listener.Start(); $port = ([Net.IPEndPoint]$listener.LocalEndpoint).Port } finally { $listener.Stop() }
        if ($port -ne 8790) { return $port }
    }
}
$port = Get-FreePort
$url = "http://127.0.0.1:$port/chat"

function Assert-Step {
    param([string]$Name, [bool]$Held, [string]$Detail = '')
    if (-not $Held) { throw "INSTALL CYCLE FAILED: $Name`n$Detail" }
    Write-Host "PASS $Name"
}

function Invoke-Setup {
    param([switch]$NoStart)
    $arguments = @('--install', '--quiet', '--install-source', $Candidate, '--install-data', $data,
        '-ApplicationDirectory', $application, '-DataDirectory', $data,
        '-WorkspaceDirectory', (Join-Path $root 'ProgramData\Agent_b\workspace'),
        '-StartMenuDirectory', (Join-Path $root 'StartMenu'), '-SendToDirectory', (Join-Path $root 'SendTo'),
        '-UninstallRegistryPath', $registry, '-NativeTestMode')
    if ($NoStart) { $arguments += '-NoStart' }
    $ErrorActionPreference = 'Continue'
    $output = & $setup @arguments 2>&1 | Out-String
    $ErrorActionPreference = 'Stop'
    return [pscustomobject]@{ Exit = $LASTEXITCODE; Output = $output + (Get-Content -Raw -LiteralPath $env:AGENT_B_INSTALL_LOG -ErrorAction SilentlyContinue) }
}

function Test-Answers {
    try { return (Invoke-WebRequest -UseBasicParsing -Uri $url -TimeoutSec 2).StatusCode -eq 200 } catch { return $false }
}

function Start-Installed {
    $ErrorActionPreference = 'Continue'
    $output = & powershell.exe -NoLogo -NoProfile -NonInteractive -File (Join-Path $application 'scripts\launch-Agent_b.ps1') -ApplicationDirectory $application -DataDirectory $data -Detached -NoBrowser -NoPause -StartupTimeoutSeconds 120 2>&1 | Out-String
    $ErrorActionPreference = 'Stop'
    return [pscustomobject]@{ Exit = $LASTEXITCODE; Output = $output }
}

function Get-Running {
    return @(Get-CimInstance Win32_Process -Filter "Name='Agent_b.exe'" | Where-Object { $_.ExecutablePath -and $_.ExecutablePath.StartsWith($application, [StringComparison]::OrdinalIgnoreCase) })
}

# The template the app writes a fresh configuration from listens on this run's port.
function Set-TemplatePort {
    $template = Join-Path $application 'harness.example.json'
    $text = [IO.File]::ReadAllText($template) -replace '"listen": "127\.0\.0\.1:\d+"', "`"listen`": `"127.0.0.1:$port`""
    [IO.File]::WriteAllText($template, $text, [Text.UTF8Encoding]::new($false))
}

function Write-Config {
    param([object]$Version)
    $config = Get-Content -Raw -LiteralPath (Join-Path $Candidate 'harness.example.json') | ConvertFrom-Json
    $config.listen = "127.0.0.1:$port"
    $config.workspace = Join-Path $data 'scratch'
    $config.log_dir = Join-Path $data 'logs'
    $config.memory.dir = Join-Path $data 'memory'
    if ($null -eq $Version) { $config.PSObject.Properties.Remove('config_version') } else { $config.config_version = $Version }
    # A release's file carries its profile catalog (profiles predate schema 14), so this one keeps it.
    $existing = Join-Path $data 'harness.json'
    if (Test-Path -LiteralPath $existing) {
        try { $profiles = (Get-Content -Raw -LiteralPath $existing | ConvertFrom-Json).profiles } catch { $profiles = $script:profiles }
        if ($profiles) { $script:profiles = $profiles; $config | Add-Member -NotePropertyName profiles -NotePropertyValue $profiles -Force }
    }
    $null = New-Item -ItemType Directory -Path $data -Force
    [IO.File]::WriteAllText((Join-Path $data 'harness.json'), ($config | ConvertTo-Json -Depth 100), [Text.UTF8Encoding]::new($false))
}

$passed = $false
try {
    Write-Config -Version 15
    $install = Invoke-Setup -NoStart
    Assert-Step 'fresh install exits 0' ($install.Exit -eq 0) $install.Output
    $launch = Start-Installed
    Assert-Step 'fresh install launches and answers' (($launch.Exit -eq 0) -and (Test-Answers)) $launch.Output
    $chat = Join-Path $data 'sessions\cycle-proof.jsonl'
    $null = New-Item -ItemType Directory -Path (Split-Path -Parent $chat) -Force
    [IO.File]::WriteAllText($chat, "{}`n")

    # The operator's case: the data folder holds a configuration from a release too
    # old to read, and the installer runs over the top of the running copy.
    Write-Config -Version 13
    $before = [IO.File]::ReadAllBytes((Join-Path $data 'harness.json'))
    $runningBefore = @(Get-Running).ProcessId
    $over = Invoke-Setup -NoStart
    Assert-Step 'install over the running copy exits 0' ($over.Exit -eq 0) $over.Output
    Assert-Step 'install over the top stopped the running copy' (-not (@(Get-Running).ProcessId | Where-Object { $runningBefore -contains $_ })) $over.Output
    Set-TemplatePort
    $launch = Start-Installed
    Assert-Step 'the new build starts over an older configuration and answers' (($launch.Exit -eq 0) -and (Test-Answers)) $launch.Output
    $kept = @(Get-ChildItem -LiteralPath $data -Filter 'harness.json.schema13-*')
    Assert-Step 'the older configuration is kept byte for byte' (($kept.Count -eq 1) -and ([Convert]::ToBase64String([IO.File]::ReadAllBytes($kept[0].FullName)) -eq [Convert]::ToBase64String($before)))

    # A file the app cannot read is named in the launcher's error, never a blank.
    foreach ($process in Get-Running) { Stop-Process -Id $process.ProcessId -Force }
    [IO.File]::WriteAllText((Join-Path $data 'harness.json'), '{invalid', [Text.UTF8Encoding]::new($false))
    $broken = Start-Installed
    $errors = Get-Content -Raw -LiteralPath (Join-Path $data 'logs\launcher-errors.log')
    Assert-Step 'an unreadable configuration is named' (($broken.Exit -ne 0) -and ($errors -match '(?i)configuration error') -and ($errors -match 'harness\.json')) $errors

    # Uninstall as Installed apps runs it: the registered command. It keeps the data
    # folder and says so once; the program folder goes.
    Write-Config -Version 15
    $quietUninstall = (Get-ItemProperty -LiteralPath $registry -Name QuietUninstallString).QuietUninstallString
    $ErrorActionPreference = 'Continue'
    $uninstall = & cmd.exe /d /s /c $quietUninstall 2>&1 | Out-String
    $uninstallExit = $LASTEXITCODE
    $ErrorActionPreference = 'Stop'
    Assert-Step 'the registered uninstall exits 0' ($uninstallExit -eq 0) $uninstall
    $deadline = [DateTime]::UtcNow.AddSeconds(60)
    while ((Test-Path -LiteralPath $application) -and [DateTime]::UtcNow -lt $deadline) { Start-Sleep -Milliseconds 250 }
    Assert-Step 'uninstall removed the program folder and its registration' ((-not (Test-Path -LiteralPath $application)) -and (-not (Test-Path -LiteralPath $registry)))
    $said = @(Select-String -LiteralPath $env:AGENT_B_INSTALL_LOG -Pattern 'UNINSTALL COMPLETE: Agent_b was removed\. Your chats and settings were kept')
    Assert-Step 'uninstall says once that the data was kept' ($said.Count -eq 1)
    Assert-Step 'uninstall kept the data folder and its chats' ((Test-Path -LiteralPath (Join-Path $data 'harness.json')) -and (Test-Path -LiteralPath $chat))

    $fresh = Invoke-Setup -NoStart
    Assert-Step 'fresh install over the kept data exits 0' ($fresh.Exit -eq 0) $fresh.Output
    $launch = Start-Installed
    Assert-Step 'fresh install over the kept data answers and the chat is there' (($launch.Exit -eq 0) -and (Test-Answers) -and (Test-Path -LiteralPath $chat)) $launch.Output
    $passed = $true
    Write-Host 'INSTALL CYCLE PASSED'
} finally {
    foreach ($process in Get-Running) { Stop-Process -Id $process.ProcessId -Force -ErrorAction SilentlyContinue }
    Remove-Item -LiteralPath $registry -Recurse -Force -ErrorAction SilentlyContinue
    if ($passed) { Remove-TreeWithinAllowedRoots -Path $root -AllowedRoots @([IO.Path]::GetTempPath()) -Purpose 'install-cycle cleanup' } else { Write-Host "Kept for reading: $root" }
}
