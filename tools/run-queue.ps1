[CmdletBinding()]
param(
    # The mailbox to drain. Each block is one order.
    [string]$Inbox = (Join-Path (Split-Path -Parent $PSScriptRoot) 'INBOX.md'),
    # The worker command. The default starts a fresh non-interactive Claude
    # Code session per block; -WorkerCommand lets the operator point the runner
    # at something else without editing this script.
    [string]$WorkerCommand = 'claude',
    [string[]]$WorkerArguments = @('-p'),
    # Print what would run and start nothing.
    [switch]$WhatIfOnly,
    # Stop after this many blocks (0 = the whole queue).
    [int]$MaxBlocks = 0,
    # How long one block may take before the runner gives up on it.
    [int]$BlockTimeoutMinutes = 240
)

# Item 2gt: each queued order runs in a FRESH worker session.
#
# Three consecutive orders -- v1.1.2, v1.1.3, v1.2.0 -- closed early with steps
# not started, in the worker's own words "deliberately, no room to finish",
# because the INBOX queue ran every block in one session and that session's
# context filled. The operator, repeatedly: "make sure he actually works a long
# time"; "why is he stopping so fast."
#
# The fix is not a bigger session. It is that the QUEUE is the runner's job and
# the BLOCK is the worker's: the runner splits the mailbox, starts one worker
# per block, waits for it, records what happened, and moves on. The operator
# starts the runner once and a night is as many blocks as the mailbox holds.
#
# The runner never edits an order, never publishes, and never decides anything
# about the work. Truncation is the worker's, on its own report, exactly as the
# standing rules say -- the runner only notices that the block is gone and
# advances.

$ErrorActionPreference = 'Stop'
. (Join-Path (Split-Path -Parent $PSScriptRoot) 'scripts\windows-tools.ps1')

$repoRoot = Split-Path -Parent $PSScriptRoot
$inflight = Join-Path $repoRoot 'plan\_inflight.md'
$logDirectory = Join-Path $repoRoot 'logs\queue'
$null = New-Item -ItemType Directory -Path $logDirectory -Force
$runStamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$runLog = Join-Path $logDirectory "run-queue-$runStamp.log"

