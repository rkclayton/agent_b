# Item 2lh (c) and (d): the whole updater path on disposable roots. A disposable
# instance of one release reads a release feed, downloads, verifies and launches
# the setup, entirely beneath this suite root.
#
# rel-1.13.2/W8 could not do this: the updater invoked the setup with no roots, so
# the setup resolved the operator own per-user locations and a disposable
# self-update would have installed over production. That is why the launch half of
# the updater path had never been gated.
#
# NARROWED per 2lh @consequence-if-false, whose @verify assumption rel-1.14.0/W8
# refuted: the suite CANNOT host the install half. scripts/install-Agent_b.ps1
# refuses any non-canonical ApplicationDirectory outside TestMode, and the updater
# must never be able to pass TestMode, so a disposable instance can never complete
# an install beneath the suite root -- which is the same guard that stops it
# landing on the operator installation. This gate therefore covers download,
# verification and launch WITH THE INSTALL TARGET ASSERTED, accepts a completed
# install when one is possible, and names the half that stays unexercised.
#
# At release N the FROM build carries the updater under test, so this gate proves
# release N-1 updater. Its first passing run is v1.14.0 -> v1.15.0; run from a
# build older than v1.14.0 it fails on the launch target, correctly.
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
# install-root-policy requires a TestMode uninstall key to be recognisably
# disposable: Agent_b followed by Test, Acceptance, or a long hex run.
$registry = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\Agent_b-UpdaterCycleTest'
$null = New-Item -ItemType Directory -Path $feedRoot -Force
$listener = $null
$child = $null
$productionBefore = @(Get-Process -Name Agent_b -ErrorAction SilentlyContinue | ForEach-Object { $_.Id }) -join ','
# The operator installation is the thing this gate must never touch, so its
# identity is recorded before anything runs and compared after.
$operatorApplication = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'Programs\Agent_b'
$operatorKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\Agent_b'
$operatorBefore = if (Test-Path -LiteralPath (Join-Path $operatorApplication 'Agent_b.exe')) {
    (Get-FileHash (Join-Path $operatorApplication 'Agent_b.exe') -Algorithm SHA256).Hash
} else { 'absent' }
$keyBefore = if (Test-Path -LiteralPath $operatorKey) { (Get-ItemProperty -LiteralPath $operatorKey).DisplayVersion } else { 'absent' }

