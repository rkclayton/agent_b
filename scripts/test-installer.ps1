[CmdletBinding()]
param(
    # Locks the workstation and disconnects the console session during the
    # session-lifetime scenario (item 2em). Off by default: it locks the
    # operator's screen.
    [switch]$LockWorkstation
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'removal-guard.ps1')
. (Join-Path $PSScriptRoot 'windows-tools.ps1')
. (Join-Path $PSScriptRoot 'agentb-stop.ps1')
$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('Agent_b-installer-test-' + [Guid]::NewGuid().ToString('N'))
# Item 2gd: set at the end of the scenario block; the cleanup below keeps the
# root when it is still false, so a failing run can be read afterwards.
$scenariosPassed = $false
$testApplication = Join-Path $testRoot 'Application\Agent_b'
$testData = Join-Path $testRoot 'Data\Agent_b'
$testWorkspace = Join-Path $testRoot 'ProgramData\Agent_b\workspace'
$testStart = Join-Path $testRoot 'StartMenu'
$testRegistry = 'HKCU:\Software\Agent_b-Installer-Test-' + [Guid]::NewGuid().ToString('N')
$installer = Join-Path $PSScriptRoot 'install-Agent_b.ps1'
$uninstaller = Join-Path $PSScriptRoot 'uninstall-Agent_b.ps1'
$installerWrapper = Join-Path (Split-Path -Parent $PSScriptRoot) 'install-Agent_b.cmd'

function Assert-TemporaryTestPath {
    param([string]$Path)
    $full = [IO.Path]::GetFullPath($Path)
    $temp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
    if (-not $full.StartsWith($temp, [StringComparison]::OrdinalIgnoreCase) -or
        -not (Split-Path -Leaf $full).StartsWith('Agent_b-installer-test-', [StringComparison]::Ordinal)) {
        throw "Refusing to clean unexpected test path: $full"
    }
}

# Item 2gu (v1.2.5): production listens on 8790, and a disposable root must
# never wear it. The operating system hands out a free port; this refuses the
# one port that is not ours to take even if it were free at that instant.
$script:ProductionPort = 8790

function Get-FreeTcpPort {
    for ($attempt = 0; $attempt -lt 20; $attempt++) {
        $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
        try {
            $listener.Start()
            $port = ([Net.IPEndPoint]$listener.LocalEndpoint).Port
        } finally { $listener.Stop() }
        if ($port -ne $script:ProductionPort) { return $port }
    }
    throw "could not get a free port that is not production's $script:ProductionPort."
}

# Assert-DisposableListen is the preflight every disposable root passes before a
# launch: a configuration that still names production's port fails HERE, with
# the reason, rather than by colliding with the operator's running Agent_b.
function Assert-DisposableListen {
    param([string]$Listen, [string]$Where)
    if ([string]::IsNullOrWhiteSpace($Listen)) { throw "$Where has no listen address." }
    $port = ($Listen -split ':')[-1]
    if ($port -eq [string]$script:ProductionPort) {
        throw "$Where names production's port $script:ProductionPort; a disposable root must listen elsewhere."
    }
    return $port
}

function Get-AgentBProcessesAtPath {
    param([string]$Executable)
    return @(Get-Process -Name 'Agent_b' -ErrorAction SilentlyContinue | Where-Object {
        try { [IO.Path]::GetFullPath($_.Path).Equals([IO.Path]::GetFullPath($Executable), [StringComparison]::OrdinalIgnoreCase) } catch { $false }
    })
}

function Get-FilePrefixHash {
    param([string]$Path, [long]$Length)
    $stream = [IO.File]::Open($Path, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::ReadWrite)
    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        $buffer = New-Object byte[] 65536
        $remaining = $Length
        while ($remaining -gt 0) {
            $read = $stream.Read($buffer, 0, [Math]::Min($buffer.Length, $remaining))
            if ($read -le 0) { throw "File became shorter while hashing its prefix: $Path" }
            $null = $sha.TransformBlock($buffer, 0, $read, $buffer, 0)
            $remaining -= $read
        }
        $null = $sha.TransformFinalBlock([byte[]]::new(0), 0, 0)
        return ([BitConverter]::ToString($sha.Hash) -replace '-', '')
    } finally {
        $sha.Dispose()
        $stream.Dispose()
    }
}

function Get-StableConfigFingerprint {
    param([string]$Path)
    $value = Get-Content -Raw -LiteralPath $Path | ConvertFrom-Json
    foreach ($server in @($value.servers)) {
        $server.PSObject.Properties.Remove('capabilities')
    }
    return ($value | ConvertTo-Json -Depth 100 -Compress)
}

