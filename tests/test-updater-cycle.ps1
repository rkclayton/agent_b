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

# Item 2nf (e): THE GATE RUNS THE INSTALLED BASE'S COMMAND LINE.
#
# This gate proved the cycle from the PREVIOUS release's updater, and passed for
# twelve releases while the version most people are actually on could not update at
# all. v1.24.0's updater sends -WorkspaceDirectory pointing INSIDE the data root -
# the app's own profile scratch folder - and the installer refused it with
# "Application, operator-data, and workspace directories must be three disjoint
# trees" in under a second, three times, with nothing readable on screen.
#
# So the first case is a verbatim replay of that argument list, from a fixture
# captured out of the operator's own transcript, against the candidate setup. It runs
# in TestMode on disposable roots shaped like his, because the disjoint-roots check
# runs in TestMode too - it is the canonical-location checks that do not, and the
# disjoint one is what refused him.
$replayFixture = Join-Path $PSScriptRoot 'fixtures\updater-v1.24.0-command-line.json'
if (-not (Test-Path -LiteralPath $replayFixture -PathType Leaf)) {
    throw "UPDATER REPLAY REFUSED: missing fixture $replayFixture"
}
$replay = Get-Content -LiteralPath $replayFixture -Raw | ConvertFrom-Json
$replayRoot = Join-Path ([IO.Path]::GetTempPath()) ('agentb-v1240-replay-' + [Guid]::NewGuid().ToString('N'))
$replayApplication = Join-Path $replayRoot 'Application\Agent_b'
$replayData = Join-Path $replayRoot 'Data\Agent_b'
# His shape exactly: the workspace the updater sends is inside the data root.
$replayWorkspace = Join-Path $replayData ('profiles\' + $env:USERNAME + '\scratch')
$replayRegistry = 'HKCU:\Software\Agent_b-UpdaterReplayTest-' + [Guid]::NewGuid().ToString('N').Substring(0, 16)
try {
    foreach ($directory in @($replayApplication, $replayData, $replayWorkspace)) {
        $null = New-Item -ItemType Directory -Path $directory -Force
    }
    # A file in the handed workspace, so the assertion can say it was left alone.
    $replayWitness = Join-Path $replayWorkspace 'witness.txt'
    Set-Content -LiteralPath $replayWitness -Value 'the operator work that lives here' -Encoding utf8

    $replayArguments = @('--install', '--quiet', '--install-data', $replayData)
    foreach ($argument in $replay.arguments) {
        # -ProgressFile and -EmbeddedBundle are added by the setup itself, so the
        # replay passes what the UPDATER passes and nothing more.
        if ($argument -eq '-ProgressFile' -or $argument -eq '{progress}' -or $argument -eq '-EmbeddedBundle') { continue }
        $replayArguments += switch ($argument) {
            '{application}' { $replayApplication }
            '{data}' { $replayData }
            '{workspace}' { $replayWorkspace }
            default { $argument }
        }
    }
    # TestMode keeps every root beneath the suite root, so the disposable Start menu
    # and Send-to folder are named here rather than defaulting to the operator's.
    $replayStartMenu = Join-Path $replayRoot 'StartMenu'
    # The canonical workspace the installer falls back to is derived from the
    # operator's LocalAppData, so TestMode is given a disposable one: the fallback then
    # lands inside the suite root instead of beside the operator's own.
    $replayLocalAppData = Join-Path $replayRoot 'LocalAppData'
    $null = New-Item -ItemType Directory -Path $replayLocalAppData -Force
    $replayArguments += @('-StartMenuDirectory', $replayStartMenu, '-SendToDirectory', (Join-Path $replayStartMenu 'SendTo'),
        '-OperatorLocalAppData', $replayLocalAppData,
        '-UninstallRegistryPath', $replayRegistry, '-TestMode')
    Write-Host "UPDATER REPLAY: $ToSetup $($replayArguments -join ' ')"

    $replayProcess = Start-Process -FilePath $ToSetup -ArgumentList $replayArguments -PassThru -WindowStyle Hidden
    if (-not $replayProcess.WaitForExit(300000)) {
        throw 'UPDATER REPLAY FAILED: the setup did not finish within five minutes.'
    }
    $replayExit = $replayProcess.ExitCode
    $replayLogs = @(Get-ChildItem (Join-Path $replayData 'logs') -Filter 'installer-*.log' -File -ErrorAction SilentlyContinue |
        Sort-Object LastWriteTime)
    $replayTranscript = ($replayLogs | ForEach-Object { Get-Content -LiteralPath $_.FullName -Raw }) -join "`n"
    if ($replayExit -ne 0) {
        $reason = @($replayTranscript -split "`n" | Where-Object { $_ -match 'INSTALLATION FAILED:' }) -join ' | '
        throw "UPDATER REPLAY FAILED: v1.24.0's command line exited $replayExit. $reason"
    }
    # (a): the workspace it could not accept was ignored, by name, and said so.
    if ($replayTranscript -notmatch 'workspace argument ignored') {
        throw 'UPDATER REPLAY FAILED: the transcript does not record that the workspace argument was ignored.'
    }
    if ($replayTranscript -notmatch [regex]::Escape($replayWorkspace)) {
        throw 'UPDATER REPLAY FAILED: the ignored-workspace line does not name the path that was sent.'
    }
    # The app is on the new version.
    $replayExe = Join-Path $replayApplication 'Agent_b.exe'
    if (-not (Test-Path -LiteralPath $replayExe -PathType Leaf)) {
        throw "UPDATER REPLAY FAILED: no installed executable at $replayExe"
    }
    $replayIdentity = & $replayExe -version 2>$null | Select-Object -First 1
    if ($replayIdentity -notmatch [regex]::Escape($ToVersion)) {
        throw "UPDATER REPLAY FAILED: the installed app reports $replayIdentity, not $ToVersion."
    }
    # And the operator's own workspace is untouched: the installer ignored the
    # argument, it did not reach into the folder and reorganise it.
    if (-not (Test-Path -LiteralPath $replayWitness -PathType Leaf)) {
        throw 'UPDATER REPLAY FAILED: the workspace the updater sent was modified.'
    }
    Write-Host "PASS updater replay: v1.24.0's exact command line installs $ToVersion, the workspace argument is ignored by name, and the folder it named is untouched"

    # Item 2nh (b), (c) and (d): THE SEQUENCE THE INSTALLER WROTE, AND THE OUTCOME THE
    # INSTANCE THAT CAME BACK CAN STATE.
    #
    # This is the one place in the suite where a REAL install of the candidate has
    # just run to completion, so it is the only place the phases can be read from the
    # installer's own file rather than from a fixture. The operator's complaint was
    # not that the update failed - it succeeded - but that nothing showed it
    # happening and the instance that came back said nothing about it.
    $replayProgress = Join-Path $replayData 'install-progress.jsonl'
    if (-not (Test-Path -LiteralPath $replayProgress -PathType Leaf)) {
        throw "UPDATER SEQUENCE FAILED: the install wrote no progress file at $replayProgress"
    }
    $replayPhases = @(Get-Content -LiteralPath $replayProgress | Where-Object { $_.Trim() } | ForEach-Object { $_ | ConvertFrom-Json })
    $phaseNames = @($replayPhases | ForEach-Object { $_.phase })
    Write-Host ("UPDATER SEQUENCE: " + ($phaseNames -join ' -> '))
    foreach ($expected in @('starting', 'preflight', 'copying the application', 'finished')) {
        if ($phaseNames -notcontains $expected) {
            throw "UPDATER SEQUENCE FAILED: the phase '$expected' never appeared. Saw: $($phaseNames -join ', ')"
        }
    }
    # (c): the LAST line is the finish. His successful update ended with a migration
    # warning written as a second finish, and that is the line the page read as the
    # result.
    $replayLast = $replayPhases[-1]
    if (-not ($replayLast.done -eq $true -and $replayLast.ok -eq $true -and $replayLast.text -match 'is installed')) {
        throw "UPDATER SEQUENCE FAILED: the last progress line is not the finish: $($replayLast | ConvertTo-Json -Compress)"
    }
    # And the instance that came back states the outcome. Its own data root carries
    # the file the installer wrote, so it can answer without being told.
    Get-Process -Name Agent_b -ErrorAction SilentlyContinue | Where-Object {
        try { $_.Path -and $_.Path.StartsWith($replayRoot, [StringComparison]::OrdinalIgnoreCase) } catch { $false }
    } | ForEach-Object { Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue }
    Start-Sleep -Seconds 2
    $probe = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $probe.Start(); $replayPort = $probe.LocalEndpoint.Port; $probe.Stop()
    if ($replayPort -eq 8790) { throw 'UPDATER SEQUENCE REFUSED: the probe picked the production port' }
    $replayConfigPath = Join-Path $replayData 'harness.json'
    $replayConfig = Get-Content -Raw -LiteralPath $replayConfigPath | ConvertFrom-Json
    $replayConfig.listen = "127.0.0.1:$replayPort"
    # The reopened page must not need a network to say what the last update did.
    $replayConfig.updates.auto_check = $false
    $replayConfig | ConvertTo-Json -Depth 20 | Set-Content -LiteralPath $replayConfigPath -Encoding utf8
    $reopened = Start-Process -FilePath $replayExe -PassThru -WindowStyle Hidden `
        -ArgumentList @('-config', $replayConfigPath, '-app-root', $replayApplication, '-data-root', $replayData)
    try {
        $deadline = (Get-Date).AddSeconds(120); $updateState = $null
        while ((Get-Date) -lt $deadline) {
            try {
                $updateState = (Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$replayPort/api/update" -TimeoutSec 5).Content | ConvertFrom-Json
                break
            } catch { Start-Sleep -Milliseconds 400 }
        }
        if (-not $updateState) { throw "UPDATER SEQUENCE FAILED: the reopened instance never answered on $replayPort" }
        Write-Host ("UPDATER OUTCOME: " + ($updateState.outcome | ConvertTo-Json -Compress))
        if (-not $updateState.outcome) {
            throw 'UPDATER SEQUENCE FAILED: the instance that came back after the install states no outcome, which is the silence 2nh exists to end'
        }
        if ($updateState.outcome.ok -ne $true) {
            throw "UPDATER SEQUENCE FAILED: a completed install is reported as a failure: $($updateState.outcome | ConvertTo-Json -Compress)"
        }
        if ($updateState.outcome.version -ne $ToVersion) {
            throw "UPDATER SEQUENCE FAILED: the outcome names $($updateState.outcome.version), not $ToVersion"
        }
        Write-Host "PROOF the reopened page can say what happened: the About row reads 'updated to $($updateState.outcome.version)' at $($updateState.outcome.at) from the installer's own finish, with $(@($updateState.outcome.warnings).Count) note(s)"
    } finally {
        if ($reopened -and -not $reopened.HasExited) { Stop-Process -Id $reopened.Id -Force -ErrorAction SilentlyContinue }
    }
} finally {
    Get-Process -Name Agent_b -ErrorAction SilentlyContinue | Where-Object {
        try { $_.Path -and $_.Path.StartsWith($replayRoot, [StringComparison]::OrdinalIgnoreCase) } catch { $false }
    } | ForEach-Object { Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue }
    if (Test-Path -LiteralPath $replayRegistry) { Remove-Item -LiteralPath $replayRegistry -Recurse -Force -ErrorAction SilentlyContinue }
    if (Test-Path -LiteralPath $replayRoot) {
        try { Remove-TreeWithinAllowedRoots -Path $replayRoot -AllowedRoots @([IO.Path]::GetTempPath()) -Purpose 'updater replay cleanup' }
        catch { Write-Warning $_.Exception.Message }
    }
}
$root = Join-Path ([IO.Path]::GetTempPath()) ('agentb-updater-cycle-' + [Guid]::NewGuid().ToString('N'))
$application = Join-Path $root 'Application\Agent_b'
$data = Join-Path $root 'Data\Agent_b'
# Item 2mr (d) and (e): THE WORKSPACE THE UPDATER PASSES IS SHAPED LIKE
# PRODUCTION'S, not like a convenient temp folder.
#
# This gate passed through twelve releases while every update from the operator's
# own client failed, for exactly one reason: its workspace was named 'workspace',
# which the installer's leaf-only path guard accepted, and his is the
# profile-scoped scratch root, which it refused. A gate whose fixture is safer
# than production proves nothing about production.
#
# The FROM install is done with a name the PREVIOUS release's installer accepts,
# because the previous release carries the OLD guard and cannot install to a
# profile-scoped root at all — measured: it exits 1 during failed. What matters is
# the workspace the updater then PASSES TO THE CANDIDATE, so the installed
# instance's configured workspace is set to the production shape before Install is
# pressed, below. That is the operator's case exactly: an install whose configured
# workspace is profile-scoped, updating with the new installer.
$workspace = Join-Path $root 'workspace'
$productionShapedWorkspace = Join-Path $data 'profiles\Operator\scratch'
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
    # Item 2mr (d): the configured workspace is what the updater hands the setup, so
    # this is where the operator's shape enters the gate. The directory is created
    # because a missing workspace is a different failure.
    $null = New-Item -ItemType Directory -Path $productionShapedWorkspace -Force
    $config.workspace = $productionShapedWorkspace
    $config | ConvertTo-Json -Depth 20 | Set-Content (Join-Path $data 'harness.json') -Encoding utf8
    Write-Host "CYCLE WORKSPACE: the instance will hand the setup $productionShapedWorkspace, which is the shape the operator's install carries"
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
    # Item 2nh (a) and (d): WALK THE PAGE'S OWN READOUT WHILE IT HAPPENS.
    #
    # Install blocks until the setup is launched, so the states the About row would
    # show - download with bytes of total, verify, install - exist only DURING that
    # call. A second reader samples the same endpoint the page reads, so the
    # sequence is asserted from what a watching operator would actually have seen.
    $walkFile = Join-Path $root 'update-walk.jsonl'
    $walker = [powershell]::Create()
    $null = $walker.AddScript({
        param($uri, $file, $seconds)
        $stop = (Get-Date).AddSeconds($seconds)
        while ((Get-Date) -lt $stop) {
            try { [IO.File]::AppendAllText($file, (Invoke-WebRequest -UseBasicParsing -Uri $uri -TimeoutSec 5).Content + [Environment]::NewLine) } catch { }
            Start-Sleep -Milliseconds 150
        }
    }).AddArgument("http://127.0.0.1:$port/api/update").AddArgument($walkFile).AddArgument(300)
    $null = $walker.BeginInvoke()
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

    # The walk, read back. Every state the page could have shown, in order.
    $walker.Stop()
    $walk = @()
    if (Test-Path -LiteralPath $walkFile -PathType Leaf) {
        $walk = @(Get-Content -LiteralPath $walkFile | Where-Object { $_.Trim() } | ForEach-Object { try { $_ | ConvertFrom-Json } catch { } })
    }
    $walkSteps = @()
    foreach ($state in $walk) {
        if ($state.step -and ($walkSteps.Count -eq 0 -or $walkSteps[-1] -ne $state.step)) { $walkSteps += $state.step }
    }
    Write-Host ("UPDATE WALK: " + (($walkSteps | Select-Object -Unique) -join ' -> ') + " over $($walk.Count) samples")
    # The updater under test is the one in the FROM build, so a build that predates
    # item 2nh publishes no stages at all and this half of the walk cannot apply to
    # it. Said up front, as the 2lh assertion below does, so a failure from that
    # cause reads as the cause. The candidate's own stages are proved in-process by
    # TestTheUpdateIsASequenceOfStages2nh.
    $walkCapable = [version]($before.tag -replace '^v') -ge [version]'1.30.0'
    if (-not $walkCapable) {
        Write-Host "UPDATE WALK NOTE: $($before.tag) predates item 2nh, so its updater publishes no stages; the stage assertions are skipped and this half activates from v1.30.0 onward"
    }
    foreach ($expected in @('downloading', 'verifying', 'installing')) {
        if (-not $walkCapable) { break }
        if ($walkSteps -notcontains $expected) {
            throw "UPDATE WALK FAILED: the stage '$expected' was never shown. Saw: $($walkSteps -join ', ')"
        }
    }
    # Determinate where it can be: the release manifest names the size, so the
    # download reports bytes of total rather than cycling a pattern in place.
    $determinate = @($walk | Where-Object { $_.step -eq 'downloading' -and [long]$_.total -gt 0 -and [long]$_.processed -gt 0 })
    if ($walkCapable -and -not $determinate.Count) {
        throw 'UPDATE WALK FAILED: no download state carried bytes of a total, so the wait element could never have been determinate'
    }
    $largest = if ($determinate.Count) { ($determinate | Measure-Object -Property processed -Maximum).Maximum } else { 0 }
    if ([long]$largest -ne [long]$setupBytes) {
        Write-Host "UPDATE WALK NOTE: the largest reported read was $largest of $setupBytes bytes; the last sample can land before the final chunk"
    }
    foreach ($state in $walk) {
        if ($state.step -and -not $state.line) { throw 'UPDATE WALK FAILED: a stage was published with no line, and a wait element without one is decoration' }
    }
    if ($walkCapable -and $determinate.Count) {
        Write-Host "PROOF the update is a sequence you can watch: $($walkSteps -join ' -> '), determinate at $largest of $setupBytes bytes during the download"
    } else {
        # A PROOF line that proves nothing is worse than no line: this half is the one
        # the FROM build cannot answer, and it says so instead of printing an empty one.
        Write-Host "NOT PROVED HERE: the stage walk saw $($walk.Count) states and no stages, because $($before.tag)'s updater publishes none. The candidate's own stages are proved in-process by TestTheUpdateIsASequenceOfStages2nh."
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
        # Item 2mr (d): THE INSTALLER MUST NOT REFUSE THE WORKSPACE IT WAS GIVEN.
        # This is the step that failed on the operator's machine at 02:01 on
        # 2026-09-27 while this gate was green, and the refusal is the one thing the
        # unexercised install half cannot hide: the installer reaches the path guard
        # before it reaches anything that needs a canonical application root.
        $refusals = @(Get-ChildItem (Join-Path $data 'logs') -Filter 'installer-*.log' -File -ErrorAction SilentlyContinue |
            Where-Object { $_.LastWriteTime -ge $launchedAt } |
            Where-Object { (Get-Content -Raw -LiteralPath $_.FullName) -match 'WorkspaceDirectory must name a dedicated' })
        $progress = Join-Path $data 'install-progress.jsonl'
        $progressRefused = (Test-Path -LiteralPath $progress) -and ((Get-Content -Raw -LiteralPath $progress) -match 'WorkspaceDirectory must name a dedicated')
        if ($refusals.Count -or $progressRefused) {
            throw ("UPDATER CYCLE FAILED: the installer refused the workspace it was given, $workspace. " +
                   "That is item 2mr's defect: every update from the operator's own client failed on this guard and the product said nothing. " + $decision)
        }
        # Name the path the transcript actually records, not the one this script
        # asked for: the updater passes the instance's CONFIGURED workspace, and
        # the proof is worth nothing if it names something else.
        $passed = @($transcripts | ForEach-Object {
            [regex]::Match((Get-Content -Raw -LiteralPath $_.FullName), '-WorkspaceDirectory\s+(\S+)').Groups[1].Value
        } | Where-Object { $_ }) | Select-Object -Last 1
        # Item 2nf (b): AN UPDATER FROM v1.28.0 ONWARDS SENDS NO WORKSPACE AT ALL, so
        # there is none for the transcript to record and none for the installer to
        # refuse. That is the stronger outcome: item 2mr's proof was that a workspace it
        # was GIVEN was accepted, and this is the same guarantee reached by not giving
        # one. The v1.24.0 replay case above still covers the was-given-one half, because
        # an installed updater cannot be changed retroactively.
        if ($passed) {
            if ($passed -notmatch 'profiles') {
                throw "UPDATER CYCLE FAILED: the setup was given $passed, which is not shaped like the operator's profile-scoped scratch root, so this gate proves nothing about his case."
            }
            Write-Host "PROOF workspace accepted: the installer was given $passed and did not refuse it; that shape is what an older install hands the setup, and refusing it is item 2mr's defect"
        } else {
            Write-Host 'PROOF no workspace sent: this updater passes none, so there is nothing for the installer to refuse. Item 2nf (b).'
        }
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