# Item 2ll (c): the check that would have caught the leak. Everything the
# operator's own data root holds before this gate runs, so anything the launched
# setup writes there is visible as a difference rather than as a warning nobody
# reads.
$operatorData = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'Agent_b'
$operatorDataBefore = @(Get-ChildItem -LiteralPath $operatorData -Recurse -File -Force -ErrorAction SilentlyContinue |
    ForEach-Object { $_.FullName }) | Sort-Object

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
    # The release manifest the deploy step produced beside this setup is served
    # verbatim: the updater checks the executable identity it carries, so a
    # synthetic manifest would prove less than the real one and fail on identity.
    $manifest = Join-Path (Split-Path -Parent $ToSetup) 'release.json'
    if (-not (Test-Path -LiteralPath $manifest -PathType Leaf)) { throw "UPDATER CYCLE REFUSED: no release.json beside $ToSetup" }
    $manifestBody = Get-Content -Raw -LiteralPath $manifest | ConvertFrom-Json
    if ($manifestBody.sha256 -cne $setupHash -or [long]$manifestBody.bytes -ne $setupBytes) {
        throw "UPDATER CYCLE REFUSED: release.json does not describe the setup it sits beside"
    }
    Copy-Item -LiteralPath $manifest -Destination (Join-Path $feedRoot 'release.json') -Force
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
    # The updater under test is the one in the FROM build, so at release N this
    # gate proves N-1 updater. A build that predates item 2lh passes no roots at
    # all, and the launch-target assertion below is what says so; it is stated up
    # front so a failure from that cause reads as the cause and not as a surprise.
    $preFix = [version]($before.tag -replace '^v') -lt [version]'1.14.0'
    if ($preFix) { Write-Host "CYCLE NOTE: $($before.tag) predates item 2lh, so its updater names no roots; the launch-target assertion is expected to fail" }

    # 3. Start it on its own port, with the loopback feed as its update source.
    $port = Get-FreePort
    $config = Get-Content (Join-Path $data 'harness.json') -Raw | ConvertFrom-Json
    $config.listen = "127.0.0.1:$port"
    # The updater answers only while it is enabled, so the cycle enables it and
    # points it at the loopback feed rather than at the real release page.
    $config.updates.auto_check = $true
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
    # Anything the launched setup writes is newer than this instant.
    $launchedAt = Get-Date
    $install = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$port/api/update" -Method POST -WebSession $session `
        -Headers @{ 'X-AgentB-Mutation-Token' = $token } -ContentType 'application/json' -Body '{"action":"install"}' -TimeoutSec 600
    Write-Host "CYCLE INSTALL HTTP $([int]$install.StatusCode): $($install.Content)"

    # 5. The install must land where the asking instance lives, and nowhere else.
    #
    # Item 2lh @consequence-if-false: if a full cycle cannot run beneath the suite
    # root, this gate covers download, verification and launch with the install
    # target asserted, and names what stays unexercised. The installer refuses any
    # non-canonical ApplicationDirectory outside TestMode, so the install half of a
    # disposable self-update cannot succeed — which is itself the guarantee that a
    # disposable instance can never land on the operator's installation.
    $decision = $null
    $progress = Join-Path $data 'install-progress.jsonl'
    # 2ll: the progress file belongs beneath the instance's own data root now,
    # so that is the only place this looks. A file in the operator's root is a
    # leak the assertion below fails on, not a second place to read from.
    $deadline = (Get-Date).AddSeconds(180); $installed = $null
    while ((Get-Date) -lt $deadline) {
        try {
            $identity = & (Join-Path $application 'Agent_b.exe') -version 2>$null | ConvertFrom-Json
            if ($identity.tag -eq $ToVersion) { $installed = $identity; break }
        } catch { }
        if (Test-Path -LiteralPath $progress -PathType Leaf) {
            $lines = @(Get-Content -LiteralPath $progress | Where-Object { $_.Trim() })
            $last = $lines | Where-Object { $_ -match '"done":true' } | Select-Object -Last 1
            if ($last) { $decision = $last }
        }
        if ($decision) { break }
        Start-Sleep -Seconds 2
    }

    # Whichever way it went, the operator's installation must be exactly as it was.
    $operatorAfter = if (Test-Path -LiteralPath (Join-Path $operatorApplication 'Agent_b.exe')) {
        (Get-FileHash (Join-Path $operatorApplication 'Agent_b.exe') -Algorithm SHA256).Hash
    } else { 'absent' }
    $keyAfter = if (Test-Path -LiteralPath $operatorKey) { (Get-ItemProperty -LiteralPath $operatorKey).DisplayVersion } else { 'absent' }
    if ($operatorAfter -cne $operatorBefore) { throw "UPDATER CYCLE FAILED: the operator's installed executable changed: $operatorBefore then $operatorAfter" }
    if ($keyAfter -cne $keyBefore) { throw "UPDATER CYCLE FAILED: the operator's uninstall registration changed: $keyBefore then $keyAfter" }
    $productionAfter = @(Get-Process -Name Agent_b -ErrorAction SilentlyContinue | Where-Object {
        try { $_.Path -and -not $_.Path.StartsWith($root, [StringComparison]::OrdinalIgnoreCase) } catch { $false }
    } | ForEach-Object { $_.Id }) -join ','
    if ($productionBefore -cne $productionAfter) { throw "UPDATER CYCLE FAILED: processes outside the suite root changed: '$productionBefore' then '$productionAfter'" }

    # Item 2ll (c): a disposable instance's update writes nothing into the
    # operator's LocalAppData. rel-1.14.0/W8 had to copy three such files out and
    # clear them by hand; this is the assertion that makes that impossible to
    # miss again. Anything found is copied to the evidence directory and cleared
    # before the gate fails, so a failure does not also leave a mess behind.
    $operatorDataAfter = @(Get-ChildItem -LiteralPath $operatorData -Recurse -File -Force -ErrorAction SilentlyContinue |
        ForEach-Object { $_.FullName }) | Sort-Object
    $leaked = @(Compare-Object -ReferenceObject @($operatorDataBefore) -DifferenceObject @($operatorDataAfter) |
        Where-Object { $_.SideIndicator -eq '=>' } | ForEach-Object { $_.InputObject })
    if ($leaked.Count) {
        if ($EvidenceDirectory) { $null = New-Item -ItemType Directory -Path $EvidenceDirectory -Force }
        foreach ($file in $leaked) {
            if ($EvidenceDirectory) { Copy-Item -LiteralPath $file -Destination $EvidenceDirectory -Force -ErrorAction SilentlyContinue }
            Remove-Item -LiteralPath $file -Force -ErrorAction SilentlyContinue
        }
        throw ("UPDATER CYCLE FAILED: the update wrote into the operator's LocalAppData: " + ($leaked -join '; '))
    }
    Write-Host "PROOF nothing written outside the suite root: the operator's data root is unchanged, $($operatorDataBefore.Count) files before and after"

    # Only now: a launch that reached no decision beneath the suite root and left
    # nothing in the operator's root either is a genuine mystery, and worth
    # saying so. Checked after the side effects, because when a pre-2ll build
    # leaks, WHERE it wrote is the answer and the check above gives it.
    if (-not $decision -and -not $installed) { throw 'UPDATER CYCLE FAILED: the launched setup reached no decision beneath the suite root' }

    # The verified setup is the download-and-verification half, proven on disk: the
    # manager deletes it when the Authenticode check fails, so its presence beneath
    # the instance's own data root is the proof that both halves ran.
    $verified = Join-Path $data "updates\$ToVersion\Agent_b-setup.exe"
    if (-not (Test-Path -LiteralPath $verified -PathType Leaf)) { throw 'UPDATER CYCLE FAILED: no verified setup beneath the instance own data root' }
    if ((Get-FileHash $verified -Algorithm SHA256).Hash.ToLowerInvariant() -cne $setupHash) { throw 'UPDATER CYCLE FAILED: the verified setup is not the one the feed served' }
    Write-Host "PROOF download and verification: $verified matches the served digest beneath the instance's own data root"

    $outcome = 'PASS'
    if ($installed) {
        Write-Host "CYCLE TO: $($installed.tag) $($installed.commit)"
        Write-Host "PROOF disposable updater cycle: $($before.tag) updated itself to $($installed.tag) beneath $root; nothing outside it changed"
    } else {
        # The launch happened and the installer decided. It must have decided about
        # THIS instance's roots, not the operator's: that is 2lh (a).
        #
        # The decision TEXT is not the proof: a refusal can name the disposable path
        # because it found the disposable REGISTRATION, with no root passed at all.
        # The installer's own transcript records the command line it was invoked
        # with, so the proof is a transcript beneath this suite root carrying
        # -ApplicationDirectory <this instance>.
        $transcripts = @(Get-ChildItem (Join-Path $data 'logs') -Filter 'installer-*.log' -File -ErrorAction SilentlyContinue |
            Where-Object { $_.LastWriteTime -ge $launchedAt })
        $named = @($transcripts | Where-Object {
            (Get-Content -Raw -LiteralPath $_.FullName) -match ("(?i)-ApplicationDirectory\s+" + [regex]::Escape($application))
        })
        if (-not $named.Count) {
            $why = if ($preFix) { " -- $($before.tag) predates item 2lh, so its updater passed no roots and the setup resolved the operator's own locations" } else { '' }
            # Say what was actually there: a failure that names only what it
            # wanted sends the reader hunting for a directory that is already gone.
            $seen = @(Get-ChildItem (Join-Path $data 'logs') -Filter 'installer-*.log' -File -ErrorAction SilentlyContinue |
                ForEach-Object { "$($_.Name) @ $($_.LastWriteTime.ToString('o'))" }) -join '; '
            throw ("UPDATER CYCLE FAILED: no transcript beneath the suite root shows the setup being invoked with this instance's application root$why." +
                   " Looked for -ApplicationDirectory $application in transcripts newer than $($launchedAt.ToString('o')); found: $seen. " + $decision)
        }
        Write-Host "PROOF launch target: $($named[0].FullName) records -ApplicationDirectory $application, so the setup the updater launched targeted this instance and not the operator location"
        Write-Host "INSTALLER DECISION: $decision"
        Write-Host 'UNEXERCISED: the install-and-restart half. The installer refuses a non-canonical ApplicationDirectory outside TestMode, and the updater must not be able to pass TestMode, so a disposable instance cannot complete an install beneath the suite root.'
        $outcome = 'PARTIAL'
    }
    Write-Host "UPDATER CYCLE $outcome"
    if ($EvidenceDirectory) {
        $null = New-Item -ItemType Directory -Path $EvidenceDirectory -Force
        @{ outcome = $outcome; from = $before.tag; to = $(if ($installed) { $installed.tag } else { $null }); offered = $ToVersion
           root = $root; setup_sha256 = $setupHash; setup_bytes = $setupBytes; verified_setup = $verified
           installer_decision = $decision; operator_data_files = $operatorDataBefore.Count
           operator_executable_sha256 = $operatorAfter; operator_registration = $keyAfter } |
            ConvertTo-Json -Depth 5 | Set-Content (Join-Path $EvidenceDirectory 'updater-cycle.json') -Encoding utf8
    }
} finally {
    # The suite root is removed below, so anything worth reading afterwards is
    # copied out first -- on a pass and on a failure alike. rel-1.15.0/W6 lost a
    # diagnosis to this twice before the gate started keeping them.
    if ($EvidenceDirectory) {
        $null = New-Item -ItemType Directory -Path $EvidenceDirectory -Force
        Get-ChildItem (Join-Path $data 'logs') -Filter 'installer-*.log' -File -ErrorAction SilentlyContinue |
            ForEach-Object { Copy-Item $_.FullName (Join-Path $EvidenceDirectory "suite-$($_.Name)") -Force -ErrorAction SilentlyContinue }
        $progressFile = Join-Path $data 'install-progress.jsonl'
        if (Test-Path -LiteralPath $progressFile) { Copy-Item $progressFile (Join-Path $EvidenceDirectory 'suite-install-progress.jsonl') -Force -ErrorAction SilentlyContinue }
    }
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
