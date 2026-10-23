[CmdletBinding()]
param(
    # Local time, HH:mm. The real-model proof never runs between 08:00 and 17:00 (the shared GPU).
    [string]$At,
    # The candidate executable the proof starts, and the folder its journals and output go to.
    [string]$App,
    [string]$Evidence,
    # Optional comma-separated cases; empty runs the --real set.
    [string]$Cases = '',
    [string]$Name = 'Agentb-real-proof',
    # Remove the named task and its files' trigger; registers nothing.
    [switch]$Remove
)

# Item 2wi: the real-model compaction proof is a scheduled measurement, never a release gate. This
# registers it as a one-shot task for the current user, hidden (wscript, window style 0), that deletes
# itself when the run ends. The task needs the user's logon for the connection credential.

$ErrorActionPreference = 'Stop'
if ($Remove) {
    Unregister-ScheduledTask -TaskName $Name -Confirm:$false -ErrorAction SilentlyContinue
    Write-Host "REAL PROOF TASK REMOVED: $Name"
    return
}
if (-not $At -or -not $App -or -not $Evidence) { throw 'schedule-real-proof needs -At, -App and -Evidence.' }
$time = [TimeSpan]::ParseExact($At, 'hh\:mm', [Globalization.CultureInfo]::InvariantCulture)
if ($time -ge [TimeSpan]::FromHours(8) -and $time -lt [TimeSpan]::FromHours(17)) { throw "REFUSED: the real-model proof never runs between 08:00 and 17:00 (asked for $At)." }

$repository = Split-Path -Parent $PSScriptRoot
$App = [IO.Path]::GetFullPath($App); $Evidence = [IO.Path]::GetFullPath($Evidence)
if (-not (Test-Path -LiteralPath $App -PathType Leaf)) { throw "The candidate executable is missing: $App" }
$null = New-Item -ItemType Directory -Path $Evidence -Force
$start = [DateTime]::Today.Add($time); if ($start -le [DateTime]::Now) { $start = $start.AddDays(1) }

$quote = { param($value) "'" + ($value -replace "'", "''") + "'" }
$only = if ($Cases) { " --only $(& $quote $Cases)" } else { '' }
$script = Join-Path $Evidence 'real-proof.ps1'
@"
`$ErrorActionPreference = 'Continue'
Set-Location -LiteralPath $(& $quote $repository)
[IO.File]::WriteAllText($(& $quote (Join-Path $Evidence 'real-proof-started.txt')), [DateTime]::Now.ToString('o'))
& node 'tools\compaction-proof.mjs' --real --app $(& $quote $App) --evidence $(& $quote (Join-Path $Evidence 'journals')) --raw-output $(& $quote (Join-Path $Evidence 'real-proof-output.txt'))$only
[IO.File]::WriteAllText($(& $quote (Join-Path $Evidence 'real-proof-ended.txt')), "`$([DateTime]::Now.ToString('o')) exit `$LASTEXITCODE")
Unregister-ScheduledTask -TaskName $(& $quote $Name) -Confirm:`$false -ErrorAction SilentlyContinue
"@ | Set-Content -LiteralPath $script -Encoding utf8
$launcher = Join-Path $Evidence 'real-proof.vbs'
"CreateObject(""WScript.Shell"").Run ""pwsh.exe -NoProfile -NonInteractive -WindowStyle Hidden -ExecutionPolicy Bypass -File """"$script"""""", 0, True" | Set-Content -LiteralPath $launcher -Encoding ascii

$action = New-ScheduledTaskAction -Execute 'wscript.exe' -Argument "//B //NoLogo `"$launcher`""
$trigger = New-ScheduledTaskTrigger -Once -At $start
$principal = New-ScheduledTaskPrincipal -UserId ([Security.Principal.WindowsIdentity]::GetCurrent().Name) -LogonType Interactive -RunLevel Limited
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable -ExecutionTimeLimit (New-TimeSpan -Hours 6) -MultipleInstances IgnoreNew
$null = Register-ScheduledTask -TaskName $Name -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force
$task = Get-ScheduledTask -TaskName $Name
Write-Host "REAL PROOF TASK: $Name state=$($task.State) start=$($start.ToString('yyyy-MM-dd HH:mm')) evidence=$Evidence"
