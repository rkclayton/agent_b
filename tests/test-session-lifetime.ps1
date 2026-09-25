[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$ApplicationDirectory,
    [Parameter(Mandatory = $true)][string]$DataDirectory,
    [Parameter(Mandatory = $true)][string]$StartMenuDirectory,
    [Parameter(Mandatory = $true)][int]$Port,
    [int]$LockedSeconds = 60,
    [switch]$LockWorkstation
)

# Item 2em on a disposable install: production returns at sign-in through the
# installed Startup shortcut, a second sign-in start does not start a second
# server, every exit leaves its reason in the launcher log (a graceful stop as it
# happens, a forced end at the next start), and with -LockWorkstation the server
# keeps answering while the workstation is locked and the console session is
# disconnected. A real logoff or sleep cannot be driven unattended here; the
# logoff path is proved by the harness's WM_ENDSESSION test.

$ErrorActionPreference = 'Stop'
. (Join-Path (Split-Path -Parent $PSScriptRoot) 'scripts\agentb-stop.ps1')
$executable = Join-Path $ApplicationDirectory 'Agent_b.exe'
$launcherLog = Join-Path $DataDirectory 'logs\launcher-errors.log'
$state = "http://127.0.0.1:$Port/chat"
$taskkill = Join-Path $env:SystemRoot 'System32\taskkill.exe'

function Get-Running {
    return @(Get-Process -Name 'Agent_b' -ErrorAction SilentlyContinue | Where-Object {
        try { [IO.Path]::GetFullPath($_.Path).Equals([IO.Path]::GetFullPath($executable), [StringComparison]::OrdinalIgnoreCase) } catch { $false }
    })
}

function Test-Ready {
    try { $null = Invoke-WebRequest -UseBasicParsing -Uri $state -TimeoutSec 3; return $true } catch { return $false }
}

function Wait-Until([scriptblock]$Condition, [int]$Seconds, [string]$What) {
    $deadline = [DateTime]::UtcNow.AddSeconds($Seconds)
    while ([DateTime]::UtcNow -lt $deadline) {
        if (& $Condition) { return }
        Start-Sleep -Milliseconds 250
    }
    throw "Timed out after $Seconds s waiting for $What."
}

function Get-LogLines {
    if (-not (Test-Path -LiteralPath $launcherLog)) { return @() }
    return @(Get-Content -LiteralPath $launcherLog -Encoding UTF8)
}

$startupShortcut = Join-Path $StartMenuDirectory 'Startup\Agent_b.lnk'
if (-not (Test-Path -LiteralPath $startupShortcut -PathType Leaf)) { throw "Installer did not register a sign-in start: $startupShortcut" }
$shortcut = (New-Object -ComObject WScript.Shell).CreateShortcut($startupShortcut)
if (-not $shortcut.TargetPath.Equals((Join-Path $env:SystemRoot 'System32\wscript.exe'), [StringComparison]::OrdinalIgnoreCase) -or
    $shortcut.Arguments -notmatch [regex]::Escape((Join-Path $ApplicationDirectory 'scripts\launch-hidden.vbs')) -or
    $shortcut.Arguments -notmatch [regex]::Escape((Join-Path $ApplicationDirectory 'Agent_b.cmd')) -or
    $shortcut.Arguments -notmatch '-Detached' -or $shortcut.Arguments -notmatch '-NoBrowser' -or
    $shortcut.Arguments -notmatch ('-DataDirectory "' + [regex]::Escape($DataDirectory) + '"')) {
    throw "Sign-in shortcut does not start the installed launcher hidden, detached and without a browser: $($shortcut.TargetPath) $($shortcut.Arguments)"
}
if (Get-Running) { throw 'A disposable Agent_b is already running before the lifetime scenario.' }

function Invoke-SignIn {
    Start-Process -FilePath $shortcut.TargetPath -ArgumentList $shortcut.Arguments -WorkingDirectory $shortcut.WorkingDirectory | Out-Null
}