function Get-RootFingerprint {
    param([string[]]$Roots)
    return ($Roots | ForEach-Object {
        $root = [IO.Path]::GetFullPath($_)
        [ordered]@{
            root = $root
            exists = Test-Path -LiteralPath $root
            directories = @($(if (Test-Path -LiteralPath $root -PathType Container) {
                Get-ChildItem -LiteralPath $root -Recurse -Directory | ForEach-Object { $_.FullName.Substring($root.Length).TrimStart('\') } | Sort-Object
            }))
            files = @($(if (Test-Path -LiteralPath $root -PathType Container) {
                Get-ChildItem -LiteralPath $root -Recurse -File | ForEach-Object {
                    [ordered]@{ path = $_.FullName.Substring($root.Length).TrimStart('\'); length = $_.Length; sha256 = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash }
                } | Sort-Object { $_['path'] }
            }))
        }
    } | ConvertTo-Json -Depth 6 -Compress)
}

function Copy-TrackedTree {
    param([string]$Source, [string]$Destination)
    $git = Get-Command git.exe -ErrorAction SilentlyContinue
    if (-not $git) { throw 'git.exe is required to copy the tracked tree.' }
    # Item 2gu (v1.2.5): the inventory is the tracked files AND the untracked,
    # non-ignored ones. A new script is a file this suite should see before it
    # is staged, not after: v1.1.2, v1.1.3 and v1.2.0 each shipped a scenario
    # that the clean-archive case could not copy because it was untracked, and
    # the suite said nothing until the copy was already wrong.
    $tracked = @(& $git.Source -C $Source ls-files)
    if ($LASTEXITCODE -ne 0 -or -not $tracked.Count) { throw "git ls-files produced no tracked files." }
    $untracked = @(& $git.Source -C $Source ls-files --others --exclude-standard)
    $tracked = @($tracked) + @($untracked)
    foreach ($relative in $tracked) {
        $from = Join-Path $Source ($relative.Replace('/', [IO.Path]::DirectorySeparatorChar))
        if (-not (Test-Path -LiteralPath $from -PathType Leaf)) { continue }
        $to = Join-Path $Destination ($relative.Replace('/', [IO.Path]::DirectorySeparatorChar))
        $null = New-Item -ItemType Directory -Path (Split-Path -Parent $to) -Force
        Copy-Item -LiteralPath $from -Destination $to -Force
    }
}

# Item 2eu: the installer never builds. The release step's build runs once
# here, and every install below takes the exe and manifest it wrote.
$repositoryRoot = Split-Path -Parent $PSScriptRoot
& (Get-WindowsPowerShell) -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'build-candidate.ps1') -SourceDirectory $repositoryRoot
if ($LASTEXITCODE -ne 0) { throw "Candidate build exited $LASTEXITCODE." }
$candidateManifest = Get-Content -Raw -LiteralPath (Join-Path $repositoryRoot 'candidate-final.json') | ConvertFrom-Json

$whatIfTranscript = ''
try {
    $whatIfApplication = Join-Path $testRoot 'WhatIf\Application\Agent_b'
    $whatIfData = Join-Path $testRoot 'WhatIf\Data\Agent_b'
    $whatIfWorkspace = Join-Path $testRoot 'WhatIf\ProgramData\Agent_b\workspace'
    $whatIfRoots = @($whatIfApplication, $whatIfData, $whatIfWorkspace)
    $whatIfBefore = Get-RootFingerprint -Roots $whatIfRoots
    $whatIfOutput = (& (Get-WindowsPowerShell) -NoLogo -NoProfile -File $installer -SourceDirectory (Split-Path -Parent $PSScriptRoot) -ApplicationDirectory $whatIfApplication -DataDirectory $whatIfData -WorkspaceDirectory $whatIfWorkspace -StartMenuDirectory (Join-Path $testRoot 'WhatIf\StartMenu') -UninstallRegistryPath ($testRegistry + '-WhatIf') -TestMode -WhatIf | Out-String)
    if ($LASTEXITCODE -ne 0) { throw "WhatIf install exited $LASTEXITCODE.`n$whatIfOutput" }
    $whatIfAfter = Get-RootFingerprint -Roots $whatIfRoots
    if ($whatIfAfter -cne $whatIfBefore) { throw "WhatIf changed a target root.`nBEFORE $whatIfBefore`nAFTER $whatIfAfter" }
    $whatIfTranscript = if ($whatIfOutput -match '(?m)^Transcript: (.+)$') { $Matches[1].Trim() } else { '' }
    $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\')
    if (-not $whatIfTranscript -or -not (Test-Path -LiteralPath $whatIfTranscript -PathType Leaf) -or
        -not ([IO.Path]::GetFullPath($whatIfTranscript).StartsWith($tempRoot + '\', [StringComparison]::OrdinalIgnoreCase)) -or
        (Split-Path -Leaf $whatIfTranscript) -notlike 'Agent_b-whatif-installer-*.log') {
        throw "WhatIf transcript was not isolated in the caller's temporary directory.`n$whatIfOutput"
    }

    & (Get-WindowsPowerShell) -NoLogo -NoProfile -File $installer -ApplicationDirectory $testApplication -DataDirectory $testData -WorkspaceDirectory $testWorkspace -StartMenuDirectory $testStart -UninstallRegistryPath $testRegistry -TestMode
    if ($LASTEXITCODE -ne 0) { throw "First install exited $LASTEXITCODE." }
    $installedSha = (Get-FileHash -LiteralPath (Join-Path $testApplication 'Agent_b.exe') -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($installedSha -ne $candidateManifest.exe_sha256) { throw "First install did not install the manifest's exe: $installedSha, manifest $($candidateManifest.exe_sha256)." }
    Write-Host "PROOF candidate identity: installed Agent_b.exe sha256 $installedSha equals candidate-final.json"

    $configPath = Join-Path $testData 'harness.json'
    $installedConfig = Get-Content -Raw -LiteralPath $configPath | ConvertFrom-Json
    if (-not ([string]$installedConfig.workspace).Equals((Join-Path $testData 'scratch'), [StringComparison]::OrdinalIgnoreCase) -or
        -not ([string]$installedConfig.log_dir).Equals((Join-Path $testData 'logs'), [StringComparison]::OrdinalIgnoreCase) -or
        -not ([string]$installedConfig.memory.dir).Equals((Join-Path $testData 'memory'), [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Installed configuration does not use data-root scratch, logs, and memory.'
    }
	if (Test-Path -LiteralPath $testWorkspace) { throw 'Fresh install created the removed legacy workspace.' }
    $testPort = Get-FreeTcpPort
    $installedConfig.listen = "127.0.0.1:$testPort"
    $null = Assert-DisposableListen -Listen $installedConfig.listen -Where 'the installed disposable configuration' 
    $installedConfig.operator_files.log_retention_days = 1
    [IO.File]::WriteAllText($configPath, ($installedConfig | ConvertTo-Json -Depth 100) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
    $expiredWorkingLog = Join-Path $testData 'logs\retention-expired-working.jsonl'
    $expiredEvidenceLog = Join-Path $testData 'logs\evidence\retention-expired-evidence.jsonl'
    $retainedChatLog = Join-Path $testData 'chats\retention-retained-chat.jsonl'
    foreach ($path in @($expiredWorkingLog, $expiredEvidenceLog, $retainedChatLog)) {
        [IO.Directory]::CreateDirectory((Split-Path -Parent $path)) | Out-Null
    }
    [IO.File]::WriteAllText($expiredWorkingLog, "{}`n", [Text.UTF8Encoding]::new($false))
    [IO.File]::WriteAllText($expiredEvidenceLog, "{}`n", [Text.UTF8Encoding]::new($false))
    $retainedSeed = [ordered]@{
        seq = 1; ts = '2026-09-01T00:00:00.000Z'; session_id = 'retention-proof'; run_id = ''; type = 'session.created'
        data = @{ session = [ordered]@{ id = 'retention-proof'; label = 'Retention proof'; agent_id = 'api'; role = 'b'; server_id = 'setup-api'; agent_name = 'API'; main_profile = 'API'; b_profile = 'API'; created_at = '2026-09-01T00:00:00Z'; closed = $true; workspace = $testWorkspace; run = @{ status = 'idle'; max_turns = 10000 }; tools = @(); messages = @(); runnable = $true } }
    }
    [IO.File]::WriteAllText($retainedChatLog, ($retainedSeed | ConvertTo-Json -Depth 20 -Compress) + "`n", [Text.UTF8Encoding]::new($false))
    $expiredAt = [DateTime]::UtcNow.AddDays(-2)
    foreach ($path in @($expiredWorkingLog, $expiredEvidenceLog, $retainedChatLog)) { [IO.File]::SetLastWriteTimeUtc($path, $expiredAt) }
    $node = (Get-Command node.exe -ErrorAction Stop).Source
    & $node (Join-Path $PSScriptRoot 'onboarding-acceptance.mjs') --app $testApplication --data $testData --config $configPath --port $testPort
    if ($LASTEXITCODE -ne 0) { throw "First-run onboarding acceptance exited $LASTEXITCODE." }

    foreach ($path in @(
        (Join-Path $testApplication 'Agent_b.exe'),
        (Join-Path $testApplication 'Agent_b.cmd'),
        (Join-Path $testApplication 'LICENSE'),
        (Join-Path $testApplication 'NOTICE'),
        (Join-Path $testApplication 'scripts\launch-Agent_b.ps1'),
        (Join-Path $testApplication 'web\assets\Agent_b.ico'),
        (Join-Path $testStart 'Agent_b.lnk'),
        (Join-Path $testStart 'Startup/Agent_b.lnk')
    )) {
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Missing installed file: $path" }
    }
    $checkOutput = (& (Get-WindowsPowerShell) -NoLogo -NoProfile -File (Join-Path $testApplication 'scripts\launch-Agent_b.ps1') -ApplicationDirectory $testApplication -DataDirectory $testData -ConfigPath $configPath -Check | Out-String)
    if ($LASTEXITCODE -ne 0) { throw "Installed launcher check exited $LASTEXITCODE." }
    if ($checkOutput -notmatch [regex]::Escape("API base: http://127.0.0.1:$testPort/") -or
        $checkOutput -notmatch 'Endpoint ready: False') {
        throw "Installed launcher check did not measure the assigned isolated port.`n$checkOutput"
    }

    $launcherSource = Get-Content -Raw -LiteralPath (Join-Path $testApplication 'scripts\launch-Agent_b.ps1')
    if ($launcherSource -notmatch "'chat'" -or $launcherSource -notmatch 'Show-AgentBWindow -Url \$appUrl' -or $launcherSource -match 'CloseMainWindow') {
        throw 'Installed launcher is not configured to open the Chat-first application view.'
    }
    if ($launcherSource -notmatch '\[switch\]\$Detached' -or
        $launcherSource -notmatch '\[switch\]\$NoPause' -or
        $launcherSource -notmatch '\[switch\]\$Console' -or
        $launcherSource -notmatch "WindowStyle = 'Hidden'" -or
        $launcherSource -notmatch 'launcher-errors\.log') {
        throw 'Installed PowerShell launcher is missing hidden-default/Console-opt-in launch behavior, detached automation, or durable failure logging.'
    }
    $batchLauncherSource = Get-Content -Raw -LiteralPath (Join-Path $testApplication 'Agent_b.cmd')
    if ($batchLauncherSource -match '(?im)^\s*pause\s*$' -or
        $batchLauncherSource -notmatch 'AGENTB_HIDDEN_REENTRY' -or
        $batchLauncherSource -notmatch '"-Console"' -or
        $batchLauncherSource -notmatch 'timeout /t 10 /nobreak' -or
        $batchLauncherSource -notmatch 'AGENT_B_AUTO_CLOSE') {
        throw 'Installed batch launcher does not hide by default with -Console opt-in, or can still wait indefinitely after a failure.'
    }
    $installedInstallerSource = Get-Content -Raw -LiteralPath (Join-Path $testApplication 'scripts\install-Agent_b.ps1')
    if ($installedInstallerSource -notmatch '\$installedAclScript[^\r\n]+apply-acls\.ps1' -or
        $installedInstallerSource -notmatch '& \$installedAclScript[^\r\n]+-Verify' -or
        $installedInstallerSource -notmatch 'PASS: installed root, plans/scratch exceptions, workspace, and exchange-folder ACL policy') {
		throw 'Installed elevated installer does not self-verify the plans/scratch host-policy exceptions.'
    }
    $preStopPolicyPosition = $installedInstallerSource.IndexOf("Write-Host 'PRESTOP POLICY: applying and verifying host policy before stopping Agent_b.'")
    $stopCallPosition = $installedInstallerSource.LastIndexOf('Stop-InstalledProcesses -Processes $installedProcesses')
    if ($preStopPolicyPosition -lt 0 -or $stopCallPosition -lt 0 -or $preStopPolicyPosition -ge $stopCallPosition) {
        throw 'Installed elevated installer does not apply and verify host policy before its process-stop call.'
    }
    $sourceBatchLauncher = Get-Content -Raw -LiteralPath (Join-Path (Split-Path -Parent $PSScriptRoot) 'start-Agent_b.cmd')
    if ($sourceBatchLauncher -notmatch 'AGENTB_HIDDEN_REENTRY' -or
        $sourceBatchLauncher -notmatch '"-Console"' -or
        $sourceBatchLauncher -notmatch 'launcher-errors\.log') {
        throw 'Source launcher does not hide by default with -Console opt-in and durable failure logging.'
    }
    $indexSource = Get-Content -Raw -LiteralPath (Join-Path $testApplication 'web\index.html')
    $chatSource = $indexSource
    $planSource = Get-Content -Raw -LiteralPath (Join-Path $testApplication 'web\plan.html')
    $shellSource = Get-Content -Raw -LiteralPath (Join-Path $testApplication 'web\js\shell.js')
    if ($indexSource -match 'target=' -or $chatSource -match 'target=' -or $planSource -match 'target=') {
        throw 'Installed application still contains second-window navigation.'
    }
    if ($indexSource -match 'class="brand"' -or $chatSource -match 'class="chat-brand"') {
        throw 'Installed application still contains redundant in-page Agent_b branding.'
    }
    foreach ($page in @(
        @{ Source = $indexSource; Name = 'chat' },
        @{ Source = $planSource; Name = 'plan' }
    )) {
        if ($page.Source -notmatch ('id="app-shell"[^>]+data-page="' + $page.Name + '"')) {
            throw "Installed $($page.Name) page is missing the shared shell slot."
        }
    }
    if ($shellSource -notmatch 'root\.append\(left, right\)' -or
        $shellSource -notmatch 'right\.append\(sessionHeading, pages, settings\)' -or
        $shellSource -match 'shell-operator-status' -or
        $shellSource -match 'all:\s*true') {
        throw 'Installed shared shell does not preserve agent-tabs/right-controls ownership.'
    }
    foreach ($required in @('id="chat-attach"', 'id="chat-expand"', 'rows="5"', '/static/assets/Agent_b.ico', '/static/app.webmanifest')) {
        if ($chatSource -notmatch [regex]::Escape($required)) { throw "Installed Chat view is missing: $required" }
    }
    if ($chatSource -match 'chat-clear-conversation|chat-attachment-controls') {
        throw 'Installed Chat view still contains removed Clear or attachment-pane chrome.'
    }
    foreach ($link in @('Plan", "/plan"')) {
        if ($shellSource -notmatch [regex]::Escape($link)) { throw "Installed application is missing page switch $link." }
    }
    foreach ($removed in @('Chat", "/chat"', 'Console", "/"')) {
        if ($shellSource -match [regex]::Escape($removed)) { throw "Installed application retains removed page switch $removed." }
    }
    $chatCSS = Get-Content -Raw -LiteralPath (Join-Path $testApplication 'web\css\chat.css')
    foreach ($required in @('.chat-budget { grid-row: 2; }', '.chat-log { grid-row: 3; }', '.chat-composer { grid-row: 4; }', '#chat-send {')) {
        if ($chatCSS -notmatch [regex]::Escape($required)) { throw "Installed Chat layout is missing: $required" }
    }
    $chatScript = Get-Content -Raw -LiteralPath (Join-Path $testApplication 'web\js\chat.js')
    $settingsScript = [string]::Join("`n", @(
        @('settings.js', 'settings-connections.js', 'settings-general.js', 'settings-context.js', 'settings-run.js', 'settings-delivery.js', 'settings-about.js', 'settings-workspace.js', 'settings-security.js') |
            ForEach-Object { Get-Content -Raw -LiteralPath (Join-Path $testApplication "web\js\$_") }
    ))
    # Item 2gf: the selected Plan link still prevents the default document
    # navigation, and now also returns to the chat instead of doing nothing —
    # the operator had no route back from the page that control opened.
    if ($shellSource -notmatch 'link\.onclick = \(event\) => \{[\s\S]{0,160}event\.preventDefault\(\);[\s\S]{0,80}returnToChat\(\);' -or
        $settingsScript -notmatch 'gear\.addEventListener\("click", \(event\) => \{\s+event\.preventDefault\(\);' -or
        $settingsScript -match 'consoleLaunch') {
        throw 'Installed application does not preserve selected-Plan or Settings in-place navigation.'
    }
    $manifestPath = Join-Path $testApplication 'web\app.webmanifest'
    if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
        throw 'Installed application manifest is missing.'
    }
    $manifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json
    if ($manifest.display_override[0] -ne 'window-controls-overlay') {
        throw 'Installed application does not request native Window Controls Overlay.'
    }
    if ($indexSource -match 'id="state-filters"' -or $indexSource -notmatch '>Activity<' -or $indexSource -notmatch '>Context<' -or $indexSource -notmatch '>History<') {
        throw 'Installed instrument panels are not using the simplified layout.'
    }
    if ($indexSource -match 'id="composer"' -or $indexSource -match 'id="task"' -or $indexSource -match '>Send</button>') {
        throw 'Installed instrument panels still contain the removed task composer.'
    }

    $shortcutPath = Join-Path $testStart 'Agent_b.lnk'
    $shortcut = (New-Object -ComObject WScript.Shell).CreateShortcut($shortcutPath)
    $expectedWScript = Join-Path $env:SystemRoot 'System32\wscript.exe'
    if (-not $shortcut.TargetPath.Equals($expectedWScript, [StringComparison]::OrdinalIgnoreCase) -or
        $shortcut.Arguments -notmatch [regex]::Escape((Join-Path $testApplication 'scripts\launch-hidden.vbs')) -or
        $shortcut.Arguments -notmatch [regex]::Escape((Join-Path $testApplication 'Agent_b.cmd'))) {
        throw 'Shortcut does not enter the installed launcher through the hidden host.'
    }
    if (-not $shortcut.IconLocation.StartsWith((Join-Path $testApplication 'web\assets\Agent_b.ico'), [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Shortcut does not use the Agent_b icon.'
    }
    $wrapperSource = Get-Content -Raw -LiteralPath $installerWrapper
    if ($wrapperSource -match [regex]::Escape("`$launchArgs=@('-Console'") -or
        $wrapperSource -match 'AUTOSTART COMPLETE:[^\r\n]+\r?\ncall :append_install_record\r?\nif not defined AGENT_B_INSTALL_NO_PAUSE pause') {
        throw 'Installer autostart still opts into a visible console or pauses after a successful launch.'
    }

    $registration = Get-ItemProperty -LiteralPath $testRegistry
    if ($registration.DisplayName -ne 'Agent_b' -or
        -not ([string]$registration.InstallLocation).Equals($testApplication, [StringComparison]::OrdinalIgnoreCase) -or
        -not ([string]$registration.DataLocation).Equals($testData, [StringComparison]::OrdinalIgnoreCase) -or
        -not ([string]$registration.WorkspaceLocation).Equals($testWorkspace, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Installed apps registration is incorrect.'
    }

    $portOwner = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, $testPort)
    $portOwner.Start()
    try {
        $savedErrorAction = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        $portFailure = (& (Get-WindowsPowerShell) -NoLogo -NoProfile -File (Join-Path $testApplication 'scripts\launch-Agent_b.ps1') -ApplicationDirectory $testApplication -DataDirectory $testData -ConfigPath $configPath -Detached -NoBrowser -NoPause -StartupTimeoutSeconds 5 2>&1 | Out-String)
        $portFailureExit = $LASTEXITCODE
        $ErrorActionPreference = $savedErrorAction
    } finally {
        $portOwner.Stop()
    }
    $portFailureNormalized = $portFailure -replace '\s+', ' '
    if ($portFailureExit -eq 0 -or $portFailureNormalized -notmatch [regex]::Escape("listen port $testPort is already in use") -or
        $portFailureNormalized -notmatch 'Diagnostics:' -or $portFailureNormalized -notmatch 'startup-' -or $portFailureNormalized -notmatch '\.log') {
        throw "Port-conflict launch did not name its cause and diagnostic files.`n$portFailure"
    }
    if ($launcherSource -notmatch 'Configuration error in' -or $launcherSource -notmatch 'Permission error') {
        throw 'Installed launcher does not classify configuration and permission startup failures.'
    }

    $lifetimeArguments = @{ ApplicationDirectory = $testApplication; DataDirectory = $testData; StartMenuDirectory = $testStart; Port = $testPort }
    if ($LockWorkstation) { $lifetimeArguments.LockWorkstation = $true }
    & (Join-Path $PSScriptRoot 'test-session-lifetime.ps1') @lifetimeArguments

    $credentialPath = Join-Path $testData '.agentb-shell-credential.dpapi'
    [IO.File]::WriteAllBytes($credentialPath, [byte[]](1, 2, 3, 4))
    $credentialHash = (Get-FileHash -LiteralPath $credentialPath -Algorithm SHA256).Hash
	$null = New-Item -ItemType Directory -Path $testWorkspace -Force
    $workspaceMarker = Join-Path $testWorkspace 'preserve-me.txt'
    Set-Content -LiteralPath $workspaceMarker -Value 'preserve'

    $savedErrorAction = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    & (Get-WindowsPowerShell) -NoLogo -NoProfile -File $uninstaller -ApplicationDirectory $testApplication -DataDirectory $testData -WorkspaceDirectory $testWorkspace -StartMenuDirectory $testStart -UninstallRegistryPath $testRegistry -ExpectedOperatorSid 'S-1-5-18' -ExpectedOperatorLocalAppData ([Environment]::GetFolderPath('LocalApplicationData')) -Quiet -PurgeData -TestMode 2>$null
    $wrongPurgeExit = $LASTEXITCODE
    $ErrorActionPreference = $savedErrorAction
    if ($wrongPurgeExit -eq 0 -or -not (Test-Path -LiteralPath $configPath -PathType Leaf) -or -not (Test-Path -LiteralPath $workspaceMarker -PathType Leaf)) {
        throw 'Wrong-operator purge was not refused before changing data.'
    }

    $webDirectory = Join-Path $testApplication 'web'
    $webAcl = Get-Acl -LiteralPath $webDirectory
    $aclMarker = [Security.AccessControl.FileSystemAccessRule]::new(
        [Security.Principal.WindowsIdentity]::GetCurrent().User,
        [Security.AccessControl.FileSystemRights]::ReadPermissions,
        [Security.AccessControl.InheritanceFlags]::None,
        [Security.AccessControl.PropagationFlags]::None,
        [Security.AccessControl.AccessControlType]::Allow
    )
    $null = $webAcl.AddAccessRule($aclMarker)
    Set-Acl -LiteralPath $webDirectory -AclObject $webAcl
    $aclBeforeUpgrade = (Get-Acl -LiteralPath $webDirectory).Sddl
    $staleFile = Join-Path $webDirectory 'stale-upgrade-test.txt'
    Set-Content -LiteralPath $staleFile -Value 'removed by upgrade'
    $staleShortcut = (New-Object -ComObject WScript.Shell).CreateShortcut($shortcutPath)
    $staleShortcut.TargetPath = 'powershell.exe'
    $staleShortcut.Arguments = '-NoExit -File C:\stale\launch-Agent_b.ps1'
    $staleShortcut.Save()

    $installedBinary = Join-Path $testApplication 'Agent_b.exe'
    $beforeStdout = Join-Path $testRoot 'running-before-stdout.log'
    $beforeStderr = Join-Path $testRoot 'running-before-stderr.log'
    $beforeArguments = '-config "' + $configPath + '" -app-root "' + $testApplication + '" -data-root "' + $testData + '"'
    $beforeProcess = Start-Process -FilePath $installedBinary -ArgumentList $beforeArguments -WorkingDirectory $testData -WindowStyle Hidden -RedirectStandardOutput $beforeStdout -RedirectStandardError $beforeStderr -PassThru
    $ready = $false
    $deadline = [DateTime]::UtcNow.AddSeconds(15)
    do {
        Start-Sleep -Milliseconds 200
        try {
            $beforeState = Invoke-RestMethod -Uri "http://127.0.0.1:$testPort/api/state" -TimeoutSec 1
            $ready = $true
        } catch { }
    } while (-not $ready -and -not $beforeProcess.HasExited -and [DateTime]::UtcNow -lt $deadline)
    if (-not $ready) { throw 'Installed Agent_b did not become ready before the running-instance upgrade.' }
    # Item 2fe: on a fresh install the workspace is <data>\scratch, inside the
    # data root by design, and Settings > Security must still load its status.
    $securityServer = [string]@($beforeState.config.servers)[0].id
    try {
        $security = Invoke-WebRequest -UseBasicParsing -Uri ("http://127.0.0.1:$testPort/api/hardening?server_id=" + [Uri]::EscapeDataString($securityServer)) -TimeoutSec 60
    } catch {
        throw ('Fresh install: Settings > Security did not load: ' + $_.Exception.Message + ' ' + $_.ErrorDetails.Message)
    }
    if ($security.StatusCode -ne 200) { throw ('Fresh install: Settings > Security returned ' + $security.StatusCode) }
    Write-Host 'PASS: fresh install, workspace <data>\scratch: Settings > Security loads its status'
    $configFingerprint = Get-StableConfigFingerprint -Path $configPath

    $dataBefore = @(Get-ChildItem -LiteralPath $testData -File -Recurse | Where-Object {
        -not $_.FullName.StartsWith((Join-Path $testData 'logs') + '\', [StringComparison]::OrdinalIgnoreCase) -and
        -not $_.FullName.StartsWith((Join-Path $testData 'stats') + '\', [StringComparison]::OrdinalIgnoreCase) -and
        -not $_.FullName.Equals($configPath, [StringComparison]::OrdinalIgnoreCase) -and
        -not $_.FullName.Equals((Join-Path $testData 'STATE.md'), [StringComparison]::OrdinalIgnoreCase) -and
        -not $_.FullName.Equals((Join-Path $testData 'agent_b-run.json'), [StringComparison]::OrdinalIgnoreCase)
    } | ForEach-Object {
        [pscustomobject]@{ Path = $_.FullName; Length = $_.Length; PrefixSHA256 = Get-FilePrefixHash -Path $_.FullName -Length $_.Length }
    })
    $workspaceBefore = @(Get-ChildItem -LiteralPath $testWorkspace -File -Recurse | ForEach-Object {
        [pscustomobject]@{ Path = $_.FullName; SHA256 = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash }
    })
    $logsBefore = @(Get-ChildItem -LiteralPath (Join-Path $testData 'logs') -File -Recurse | ForEach-Object {
        [pscustomobject]@{ Path = $_.FullName; Length = $_.Length; PrefixSHA256 = Get-FilePrefixHash -Path $_.FullName -Length $_.Length }
    })

    # --- Scenario (2eu): a candidate whose exe is not the one its manifest names
    # is refused before anything is stopped. The stale exe is a real build that
    # reports the early-September commit the operator saw on 2026-09-17.
    $staleTree = Join-Path $testRoot 'stale-candidate'
    Copy-TrackedTree -Source $repositoryRoot -Destination $staleTree
    Copy-Item -LiteralPath (Join-Path $repositoryRoot 'candidate-final.json') -Destination (Join-Path $staleTree 'candidate-final.json')
    $staleCommit = [string](& git -C $repositoryRoot rev-parse 8b03cf0 | Select-Object -First 1)
    $buildGo = @((Join-Path $repositoryRoot '.tools\go\bin\go.exe'), 'C:\Go\bin\go.exe') | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } | Select-Object -First 1
    if (-not $buildGo) { throw 'The stale-exe scenario needs Go to build its stale exe.' }
    Push-Location $repositoryRoot
    try { & $buildGo build -ldflags "-X harness/internal/buildinfo.Tag=$($candidateManifest.tag) -X harness/internal/buildinfo.Commit=$($staleCommit.Trim()) -X harness/internal/buildinfo.Dirty=false" -o (Join-Path $staleTree 'Agent_b.exe') ./cmd/harness } finally { Pop-Location }
    if ($LASTEXITCODE -ne 0) { throw "Stale-exe build exited $LASTEXITCODE." }
    $installedShaBeforeStale = (Get-FileHash -LiteralPath $installedBinary -Algorithm SHA256).Hash
    $staleTranscriptPath = Join-Path $testData 'logs\stale-candidate-transcript.log'
    $savedInstallLog = $env:AGENT_B_INSTALL_LOG
    $savedNoPause = $env:AGENT_B_INSTALL_NO_PAUSE
    $savedNoBrowser = $env:AGENT_B_INSTALL_NO_BROWSER
    $env:AGENT_B_INSTALL_LOG = $staleTranscriptPath
    $env:AGENT_B_INSTALL_NO_PAUSE = '1'
    $env:AGENT_B_INSTALL_NO_BROWSER = '1'
    try {
        $staleOutput = (& (Join-Path $staleTree 'install-Agent_b.cmd') -SourceDirectory $staleTree -ApplicationDirectory $testApplication -DataDirectory $testData -WorkspaceDirectory $testWorkspace -StartMenuDirectory $testStart -UninstallRegistryPath $testRegistry -TestMode 2>&1 | Out-String)
        $staleExit = $LASTEXITCODE
    } finally {
        $env:AGENT_B_INSTALL_LOG = $savedInstallLog
        $env:AGENT_B_INSTALL_NO_PAUSE = $savedNoPause
        $env:AGENT_B_INSTALL_NO_BROWSER = $savedNoBrowser
    }
    $staleNormalized = $staleOutput -replace '\s+', ' '
    $expectedStaleLine = "CANDIDATE REFUSED: expected Agent_b.exe $($candidateManifest.tag) $($candidateManifest.commit) sha256 $($candidateManifest.exe_sha256)"
    if ($staleExit -eq 0 -or $staleNormalized -notmatch [regex]::Escape($expectedStaleLine) -or
        $staleNormalized -notmatch [regex]::Escape("found $($candidateManifest.tag) $($staleCommit.Trim()) sha256 ") -or
        $staleNormalized -notmatch 'Nothing was stopped or changed' -or $staleNormalized -match 'STOPPING:') {
        throw "Stale candidate was not refused before the stop with both identities.`n$staleOutput"
    }
    if ($beforeProcess.HasExited) { throw 'The stale-candidate refusal stopped the running instance.' }
    $staleState = Invoke-RestMethod -Uri "http://127.0.0.1:$testPort/api/state" -TimeoutSec 5
    if ($staleState.build.commit -ne $beforeState.build.commit -or (Get-FileHash -LiteralPath $installedBinary -Algorithm SHA256).Hash -ne $installedShaBeforeStale) {
        throw 'The stale-candidate refusal changed the running version.'
    }
    Write-Host "PROOF stale candidate: $expectedStaleLine ... found $($candidateManifest.tag) $($staleCommit.Trim()); exit $staleExit; PID $($beforeProcess.Id) still serving $($staleState.build.display)"

    $upgradeTranscriptPath = Join-Path $testData 'logs\running-upgrade-transcript.log'
    $savedInstallLog = $env:AGENT_B_INSTALL_LOG
    $savedNoPause = $env:AGENT_B_INSTALL_NO_PAUSE
    $savedNoBrowser = $env:AGENT_B_INSTALL_NO_BROWSER
    $env:AGENT_B_INSTALL_LOG = $upgradeTranscriptPath
    $env:AGENT_B_INSTALL_NO_PAUSE = '1'
    $env:AGENT_B_INSTALL_NO_BROWSER = '1'
    try {
        $upgradeOutput = (& $installerWrapper -SourceDirectory (Split-Path -Parent $PSScriptRoot) -ApplicationDirectory $testApplication -DataDirectory $testData -WorkspaceDirectory $testWorkspace -StartMenuDirectory $testStart -UninstallRegistryPath $testRegistry -TestMode 2>&1 | Out-String)
        $upgradeExit = $LASTEXITCODE
    } finally {
        $env:AGENT_B_INSTALL_LOG = $savedInstallLog
        $env:AGENT_B_INSTALL_NO_PAUSE = $savedNoPause
        $env:AGENT_B_INSTALL_NO_BROWSER = $savedNoBrowser
    }
    if ($upgradeExit -ne 0) { throw "Running-instance wrapper upgrade exited $upgradeExit.`n$upgradeOutput" }
    if ($upgradeOutput -notmatch 'STOPPING: Agent_b PID' -or $upgradeOutput -notmatch 'STOPPED: Agent_b PID' -or $upgradeOutput -notmatch "Agent_b is ready at http://127\.0\.0\.1:$testPort/chat") {
        throw "Running-instance upgrade did not report stop and restart lifecycle.`n$upgradeOutput"
    }
    $strictUtf8 = [Text.UTF8Encoding]::new($false, $true)
    $upgradeTranscript = $strictUtf8.GetString([IO.File]::ReadAllBytes($upgradeTranscriptPath))
    if ($upgradeTranscript.Contains([char]0) -or $upgradeTranscript.Contains([char]0xfffd)) {
        throw 'Running-instance transcript is not one continuous UTF-8 encoding.'
    }
    if ($upgradeTranscript -notmatch 'AUTOSTART COMPLETE: Agent_b started through ' -or
        $upgradeTranscript -notmatch "Agent_b is ready at http://127\.0\.0\.1:$testPort/chat" -or
        $upgradeTranscript -match 'Next: open Agent_b from Start' -or
        $upgradeTranscript -notmatch [regex]::Escape("Transcript: $upgradeTranscriptPath")) {
        throw 'Running-instance transcript is missing its UTF-8 autostart record/path or retains contradictory closing guidance.'
    }
    $repairedShortcut = (New-Object -ComObject WScript.Shell).CreateShortcut($shortcutPath)
    if (-not $repairedShortcut.TargetPath.Equals($expectedWScript, [StringComparison]::OrdinalIgnoreCase) -or
        $repairedShortcut.Arguments -notmatch [regex]::Escape((Join-Path $testApplication 'Agent_b.cmd')) -or
        $repairedShortcut.Arguments -match 'stale') {
        throw 'Upgrade did not repair the deliberately stale Start Menu launch target.'
    }
    $beforeProcess.WaitForExit(15000) | Out-Null
    if (-not $beforeProcess.HasExited) { throw 'The pre-upgrade Agent_b process did not exit.' }
    if ((Get-Content -LiteralPath $beforeStdout -Raw) -notmatch 'stopping on terminated') {
        throw 'The pre-upgrade Agent_b process did not record graceful signal shutdown.'
    }
    $afterProcesses = @(Get-AgentBProcessesAtPath -Executable $installedBinary)
    if ($afterProcesses.Count -ne 1 -or $afterProcesses[0].Id -eq $beforeProcess.Id) {
        throw "Running-instance upgrade did not finish with exactly one restarted instance: $(@($afterProcesses.Id) -join ', ')"
    }
    $afterState = Invoke-RestMethod -Uri "http://127.0.0.1:$testPort/api/state" -TimeoutSec 5
    if ([bool]$afterState.build.dirty -ne [bool]$beforeState.build.dirty -or $afterState.build.commit -ne $beforeState.build.commit) {
        throw 'Restarted Agent_b identity does not match the installed build.'
    }
    if ((Get-StableConfigFingerprint -Path $configPath) -cne $configFingerprint) {
        throw 'Upgrade changed the installed connection configuration.'
    }
    if ((Get-FileHash -LiteralPath $credentialPath -Algorithm SHA256).Hash -ne $credentialHash) {
        throw 'Upgrade changed the installed credential.'
    }
    if ((Get-Acl -LiteralPath $webDirectory).Sddl -ne $aclBeforeUpgrade) {
        throw 'Upgrade replaced the protected web directory or changed its ACL.'
    }
    if (Test-Path -LiteralPath $staleFile) {
        throw 'Upgrade retained a stale program file.'
    }
    foreach ($entry in $dataBefore) {
        if (-not (Test-Path -LiteralPath $entry.Path -PathType Leaf)) { throw "Upgrade removed production data: $($entry.Path)" }
        $afterLength = (Get-Item -LiteralPath $entry.Path).Length
        if ($afterLength -lt $entry.Length -or (Get-FilePrefixHash -Path $entry.Path -Length $entry.Length) -ne $entry.PrefixSHA256) {
            throw "Upgrade changed an existing production-data prefix: $($entry.Path)"
        }
    }
    foreach ($entry in $workspaceBefore) {
        if (-not (Test-Path -LiteralPath $entry.Path -PathType Leaf) -or (Get-FileHash -LiteralPath $entry.Path -Algorithm SHA256).Hash -ne $entry.SHA256) {
            throw "Upgrade changed workspace evidence: $($entry.Path)"
        }
    }
    foreach ($entry in $logsBefore) {
        if (-not (Test-Path -LiteralPath $entry.Path -PathType Leaf)) { throw "Upgrade removed log evidence: $($entry.Path)" }
        $afterLength = (Get-Item -LiteralPath $entry.Path).Length
        if ($afterLength -lt $entry.Length -or (Get-FilePrefixHash -Path $entry.Path -Length $entry.Length) -ne $entry.PrefixSHA256) {
            throw "Upgrade changed an existing log prefix: $($entry.Path)"
        }
    }

    $rollbackSentinel = Join-Path $webDirectory 'rollback-sentinel.txt'
    [IO.File]::WriteAllText($rollbackSentinel, 'restore the previously installed application tree', [Text.UTF8Encoding]::new($false))
    $installedVersionMatch = [regex]::Match((Get-Content -Raw -LiteralPath (Join-Path $testApplication 'scripts\install-Agent_b.ps1')), "(?m)^\`$displayVersion\s*=\s*'([^']+)'")
    if (-not $installedVersionMatch.Success) { throw 'Could not read the installed version for the forced-failure rollback proof.' }
    $expectedRollbackVersion = 'v' + $installedVersionMatch.Groups[1].Value
    $forcedTranscriptPath = Join-Path $testData 'logs\forced-failure-transcript.log'
    $savedInstallLog = $env:AGENT_B_INSTALL_LOG
    $savedNoPause = $env:AGENT_B_INSTALL_NO_PAUSE
    $savedNoBrowser = $env:AGENT_B_INSTALL_NO_BROWSER
    $env:AGENT_B_INSTALL_LOG = $forcedTranscriptPath
    $env:AGENT_B_INSTALL_NO_PAUSE = '1'
    $env:AGENT_B_INSTALL_NO_BROWSER = '1'
    try {
        $forcedOutput = (& $installerWrapper -SourceDirectory (Split-Path -Parent $PSScriptRoot) -ApplicationDirectory $testApplication -DataDirectory $testData -WorkspaceDirectory $testWorkspace -StartMenuDirectory $testStart -UninstallRegistryPath $testRegistry -TestMode -ForcePostStopVerificationFailure 2>&1 | Out-String)
        $forcedExit = $LASTEXITCODE
    } finally {
        $env:AGENT_B_INSTALL_LOG = $savedInstallLog
        $env:AGENT_B_INSTALL_NO_PAUSE = $savedNoPause
        $env:AGENT_B_INSTALL_NO_BROWSER = $savedNoBrowser
    }
    if ($forcedExit -eq 0) { throw "Forced post-stop verification failure unexpectedly succeeded.`n$forcedOutput" }
    $forcedTranscript = $strictUtf8.GetString([IO.File]::ReadAllBytes($forcedTranscriptPath))
    foreach ($requiredLine in @(
        "ROLLBACK: restored $expectedRollbackVersion application files after installation failure.",
        "RESTART VERSION: $expectedRollbackVersion",
        'RESTART REASON: verification failure',
        "RESTARTED: $expectedRollbackVersion after verification failure."
    )) {
        if ($forcedTranscript -notmatch [regex]::Escape($requiredLine)) { throw "Forced-failure transcript is missing: $requiredLine`n$forcedTranscript" }
        Write-Host "PROOF forced-failure transcript: $requiredLine"
    }
    if (-not (Test-Path -LiteralPath $rollbackSentinel -PathType Leaf)) { throw 'Forced-failure rollback did not restore the previous application tree.' }
    $stoppedForFailure = $afterProcesses[0]
    $stoppedForFailure.WaitForExit(15000) | Out-Null
    if (-not $stoppedForFailure.HasExited) { throw 'Forced-failure upgrade did not stop the pre-existing disposable instance.' }
    $deadline = [DateTime]::UtcNow.AddSeconds(20)
    do {
        $afterProcesses = @(Get-AgentBProcessesAtPath -Executable $installedBinary)
        if ($afterProcesses.Count -eq 1 -and $afterProcesses[0].Id -ne $stoppedForFailure.Id) { break }
        Start-Sleep -Milliseconds 200
    } while ([DateTime]::UtcNow -lt $deadline)
    if ($afterProcesses.Count -ne 1 -or $afterProcesses[0].Id -eq $stoppedForFailure.Id) {
        throw "Forced-failure rollback did not restart exactly one previous-version instance: $(@($afterProcesses.Id) -join ', ')"
    }
    $rollbackState = Invoke-RestMethod -Uri "http://127.0.0.1:$testPort/api/state" -TimeoutSec 5
    if ($rollbackState.build.commit -ne $afterState.build.commit -or [bool]$rollbackState.build.dirty -ne [bool]$afterState.build.dirty) {
        throw 'Forced-failure restart identity does not match the previously installed build.'
    }
    $null = Request-AgentbGracefulStop -ProcessId $afterProcesses[0].Id
    $afterProcesses[0].WaitForExit(15000) | Out-Null
    if (-not $afterProcesses[0].HasExited) { throw 'Restarted disposable Agent_b did not exit.' }

    & (Get-WindowsPowerShell) -NoLogo -NoProfile -File $uninstaller -ApplicationDirectory $testApplication -DataDirectory $testData -WorkspaceDirectory $testWorkspace -StartMenuDirectory $testStart -UninstallRegistryPath $testRegistry -ExpectedOperatorSid ([Security.Principal.WindowsIdentity]::GetCurrent().User.Value) -ExpectedOperatorLocalAppData ([Environment]::GetFolderPath('LocalApplicationData')) -Quiet -TestMode
    if ($LASTEXITCODE -ne 0) { throw "Preserving uninstall exited $LASTEXITCODE." }
    if (-not (Test-Path -LiteralPath $configPath -PathType Leaf) -or
        -not (Test-Path -LiteralPath $credentialPath -PathType Leaf) -or
        -not (Test-Path -LiteralPath $workspaceMarker -PathType Leaf) -or
        (Test-Path -LiteralPath (Join-Path $testApplication 'Agent_b.exe')) -or
        (Test-Path -LiteralPath (Join-Path $testStart 'Agent_b.lnk')) -or
        (Test-Path -LiteralPath (Join-Path $testStart 'Startup/Agent_b.lnk')) -or
        (Test-Path -LiteralPath $testRegistry)) {
        throw 'Preserving uninstall did not keep only local data.'
    }

    & (Get-WindowsPowerShell) -NoLogo -NoProfile -File $installer -ApplicationDirectory $testApplication -DataDirectory $testData -WorkspaceDirectory $testWorkspace -StartMenuDirectory $testStart -UninstallRegistryPath $testRegistry -TestMode
    if ($LASTEXITCODE -ne 0) { throw "Reinstall exited $LASTEXITCODE." }
    if ((Get-StableConfigFingerprint -Path $configPath) -cne $configFingerprint) {
        throw 'Reinstall changed preserved connection configuration.'
    }

    & (Get-WindowsPowerShell) -NoLogo -NoProfile -File $uninstaller -ApplicationDirectory $testApplication -DataDirectory $testData -WorkspaceDirectory $testWorkspace -StartMenuDirectory $testStart -UninstallRegistryPath $testRegistry -ExpectedOperatorSid ([Security.Principal.WindowsIdentity]::GetCurrent().User.Value) -ExpectedOperatorLocalAppData ([Environment]::GetFolderPath('LocalApplicationData')) -Quiet -PurgeData -TestMode
    if ($LASTEXITCODE -ne 0) { throw "Purging uninstall exited $LASTEXITCODE." }
    if ((Test-Path -LiteralPath $testApplication) -or
        (Test-Path -LiteralPath $testData) -or
        (Test-Path -LiteralPath $testWorkspace) -or
        (Test-Path -LiteralPath (Join-Path $testStart 'Agent_b.lnk')) -or
        (Test-Path -LiteralPath (Join-Path $testStart 'Startup/Agent_b.lnk')) -or
        (Test-Path -LiteralPath $testRegistry)) {
        throw 'Uninstall left a program, shortcut, or registration artifact.'
    }
    Write-Host 'PASS: fresh install omits legacy workspace; upgrade preservation, preserve-data uninstall, reinstall, and owner-checked purge uninstall'
    # Item 2gd: the disposable root is removed only when the scenarios passed.
    $scenariosPassed = $true
} finally {
    if ($whatIfTranscript -and (Test-Path -LiteralPath $whatIfTranscript -PathType Leaf)) {
        $resolvedTranscript = [IO.Path]::GetFullPath($whatIfTranscript)
        $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\')
        if ((Split-Path -Parent $resolvedTranscript).Equals($tempRoot, [StringComparison]::OrdinalIgnoreCase) -and
            (Split-Path -Leaf $resolvedTranscript) -like 'Agent_b-whatif-installer-*.log') {
            $removalPath = Assert-RemovalWithinAllowedRoots -Path $resolvedTranscript -AllowedRoots @($tempRoot) -Purpose 'WhatIf transcript cleanup'
            Remove-Item -LiteralPath $removalPath -Force
        }
    }
    if (Test-Path -LiteralPath $testRegistry) { Remove-Item -LiteralPath $testRegistry -Recurse -Force }
    $disposableExecutable = Join-Path $testApplication 'Agent_b.exe'
    foreach ($process in @(Get-AgentBProcessesAtPath -Executable $disposableExecutable)) {
        & (Join-Path $env:SystemRoot 'System32\taskkill.exe') /PID $process.Id /T /F | Out-Null
        $process.WaitForExit(15000) | Out-Null
        if (-not $process.HasExited) { throw "Disposable Agent_b PID $($process.Id) did not exit during cleanup." }
    }
    # Item 2gd: a passing run leaves no root behind; a failing one keeps its
    # root and says where it is, because that root is the failure's evidence.
    if (Test-Path -LiteralPath $testRoot) {
        if ($scenariosPassed) {
            Assert-TemporaryTestPath $testRoot
            Remove-TreeWithinAllowedRoots -Path $testRoot -AllowedRoots @([IO.Path]::GetTempPath()) -Purpose 'installer-suite disposable-root cleanup'
        } else {
            Write-Host "KEPT for evidence: $testRoot"
        }
    }
}

# --- Scenario: the first clone. A plain extracted tree with no .git, under
# Windows PowerShell 5.1, is what anyone who clones rkclayton/agent_b has. The
# release tooling used to assume a checkout and PowerShell 7, so this path was
# never proved and broke without anyone noticing.
$cloneRoot = Join-Path ([IO.Path]::GetTempPath()) ('Agent_b-installer-test-clone-' + [Guid]::NewGuid().ToString('N'))
try {
    $sourceRoot = Split-Path -Parent $PSScriptRoot
    $null = New-Item -ItemType Directory -Path $cloneRoot -Force
    $git = Get-Command git.exe -ErrorAction SilentlyContinue
    if (-not $git) { throw 'git.exe is required to produce the clean archive for the first-clone scenario.' }
    $tree = Join-Path $cloneRoot 'tree'
    $null = New-Item -ItemType Directory -Path $tree -Force
    # Copy the TRACKED WORKING TREE rather than archiving HEAD: the point is to
    # catch a regression in the change being made, not in the last commit.
    $tracked = @(& $git.Source -C $sourceRoot ls-files)
    if ($LASTEXITCODE -ne 0 -or -not $tracked.Count) { throw "git ls-files produced no tracked files." }
    # Item 2gu: untracked, non-ignored files too, for the same reason.
    $tracked = @($tracked) + @(& $git.Source -C $sourceRoot ls-files --others --exclude-standard)
    foreach ($relative in $tracked) {
        $from = Join-Path $sourceRoot ($relative.Replace('/', [IO.Path]::DirectorySeparatorChar))
        if (-not (Test-Path -LiteralPath $from -PathType Leaf)) { continue }
        $to = Join-Path $tree ($relative.Replace('/', [IO.Path]::DirectorySeparatorChar))
        $null = New-Item -ItemType Directory -Path (Split-Path -Parent $to) -Force
        Copy-Item -LiteralPath $from -Destination $to -Force
    }
    if (Test-Path -LiteralPath (Join-Path $tree '.git')) { throw 'the extracted archive must not be a git checkout.' }

    # The release step builds the candidate (item 2eu); the installer never does.
    # A tree with no .git states its commit explicitly.
    $treeCommit = [string](& $git.Source -C $sourceRoot rev-parse HEAD | Select-Object -First 1)
    & (Get-WindowsPowerShell) -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'build-candidate.ps1') -SourceDirectory $tree -Commit $treeCommit.Trim() -Dirty true
    if ($LASTEXITCODE -ne 0) { throw "clean-archive candidate build exited $LASTEXITCODE." }
    $treeManifest = Get-Content -Raw -LiteralPath (Join-Path $tree 'candidate-final.json') | ConvertFrom-Json

    # The release step refuses an exe that does not report the tag being released.
    $savedErrorAction = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    $mismatchOutput = (& (Get-WindowsPowerShell) -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'build-candidate.ps1') -SourceDirectory $tree -Commit $treeCommit.Trim() -Dirty true -ExpectedTag 'v9.9.9' 2>&1 | Out-String)
    $mismatchExit = $LASTEXITCODE
    $ErrorActionPreference = $savedErrorAction
    if ($mismatchExit -eq 0 -or $mismatchOutput -notmatch 'CANDIDATE BUILD REFUSED' -or $mismatchOutput -notmatch 'release is v9\.9\.9' -or
        (Test-Path -LiteralPath (Join-Path $tree 'candidate-final.json'))) {
        throw "The release step did not refuse an exe reporting the wrong tag.`n$mismatchOutput"
    }
    Write-Host "PROOF release step: an exe reporting $($treeManifest.tag) is refused for release v9.9.9 and no manifest is left"
    & (Get-WindowsPowerShell) -NoLogo -NoProfile -File (Join-Path $PSScriptRoot 'build-candidate.ps1') -SourceDirectory $tree -Commit $treeCommit.Trim() -Dirty true
    if ($LASTEXITCODE -ne 0) { throw "clean-archive candidate rebuild exited $LASTEXITCODE." }
    $treeManifest = Get-Content -Raw -LiteralPath (Join-Path $tree 'candidate-final.json') | ConvertFrom-Json

    $cloneApplication = Join-Path $cloneRoot 'Application\Agent_b'
    $cloneData = Join-Path $cloneRoot 'Data\Agent_b'
    $cloneWorkspace = Join-Path $cloneRoot 'ProgramData\Agent_b\workspace'
    $cloneRegistry = $testRegistry + '-Clone'
    # -File under powershell.exe IS Windows PowerShell 5.1 on this host, which is
    # the shell the operator's installer actually runs in.
    # Go absent: no .tools in the tree and no go.exe on PATH. The installer has
    # nothing to build with and must not need anything.
    if (Test-Path -LiteralPath (Join-Path $tree '.tools')) { throw 'the extracted archive must not carry a toolchain.' }
    $savedPath = $env:PATH
    $env:PATH = (($env:PATH -split ';') | Where-Object { $_ -and -not (Test-Path -LiteralPath (Join-Path $_ 'go.exe') -PathType Leaf) }) -join ';'
    try {
        if (Get-Command go.exe -ErrorAction SilentlyContinue) { throw 'go.exe is still reachable for the Go-absent install.' }
        $cloneOutput = (& (Get-WindowsPowerShell) -NoLogo -NoProfile -File (Join-Path $tree 'scripts\install-Agent_b.ps1') -SourceDirectory $tree -ApplicationDirectory $cloneApplication -DataDirectory $cloneData -WorkspaceDirectory $cloneWorkspace -StartMenuDirectory (Join-Path $cloneRoot 'StartMenu') -UninstallRegistryPath $cloneRegistry -TestMode | Out-String)
        $cloneExit = $LASTEXITCODE
    } finally { $env:PATH = $savedPath }
    if ($cloneExit -ne 0) { throw "clean-archive install under Windows PowerShell 5.1 exited $cloneExit.`n$cloneOutput" }
    if ($cloneOutput -match 'BUILD' -or $cloneOutput -notmatch [regex]::Escape("CANDIDATE: Agent_b.exe $($treeManifest.tag) $($treeManifest.display) sha256 $($treeManifest.exe_sha256) matches candidate-final.json")) {
        throw "Go-absent install did not take the manifest's exe exactly as a Go-present install does.`n$cloneOutput"
    }
    $cloneSha = (Get-FileHash -LiteralPath (Join-Path $cloneApplication 'Agent_b.exe') -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($cloneSha -ne $treeManifest.exe_sha256) { throw "Go-absent install installed $cloneSha, manifest $($treeManifest.exe_sha256)." }
    Write-Host "PROOF Go absent: installed sha256 $cloneSha equals candidate-final.json, no BUILD line, as with Go present"
    if ($cloneOutput -match 'not a git repository') { throw "clean-archive install hit the git-checkout assumption.`n$cloneOutput" }
    if ($cloneOutput -match 'NativeCommandError') { throw "clean-archive install raised NativeCommandError under 5.1.`n$cloneOutput" }
    if (-not (Test-Path -LiteralPath (Join-Path $cloneApplication 'Agent_b.exe'))) { throw 'clean-archive install produced no application binary.' }
    Write-Host 'PASS: clean archive with no .git installs under Windows PowerShell 5.1'

    # Item 2gl: a disposable install must never touch the machine-wide Edge
    # policy. It is not elevated, the policy is real for every user on the box,
    # and a test root that outlived itself in the registry would be a mess the
    # suite made and could not clean up. It prints what it would write instead,
    # which is what this asserts on.
    if ($cloneOutput -notmatch 'TESTMODE: the Edge app-window policy is not written') {
        throw "a disposable install did not say it was leaving the Edge policy alone.`n$cloneOutput"
    }
    if ($cloneOutput -match 'REGISTERED: Edge will install the app window') {
        throw "a disposable install wrote the machine-wide Edge policy.`n$cloneOutput"
    }
    $wouldWrite = [regex]::Match($cloneOutput, '"url":"(http://127\.0\.0\.1:\d+/chat)"')
    if (-not $wouldWrite.Success -or $cloneOutput -notmatch '"default_launch_container":"window"') {
        throw "a disposable install did not print the loopback entry it would have written.`n$cloneOutput"
    }
    # The disposable install runs on a disposable port, so its own url is the
    # exact string that must NOT appear in the machine-wide policy.
    if (Test-Path -LiteralPath 'HKLM:\SOFTWARE\Policies\Microsoft\Edge\WebAppInstallForceList') {
        $forced = Get-Item -LiteralPath 'HKLM:\SOFTWARE\Policies\Microsoft\Edge\WebAppInstallForceList'
        foreach ($name in $forced.GetValueNames()) {
            if ([string]$forced.GetValue($name) -match [regex]::Escape($wouldWrite.Groups[1].Value)) {
                throw "a disposable install left an Edge policy entry behind at value $name."
            }
        }
    }
    Write-Host 'PASS: a disposable install prints the Edge app-window entry and writes no machine-wide policy'

    & (Get-WindowsPowerShell) -NoLogo -NoProfile -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'test-pwa-policy.ps1')
    if ($LASTEXITCODE -ne 0) { throw "The Edge app-window policy suite exited $LASTEXITCODE." }
} finally {
    if (Test-Path -LiteralPath $cloneRoot) {
        try { Remove-TreeWithinAllowedRoots -Path $cloneRoot -AllowedRoots @([IO.Path]::GetTempPath()) -Purpose 'installer-suite first-clone cleanup' } catch { Write-Warning $_.Exception.Message }
    }
    Remove-Item -LiteralPath ($testRegistry + '-Clone') -Recurse -Force -ErrorAction SilentlyContinue
}

# --- Scenario: the pre-stop gate fails closed on a missing exit code.
# apply-acls used to fall off its success path, leaving $LASTEXITCODE null,
# and $null -ne 0 threw with an empty code in the message. Every install failed.
# The helper is lifted out of the shipped installer and exercised directly, so
# the scenario proves the code that runs rather than a copy of it.
$installerText = Get-Content -Raw -LiteralPath $installer
$helperMatch = [regex]::Match($installerText, '(?s)function Assert-ScriptExitCode \{.*?\r?\n\}')
if (-not $helperMatch.Success) { throw 'the installer no longer defines Assert-ScriptExitCode.' }
$helperFile = Join-Path $testRoot 'exit-gate-probe.ps1'
$null = New-Item -ItemType Directory -Path $testRoot -Force
Set-Content -LiteralPath $helperFile -Value $helperMatch.Value -Encoding UTF8
function Invoke-ExitGateProbe {
    param([string]$Body)
    $script = Join-Path $testRoot ('exit-gate-case-' + [Guid]::NewGuid().ToString('N') + '.ps1')
    Set-Content -LiteralPath $script -Value ((". '" + $helperFile + "'"), 'try {', $Body, "} catch { " + '$_.Exception.Message' + ' }') -Encoding UTF8
    return (& (Get-WindowsPowerShell) -NoLogo -NoProfile -ExecutionPolicy Bypass -File $script 2>&1 | Out-String)
}
try {
    $nullProbe = Invoke-ExitGateProbe -Body ("Assert-ScriptExitCode -Purpose 'probe' -Code " + '$null')
    if ($nullProbe -notmatch 'no exit code') { throw "the gate does not fail closed on a null exit code: $nullProbe" }
    $zeroProbe = Invoke-ExitGateProbe -Body "Assert-ScriptExitCode -Purpose 'probe' -Code 0; 'gate-ok'"
    if ($zeroProbe -notmatch 'gate-ok') { throw "the gate rejects a successful exit code: $zeroProbe" }
    $twoProbe = Invoke-ExitGateProbe -Body "Assert-ScriptExitCode -Purpose 'probe' -Code 2"
    if ($twoProbe -notmatch 'exit code 2') { throw "the gate loses the real exit code: $twoProbe" }
    foreach ($required in @('apply-acls.ps1', 'install-Agent_b.ps1', 'uninstall-Agent_b.ps1', 'apply-firewall-rule.ps1')) {
        $text = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot $required)
        if ($text.TrimEnd() -notmatch 'exit 0$') { throw "$required no longer ends with an explicit exit code." }
    }
} catch {
    # Item 2ft: a failing scenario keeps its root for evidence and says where.
    Write-Host "KEPT for evidence: $testRoot"
    throw
}
Write-Host 'PASS: the pre-stop gate fails closed on a null exit code, keeps a real one, and every invoked script exits explicitly'
# Item 2ft: the probe recreated the disposable root after its cleanup above; a
# passing scenario removes it again (a failing one threw first and keeps it).
Assert-TemporaryTestPath $testRoot
Remove-TreeWithinAllowedRoots -Path $testRoot -AllowedRoots @([IO.Path]::GetTempPath()) -Purpose 'installer-suite exit-gate probe cleanup'

& (Get-WindowsPowerShell) -NoLogo -NoProfile -ExecutionPolicy Bypass -File (Join-Path $PSScriptRoot 'test-chat-acceptance.ps1') -SkipBuild
if ($LASTEXITCODE -ne 0) { throw "Chat acceptance release gate exited $LASTEXITCODE." }
