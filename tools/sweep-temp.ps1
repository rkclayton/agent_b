# sweep-temp.ps1 — the operator's sweep of Agent_b scratch (item 2ft).
#
# Lists, and with -Apply removes, Agent_b roots and files under %TEMP% (names starting
# Agent_b / AgentB / agentb, and each order folder under agentb-worker) and the
# entries of the repository's retired .tmp, when ALL of these hold:
#   - older than the last three orders: last written before the creation of the
#     third-newest order folder under logs\evidence (or -Cutoff);
#   - no open item (plan\items\*.md) and no line of NOTES.md names it;
#   - no running Agent_b.exe lives under it.
# Everything else is kept and listed with the reason. Listing is the default;
# nothing is removed without -Apply. Removal goes through the removal guard.
[CmdletBinding()]
param(
    [switch]$Apply,
    [datetime]$Cutoff,
    [string]$RepositoryRoot,
    [string]$Report
)
$ErrorActionPreference = 'Stop'
if (-not $RepositoryRoot) { $RepositoryRoot = Split-Path -Parent $PSScriptRoot }
. (Join-Path $RepositoryRoot 'scripts\removal-guard.ps1')

$temp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\')
$repoTmp = Join-Path $RepositoryRoot '.tmp'

if (-not $PSBoundParameters.ContainsKey('Cutoff')) {
    $orders = @(Get-ChildItem -LiteralPath (Join-Path $RepositoryRoot 'logs\evidence') -Directory |
        Where-Object { $_.Name -match '^\d{4}-\d\d-\d\d-v(\d+)\.(\d+)\.(\d+)$' } |
        Sort-Object { [version]($_.Name -replace '^\d{4}-\d\d-\d\d-v', '') } -Descending)
    if ($orders.Count -lt 3) { throw 'Fewer than three order folders under logs\evidence; pass -Cutoff.' }
    # The earlier of the folder's creation and the date in its name: a restored
    # or copied evidence folder is created "now" (v0.70.2 cold review).
    $named = [datetime]::ParseExact($orders[2].Name.Substring(0, 10), 'yyyy-MM-dd', $null)
    $Cutoff = @($orders[2].CreationTime, $named) | Sort-Object | Select-Object -First 1
    $cutoffSource = $orders[2].Name
} else { $cutoffSource = 'parameter' }

$cited = [Text.StringBuilder]::new()
$null = $cited.Append((Get-Content -Raw -LiteralPath (Join-Path $RepositoryRoot 'NOTES.md')))
foreach ($item in Get-ChildItem -LiteralPath (Join-Path $RepositoryRoot 'plan\items') -Filter *.md) {
    $null = $cited.Append((Get-Content -Raw -LiteralPath $item.FullName))
}
$citedText = $cited.ToString()