function Write-Runner {
    param([string]$Text)
    $line = '{0} {1}' -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $Text
    Write-Host $line
    [IO.File]::AppendAllText($runLog, $line + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
}

# A queue marker per block, in the same file and the same shape the worker's
# own markers use, so a reader sees the night as one sequence.
function Write-QueueMarker {
    param([string]$Text)
    if (-not (Test-Path -LiteralPath $inflight)) { return }
    $existing = [IO.File]::ReadAllText($inflight)
    $eol = if ($existing -match "`r`n") { "`r`n" } else { "`n" }
    $prefix = if ($existing.Trim()) { $existing.TrimEnd() + $eol + $eol } else { '' }
    [IO.File]::WriteAllText($inflight, $prefix + $Text + $eol, [Text.UTF8Encoding]::new($false))
}

# Count the blocks the mailbox holds. A block is separated by a line that is
# exactly the separator; this mirrors plan-publish's split-inbox so the runner
# and the worker always agree on what a block is.
function Get-BlockCount {
    if (-not (Test-Path -LiteralPath $Inbox)) { return 0 }
    $text = [IO.File]::ReadAllText($Inbox)
    if (-not $text.Trim()) { return 0 }
    return ([regex]::Matches($text, '(?m)^==== NEXT ORDER ====\s*$').Count) + 1
}

function Get-FirstBlockHeader {
    if (-not (Test-Path -LiteralPath $Inbox)) { return '' }
    foreach ($line in [IO.File]::ReadAllLines($Inbox)) {
        if ($line.Trim()) { return $line.Trim() }
    }
    return ''
}

# The pointer prompt is checked in beside this script, so the operator is never
# asked to retype it and the runner cannot drift from what the worker expects.
$pointerPrompt = 'Read plan/_inflight.md, then INBOX.md; publish the order via tools/plan-publish.mjs prepare/publish; execute it; append the report to NOTES.md.'

Write-Runner "QUEUE START: $Inbox"
Write-Runner "worker: $WorkerCommand $($WorkerArguments -join ' ')"
$startingBlocks = Get-BlockCount
Write-Runner "blocks in the mailbox: $startingBlocks"
if ($startingBlocks -eq 0) {
    Write-Runner 'QUEUE EMPTY: nothing to run.'
    exit 0
}

$results = @()
$index = 0
while ($true) {
    $remaining = Get-BlockCount
    if ($remaining -eq 0) {
        Write-Runner 'QUEUE EMPTY: every block has been run and truncated.'
        break
    }
    if ($MaxBlocks -gt 0 -and $index -ge $MaxBlocks) {
        Write-Runner "STOPPING: -MaxBlocks $MaxBlocks reached with $remaining block(s) left."
        break
    }
    $index++
    $header = Get-FirstBlockHeader
    Write-Runner "BLOCK $index START ($remaining left): $header"
    Write-QueueMarker "queue/block $index started $(Get-Date -Format 'HH:mm') -- $header"

    if ($WhatIfOnly) {
        Write-Runner "WHATIF: would start '$WorkerCommand $($WorkerArguments -join ' ')' with the pointer prompt and wait."
        break
    }

    $blockLog = Join-Path $logDirectory "run-queue-$runStamp-block$index.log"
    $started = Get-Date
    $exitCode = $null
    try {
        # System.Diagnostics.Process rather than Start-Process: with
        # redirection, Start-Process -PassThru leaves ExitCode empty on
        # PowerShell 5.1, and the runner's log would report every block as
        # 'exit ' with nothing in it. This also feeds the pointer prompt on
        # stdin directly, so no shell quoting can mangle it.
        $info = [Diagnostics.ProcessStartInfo]::new()
        $info.FileName = $WorkerCommand
        # PowerShell 5.1 runs on .NET Framework, whose ProcessStartInfo has no
        # ArgumentList, so the arguments are quoted into one string here.
        $info.Arguments = ($WorkerArguments | ForEach-Object {
            if ($_ -match "s") { '"' + $_ + '"' } else { $_ }
        }) -join " "
        $info.WorkingDirectory = $repoRoot
        $info.UseShellExecute = $false
        $info.RedirectStandardInput = $true
        $info.RedirectStandardOutput = $true
        $info.RedirectStandardError = $true
        $process = [Diagnostics.Process]::Start($info)
        $process.StandardInput.Write($pointerPrompt)
        $process.StandardInput.Close()
        $outputTask = $process.StandardOutput.ReadToEndAsync()
        $errorTask = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit($BlockTimeoutMinutes * 60 * 1000)) {
            Write-Runner "BLOCK $index TIMEOUT after $BlockTimeoutMinutes minutes; ending it by PID $($process.Id)."
            # Hard stop (12): the runner ends only the process it started, by PID.
            Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
            $exitCode = 'timeout'
        } else {
            $exitCode = $process.ExitCode
        }
        [IO.File]::WriteAllText($blockLog, $outputTask.GetAwaiter().GetResult(), [Text.UTF8Encoding]::new($false))
        [IO.File]::WriteAllText("$blockLog.err", $errorTask.GetAwaiter().GetResult(), [Text.UTF8Encoding]::new($false))
    } catch {
        Write-Runner "BLOCK $index COULD NOT START: $($_.Exception.Message)"
        $exitCode = 'not-started'
    }
    $duration = [int]((Get-Date) - $started).TotalSeconds

    # The block's own report is the signal that it finished: the worker
    # truncates its block when it appends the report, so the mailbox shrinking
    # is what advances the queue. A worker that exits without truncating has
    # not finished its block, and the runner says so rather than looping on it.
    $after = Get-BlockCount
    $advanced = $after -lt $remaining
    $outcome = if ($advanced) { 'completed' } elseif ($exitCode -eq 'timeout') { 'timed out' } elseif ($exitCode -eq 'not-started') { 'could not start' } else { 'ended without truncating its block' }
    Write-Runner "BLOCK $index $($outcome.ToUpper()): exit $exitCode, $duration s, blocks left $after"
    Write-QueueMarker "queue/block $index $outcome $(Get-Date -Format 'HH:mm') -- exit $exitCode after $duration s; blocks left $after"
    $results += [pscustomobject]@{ Block = $index; Header = $header; Outcome = $outcome; ExitCode = $exitCode; Seconds = $duration; BlocksLeft = $after }

    if (-not $advanced) {
        # Running the same block again would repeat work the worker already
        # did, so the runner stops and leaves the mailbox for the operator.
        Write-Runner "STOPPING: block $index did not truncate itself, so the queue cannot advance without repeating it."
        break
    }
}

Write-Runner 'QUEUE END'
foreach ($result in $results) {
    Write-Runner ("  block {0}: {1} in {2} s (exit {3}) -- {4}" -f $result.Block, $result.Outcome, $result.Seconds, $result.ExitCode, $result.Header)
}
Write-Runner "log: $runLog"
if ($results | Where-Object { $_.Outcome -ne 'completed' }) { exit 1 }
exit 0
