# Item 2lh (c) and (d): the whole updater path on disposable roots. A disposable
# instance of the PREVIOUS release reads a release feed, downloads, verifies,
# installs and comes back on the NEW version, entirely beneath this suite's root.
#
# rel-1.13.2/W8 could not do this: the updater invoked the setup with no roots, so
# the setup resolved the operator's own per-user locations and a disposable
# self-update would have installed over production. That is why the launch half of
# the updater path had never been gated.
[CmdletBinding()]
param(
    # The setup the disposable instance starts from, and the one the feed offers.
    [Parameter(Mandatory)][string]$FromSetup,
    [Parameter(Mandatory)][string]$ToSetup,
    [Parameter(Mandatory)][ValidatePattern('^v\d+\.\d+\.\d+$')][string]$ToVersion,
    [string]$EvidenceDirectory
)
$ErrorActionPreference = 'Stop'
. (Join-Path (Split-Path -Parent $PSScriptRoot) 'scripts\removal-guard.ps1')

foreach ($path in @($FromSetup, $ToSetup)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "UPDATER CYCLE REFUSED: missing setup $path" }
}
$root = Join-Path ([IO.Path]::GetTempPath()) ('agentb-updater-cycle-' + [Guid]::NewGuid().ToString('N'))
$application = Join-Path $root 'Application\Agent_b'
$data = Join-Path $root 'Data\Agent_b'
$workspace = Join-Path $root 'workspace'
$feedRoot = Join-Path $root 'feed'
$registry = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\Agent_b-UpdaterCycle'
$null = New-Item -ItemType Directory -Path $feedRoot -Force
$listener = $null
$child = $null
$productionBefore = @(Get-Process -Name Agent_b -ErrorAction SilentlyContinue | ForEach-Object { $_.Id }) -join ','

function Get-FreePort {
    $probe = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $probe.Start(); $port = $probe.LocalEndpoint.Port; $probe.Stop()
    if ($port -eq 8790) { throw 'UPDATER CYCLE REFUSED: the probe picked the production port' }
    return $port
}