# A linked git worktree is left to tools\remove-worktree.ps1, which also
# removes git's record of it.
$worktrees = @(& git -C $RepositoryRoot worktree list --porcelain 2>$null | Where-Object { $_ -like 'worktree *' } | ForEach-Object { [IO.Path]::GetFullPath($_.Substring(9).Replace('/', '\')).TrimEnd('\') })

$live = @(Get-Process -Name 'Agent_b' -ErrorAction SilentlyContinue | ForEach-Object { try { $_.Path } catch { $null } } | Where-Object { $_ })

function Get-TreeInfo {
    param([string]$Path)
    $item = Get-Item -LiteralPath $Path -Force
    if (-not $item.PSIsContainer) { return @{ bytes = [int64]$item.Length; newest = $item.LastWriteTime } }
    $bytes = [int64]0; $newest = $item.LastWriteTime
    foreach ($file in Get-ChildItem -LiteralPath $Path -Recurse -Force -File -ErrorAction SilentlyContinue) {
        $bytes += $file.Length
        if ($file.LastWriteTime -gt $newest) { $newest = $file.LastWriteTime }
    }
    return @{ bytes = $bytes; newest = $newest }
}

$candidates = @()
# v0.70.2/W7: files at the top of %TEMP% (transcripts, logs) are swept too.
foreach ($entry in Get-ChildItem -LiteralPath $temp -Force -ErrorAction SilentlyContinue) {
    if ($entry.Name -notmatch '^(?i)agent_?b(?=$|[-_.])') { continue }
    if ($entry.PSIsContainer -and $entry.Name -ieq 'agentb-worker') {
        foreach ($order in Get-ChildItem -LiteralPath $entry.FullName -Directory -Force) { $candidates += @{ path = $order.FullName; root = $entry.FullName; area = 'temp' } }
        continue
    }
    $candidates += @{ path = $entry.FullName; root = $temp; area = 'temp' }
}
if (Test-Path -LiteralPath $repoTmp) {
    foreach ($entry in Get-ChildItem -LiteralPath $repoTmp -Force) { $candidates += @{ path = $entry.FullName; root = $repoTmp; area = '.tmp' } }
}

$rows = foreach ($candidate in $candidates) {
    $name = Split-Path -Leaf $candidate.path
    $info = Get-TreeInfo -Path $candidate.path
    $reason = $null
    if ($info.newest -ge $Cutoff) { $reason = 'within the last three orders' }
    elseif ($name -like 'Agent_b-install-rollback-*') { $reason = 'an install rollback copy: it may hold the only copy of the previous install' }
    elseif ($worktrees | Where-Object { $full = [IO.Path]::GetFullPath($candidate.path).TrimEnd('\'); $_ -eq $full -or $_.StartsWith($full + '\', [StringComparison]::OrdinalIgnoreCase) }) { $reason = 'a linked git worktree (or holds one): remove it with tools\remove-worktree.ps1' }
    elseif ($live | Where-Object { $_.StartsWith($candidate.path + '\', [StringComparison]::OrdinalIgnoreCase) }) { $reason = 'holds a running Agent_b.exe' }
    elseif ($citedText.IndexOf($name, [StringComparison]::OrdinalIgnoreCase) -ge 0) { $reason = 'named by NOTES.md or an open item' }
    [pscustomobject]@{ area = $candidate.area; path = $candidate.path; root = $candidate.root; bytes = $info.bytes; newest = $info.newest.ToString('s'); action = $(if ($reason) { 'keep' } else { 'remove' }); reason = $reason; result = $null }
}

foreach ($row in $rows | Where-Object action -eq 'remove') {
    if (-not $Apply) { $row.result = 'whatif'; continue }
    try { Remove-TreeWithinAllowedRoots -Path $row.path -AllowedRoots @($row.root) -Purpose 'temp sweep'; $row.result = $(if (Test-Path -LiteralPath $row.path) { 'partly removed' } else { 'removed' }) }
    catch { $row.result = "failed: $($_.Exception.Message)" }
}

$summary = [ordered]@{
    at = (Get-Date).ToString('o'); applied = [bool]$Apply; cutoff = $Cutoff.ToString('s'); cutoff_from = $cutoffSource
    temp_total_mb = [math]::Round((($rows | Where-Object area -eq 'temp' | Measure-Object bytes -Sum).Sum) / 1MB, 1)
    tmp_total_mb = [math]::Round((($rows | Where-Object area -eq '.tmp' | Measure-Object bytes -Sum).Sum) / 1MB, 1)
    remove_count = @($rows | Where-Object action -eq 'remove').Count
    remove_mb = [math]::Round((($rows | Where-Object action -eq 'remove' | Measure-Object bytes -Sum).Sum) / 1MB, 1)
    keep_count = @($rows | Where-Object action -eq 'keep').Count
    keep_mb = [math]::Round((($rows | Where-Object action -eq 'keep' | Measure-Object bytes -Sum).Sum) / 1MB, 1)
    failed = @($rows | Where-Object { $_.result -like 'failed*' }).Count
    rows = $rows
}
if ($Report) { $summary | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $Report -Encoding utf8 }
foreach ($row in $rows) { '{0,-6} {1,-6} {2,10:N0} KB  {3}{4}' -f $row.area, $row.action, [math]::Round($row.bytes / 1KB), $row.path, $(if ($row.reason) { "  ($($row.reason))" } elseif ($row.result -and $row.result -ne 'whatif') { "  [$($row.result)]" } else { '' }) }
'SWEEP {0}: cutoff {1} ({2}); remove {3} ({4} MB), keep {5} ({6} MB); failed {7}' -f $(if ($Apply) { 'APPLIED' } else { 'WHATIF' }), $summary.cutoff, $cutoffSource, $summary.remove_count, $summary.remove_mb, $summary.keep_count, $summary.keep_mb, $summary.failed