try {
    # Sign-in start, then a second sign-in start while it runs.
    Invoke-SignIn
    Wait-Until { (Get-Running).Count -eq 1 -and (Test-Ready) } 60 'the sign-in start to become ready'
    $first = (Get-Running)[0]
    Invoke-SignIn
    Start-Sleep -Seconds 8
    $running = Get-Running
    if ($running.Count -ne 1 -or $running[0].Id -ne $first.Id) { throw 'A second sign-in start started another server.' }
    Write-Host "PASS: the sign-in shortcut starts Agent_b hidden and detached (PID $($first.Id)), and a second sign-in start does not start another"
    # Item 2ev: both launch paths say what they did, durably, naming the PID.
    $decisions = Join-Path $DataDirectory 'logs\launcher.log'
    $readyLine = "(PID $($first.Id) answering on port $Port); started as process $($first.Id)."
    $alreadyLine = "Agent_b is already running (PID $($first.Id) answering on port $Port); no new server was started."
    $decisionText = if (Test-Path -LiteralPath $decisions) { Get-Content -Raw -LiteralPath $decisions -Encoding UTF8 } else { '' }
    if (-not $decisionText.Contains($readyLine) -or -not $decisionText.Contains($alreadyLine)) {
        throw "launcher.log does not record one start and one 'already running' naming PID $($first.Id):`n$decisionText"
    }
    Write-Host "PASS: exactly one launch; launcher.log records the start and the second path's '$alreadyLine'"

    if ($LockWorkstation) {
        $lock = Add-Type -Name LockProbe -Namespace AgentB -PassThru -MemberDefinition '[DllImport("user32.dll")] public static extern bool LockWorkStation();'
        if (-not $lock::LockWorkStation()) { throw 'LockWorkStation failed.' }
        Start-Sleep -Seconds 3
        $locked = [bool](Get-Process -Name LogonUI -ErrorAction SilentlyContinue)
        $tsdiscon = Join-Path $env:SystemRoot 'System32\tsdiscon.exe'
        $disconnected = 'not attempted (tsdiscon.exe absent)'
        if (Test-Path -LiteralPath $tsdiscon) { & $tsdiscon 2>&1 | Out-Null; $disconnected = "tsdiscon exit $LASTEXITCODE" }
        $answers = 0
        $deadline = [DateTime]::UtcNow.AddSeconds($LockedSeconds)
        while ([DateTime]::UtcNow -lt $deadline) {
            if (-not (Test-Ready)) { throw "Agent_b stopped answering while the workstation was locked (after $answers answers)." }
            $answers++
            Start-Sleep -Seconds 5
        }
        $after = Get-Running
        if ($after.Count -ne 1 -or $after[0].Id -ne $first.Id) { throw 'Agent_b did not survive the lock and disconnect as the same process.' }
        Write-Host "PASS: PID $($first.Id) kept answering $answers times over $LockedSeconds s with the workstation locked (LogonUI running: $locked; disconnect: $disconnected)"
    }

    # A graceful stop records its reason as it happens.
    $before = (Get-LogLines).Count
    # Item 2eq: WM_CLOSE from another process is ignored; the stop event is the graceful stop.
    & $taskkill /PID $first.Id | Out-Null
    Start-Sleep -Seconds 3
    if (-not (Get-Running | Where-Object Id -eq $first.Id)) { throw 'Agent_b stopped on a WM_CLOSE from another process.' }
    Write-Host "PASS: PID $($first.Id) ignored WM_CLOSE (taskkill without /F)"
    $channel = Request-AgentbGracefulStop -ApplicationRoot $ApplicationDirectory -ProcessId $first.Id
    if ($channel -ne 'stop event') { throw "The graceful stop did not use the stop event: $channel" }
    Wait-Until { -not (Get-Running) } 20 'the graceful stop'
    $reason = @(Get-LogLines | Select-Object -Skip $before | Where-Object { $_ -match "Agent_b PID $($first.Id) stopped: " })
    if (-not $reason.Count) { throw "The graceful stop left no reason in the launcher log: $((Get-LogLines | Select-Object -Skip $before) -join ' | ')" }
    Write-Host "PASS: a graceful stop wrote its reason: $($reason[-1].Trim())"

    # A forced end is recorded at the next start.
    Invoke-SignIn
    Wait-Until { (Get-Running).Count -eq 1 -and (Test-Ready) } 60 'the second sign-in start'
    $killed = (Get-Running)[0]
    & $taskkill /PID $killed.Id /F | Out-Null
    Wait-Until { -not (Get-Running) } 20 'the forced end'
    $before = (Get-LogLines).Count
    Invoke-SignIn
    Wait-Until { (Get-Running).Count -eq 1 -and (Test-Ready) } 60 'the start after a forced end'
    $recorded = @(Get-LogLines | Select-Object -Skip $before | Where-Object { $_ -match "Agent_b PID $($killed.Id) \(started [^)]+\) ended without recording a reason" })
    if (-not $recorded.Count) { throw "The forced end was not recorded at the next start: $((Get-LogLines | Select-Object -Skip $before) -join ' | ')" }
    Write-Host "PASS: a forced end was recorded at the next start: $($recorded[-1].Trim())"
} finally {
    foreach ($process in @(Get-Running)) {
        try { $null = Request-AgentbGracefulStop -ApplicationRoot $ApplicationDirectory -ProcessId $process.Id } catch { }
        if (-not $process.WaitForExit(15000)) { & $taskkill /PID $process.Id /T /F | Out-Null }
    }
}