try {
    # 1. Install the previous release beneath the suite root, without starting it.
    $feedPort = Get-FreePort
    Copy-Item -LiteralPath $ToSetup -Destination (Join-Path $feedRoot 'Agent_b-setup.exe') -Force
    $setupBytes = (Get-Item (Join-Path $feedRoot 'Agent_b-setup.exe')).Length
    $setupHash = (Get-FileHash (Join-Path $feedRoot 'Agent_b-setup.exe') -Algorithm SHA256).Hash.ToLowerInvariant()
    @{ schema = 1; version = $ToVersion; commit = ('0' * 40); file = 'Agent_b-setup.exe'; sha256 = $setupHash; bytes = $setupBytes } |
        ConvertTo-Json -Depth 5 | Set-Content (Join-Path $feedRoot 'release.json') -Encoding utf8
    $base = "http://127.0.0.1:$feedPort"
    @{ tag_name = $ToVersion; body = "Agent_b $ToVersion"; assets = @(
        @{ name = 'release.json'; browser_download_url = "$base/release.json" },
        @{ name = 'Agent_b-setup.exe'; browser_download_url = "$base/Agent_b-setup.exe" }) } |
        ConvertTo-Json -Depth 6 | Set-Content (Join-Path $feedRoot 'latest.json') -Encoding utf8

    # 2. A loopback feed, so the cycle needs no network and no published release.
    $listener = [Net.HttpListener]::new()
    $listener.Prefixes.Add("$base/")
    $listener.Start()
    $serve = [powershell]::Create()
    $null = $serve.AddScript({
        param($listener, $feedRoot)
        while ($listener.IsListening) {
            try { $context = $listener.GetContext() } catch { break }
            $name = $context.Request.Url.AbsolutePath.TrimStart('/')
            if ($name -eq '' -or $name -eq 'latest') { $name = 'latest.json' }
            $file = Join-Path $feedRoot $name
            if (Test-Path -LiteralPath $file -PathType Leaf) {
                $bytes = [IO.File]::ReadAllBytes($file)
                $context.Response.ContentType = if ($name -like '*.json') { 'application/json' } else { 'application/octet-stream' }
                $context.Response.ContentLength64 = $bytes.Length
                $context.Response.OutputStream.Write($bytes, 0, $bytes.Length)
            } else { $context.Response.StatusCode = 404 }
            $context.Response.Close()
        }
    }).AddArgument($listener).AddArgument($feedRoot)
    $null = $serve.BeginInvoke()

    $install = & $FromSetup --quiet --install-data (Join-Path $root 'Data') -NoStart `
        -ApplicationDirectory $application -DataDirectory $data -WorkspaceDirectory $workspace `
        -StartMenuDirectory (Join-Path $root 'StartMenu') -UninstallRegistryPath $registry -TestMode 2>&1 | Out-String
    if (-not (Test-Path -LiteralPath (Join-Path $application 'Agent_b.exe') -PathType Leaf)) {
        throw "UPDATER CYCLE REFUSED: the previous release did not install beneath the suite root.`n$install"
    }
    $before = (& (Join-Path $application 'Agent_b.exe') -version | ConvertFrom-Json)
    Write-Host "CYCLE FROM: $($before.tag) $($before.commit)"

    # 3. Start it on its own port, with the loopback feed as its update source.
    $port = Get-FreePort
    $config = Get-Content (Join-Path $data 'harness.json') -Raw | ConvertFrom-Json
    $config.listen = "127.0.0.1:$port"
    $config.updates.auto_check = $false
    $config | ConvertTo-Json -Depth 20 | Set-Content (Join-Path $data 'harness.json') -Encoding utf8
    $env:AGENTB_UPDATE_FIXTURE_URL = "$base/latest.json"
    $child = Start-Process -FilePath (Join-Path $application 'Agent_b.exe') -PassThru -WindowStyle Hidden `
        -ArgumentList @('-config', (Join-Path $data 'harness.json'), '-app-root', $application, '-data-root', $data)
    $deadline = (Get-Date).AddSeconds(120); $ready = $false
    while ((Get-Date) -lt $deadline) {
        try { $null = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$port/api/connections" -TimeoutSec 3; $ready = $true; break } catch { Start-Sleep -Milliseconds 400 }
    }
    if (-not $ready) { throw "UPDATER CYCLE REFUSED: the disposable instance never became ready on $port" }

    # 4. Ask it to update itself, through its own updater.
    $session = New-Object Microsoft.PowerShell.Commands.WebRequestSession
    $document = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$port/chat" -WebSession $session -TimeoutSec 20
    $token = [regex]::Match([string]$document.Content, '<meta name="agentb-mutation-token" content="([^"]+)">').Groups[1].Value
    if (-not $token) { throw 'UPDATER CYCLE REFUSED: no mutation token' }
    $check = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$port/api/update" -Method POST -WebSession $session `
        -Headers @{ 'X-AgentB-Mutation-Token' = $token } -ContentType 'application/json' -Body '{"action":"check"}' -TimeoutSec 300
    Write-Host "CYCLE CHECK: $($check.Content)"
    if (($check.Content | ConvertFrom-Json).available -ne $true) { throw "UPDATER CYCLE REFUSED: the feed offering $ToVersion was not seen as available" }
    $install = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$port/api/update" -Method POST -WebSession $session `
        -Headers @{ 'X-AgentB-Mutation-Token' = $token } -ContentType 'application/json' -Body '{"action":"install"}' -TimeoutSec 600
    Write-Host "CYCLE INSTALL HTTP $([int]$install.StatusCode): $($install.Content)"

    # 5. It must come back on the new version, and nothing may be written outside.
    $deadline = (Get-Date).AddSeconds(300); $installed = $null
    while ((Get-Date) -lt $deadline) {
        try {
            $identity = & (Join-Path $application 'Agent_b.exe') -version 2>$null | ConvertFrom-Json
            if ($identity.tag -eq $ToVersion) { $installed = $identity; break }
        } catch { }
        Start-Sleep -Seconds 2
    }
    if (-not $installed) { throw "UPDATER CYCLE FAILED: the disposable install is still $((& (Join-Path $application 'Agent_b.exe') -version | ConvertFrom-Json).tag), not $ToVersion" }
    Write-Host "CYCLE TO: $($installed.tag) $($installed.commit)"
    $productionAfter = @(Get-Process -Name Agent_b -ErrorAction SilentlyContinue | Where-Object {
        try { $_.Path -and -not $_.Path.StartsWith($root, [StringComparison]::OrdinalIgnoreCase) } catch { $false }
    } | ForEach-Object { $_.Id }) -join ','
    if ($productionBefore -cne $productionAfter) { throw "UPDATER CYCLE REFUSED: processes outside the suite root changed: '$productionBefore' then '$productionAfter'" }
    Write-Host "PROOF disposable updater cycle: $($before.tag) updated itself to $($installed.tag) beneath $root; no process outside it changed"
    Write-Host 'UPDATER CYCLE PASS'
    if ($EvidenceDirectory) {
        $null = New-Item -ItemType Directory -Path $EvidenceDirectory -Force
        @{ from = $before.tag; to = $installed.tag; root = $root; setup_sha256 = $setupHash; setup_bytes = $setupBytes } |
            ConvertTo-Json -Depth 5 | Set-Content (Join-Path $EvidenceDirectory 'updater-cycle.json') -Encoding utf8
    }
} finally {
    if ($child -and -not $child.HasExited) { Stop-Process -Id $child.Id -Force -ErrorAction SilentlyContinue }
    Get-Process -Name Agent_b -ErrorAction SilentlyContinue | Where-Object {
        try { $_.Path -and $_.Path.StartsWith($root, [StringComparison]::OrdinalIgnoreCase) } catch { $false }
    } | ForEach-Object { Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue }
    if ($listener) { try { $listener.Stop(); $listener.Close() } catch { } }
    Start-Sleep -Seconds 2
    if (Test-Path -LiteralPath $registry) { Remove-Item -LiteralPath $registry -Recurse -Force -ErrorAction SilentlyContinue }
    try { Remove-TreeWithinAllowedRoots -Path $root -AllowedRoots @([IO.Path]::GetTempPath()) -Purpose 'updater cycle cleanup' } catch { Write-Warning $_.Exception.Message }
    Remove-Item Env:AGENTB_UPDATE_FIXTURE_URL -ErrorAction SilentlyContinue
}
