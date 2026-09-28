# Item 2na: THE PROOF THAT NOTHING TOOK THE OPERATOR'S SCREEN.
#
# The spawn gate is a source check: it can only see what a script was written to do.
# This watches what actually happened. It samples the foreground window while a command
# runs and reports every window that took focus, so a claim that the suite stays out of
# the operator's way is measured rather than asserted.
#
# It is a REPORTER, not a policeman, with one exception: a window belonging to a process
# this repository started is a failure, because that is the thing 2na forbids. The
# operator's own editor coming to the front while he works is not.
[CmdletBinding()]
param(
    # The command to run under the sampler, and its arguments.
    [Parameter(Mandatory)][string]$FilePath,
    [string[]]$ArgumentList = @(),
    [int]$IntervalMS = 250,
    # Process names this repository starts. A window owned by one of these taking focus
    # is the failure 2na exists to prevent.
    [string[]]$OurProcesses = @("Agent_b", "Agent_b-setup", "pwsh", "powershell", "wscript", "cscript", "conhost", "msedge", "chrome", "node"),
    # Item 2na's @verify: a gate that is ABOUT a window needs one, and naming it here is
    # the documented exception the item asks for rather than a check quietly weakened.
    # The two host-window gates and item 2mt's drag probe are the only ones, and they
    # are named by the caller so the exception is visible in the command that ran.
    [string[]]$ExpectWindowFrom = @()
)

$ErrorActionPreference = 'Stop'

Add-Type -Namespace AgentB -Name Fg -MemberDefinition @'
    [DllImport("user32.dll")] public static extern System.IntPtr GetForegroundWindow();
    [DllImport("user32.dll")] public static extern int GetWindowThreadProcessId(System.IntPtr handle, out int processId);
    [DllImport("user32.dll", CharSet = System.Runtime.InteropServices.CharSet.Unicode)] public static extern int GetWindowTextW(System.IntPtr handle, System.Text.StringBuilder text, int count);
'@

function Get-Foreground {
    $handle = [AgentB.Fg]::GetForegroundWindow()
    if ($handle -eq [IntPtr]::Zero) { return $null }
    $processId = 0
    $null = [AgentB.Fg]::GetWindowThreadProcessId($handle, [ref]$processId)
    $builder = New-Object System.Text.StringBuilder 512
    $null = [AgentB.Fg]::GetWindowTextW($handle, $builder, $builder.Capacity)
    $name = try { (Get-Process -Id $processId -ErrorAction Stop).ProcessName } catch { "(gone)" }
    return [pscustomobject]@{ Handle = $handle; ProcessId = $processId; Process = $name; Title = $builder.ToString() }
}

$before = Get-Foreground
if ($before) { Write-Host "SAMPLER: the foreground at the start belongs to $($before.Process) (PID $($before.ProcessId))" }

# The child runs with no window of its own, so its output has nowhere to go unless it
# is redirected. An early version of this swallowed the whole suite's output and
# reported a pass count of zero, which is worse than useless.
$outPath = Join-Path ([IO.Path]::GetTempPath()) ("sampler-out-" + [Guid]::NewGuid().ToString('N') + ".log")
$errPath = [IO.Path]::ChangeExtension($outPath, ".err.log")
$child = Start-Process -FilePath $FilePath -ArgumentList $ArgumentList -PassThru -WindowStyle Hidden -RedirectStandardOutput $outPath -RedirectStandardError $errPath
$seen = @{}
$order = @()
while (-not $child.HasExited) {
    $current = Get-Foreground
    if ($current -and -not $seen.ContainsKey([string]$current.Handle)) {
        $seen[[string]$current.Handle] = $current
        $order += $current
    }
    Start-Sleep -Milliseconds $IntervalMS
}
$child.WaitForExit()
$suiteExit = $child.ExitCode
# The command's own account of itself, so the sampler adds to the record instead of
# replacing it.
foreach ($path in @($outPath, $errPath)) {
    if (Test-Path -LiteralPath $path) {
        Get-Content -LiteralPath $path | ForEach-Object { Write-Host $_ }
        Remove-Item -LiteralPath $path -Force -ErrorAction SilentlyContinue
    }
}

# Whatever had focus at the start does not count as having taken it.
$took = @($order | Where-Object { -not $before -or [string]$_.Handle -ne [string]$before.Handle })
Write-Host "SAMPLER: $($took.Count) window(s) took focus while the command ran"
foreach ($one in $took) {
    Write-Host ("  {0} (PID {1}): {2}" -f $one.Process, $one.ProcessId, $(if ($one.Title) { $one.Title } else { "(no title)" }))
}
$ours = @($took | Where-Object { $OurProcesses -contains $_.Process -and $ExpectWindowFrom -notcontains $_.Process })
$expected = @($took | Where-Object { $ExpectWindowFrom -contains $_.Process })
foreach ($one in $expected) {
    Write-Host ("SAMPLER EXPECTED: {0} showed a window, which this run allows by name" -f $one.Process)
}
if ($ours.Count) {
    Write-Host "SAMPLER FAIL: a window this repository started took the operator's screen:"
    foreach ($one in $ours) { Write-Host ("  {0} (PID {1}): {2}" -f $one.Process, $one.ProcessId, $one.Title) }
    exit 1
}
Write-Host "SAMPLER PASS: nothing this repository started took focus"
if ($suiteExit -ne 0) { Write-Host "SAMPLER: the command itself exited $suiteExit" }
exit $suiteExit
