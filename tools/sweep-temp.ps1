# sweep-temp.ps1 — bounded cleanup of Agent_b temporary and build leftovers.
[CmdletBinding()]
param(
    [switch]$Apply,
    [datetime]$Cutoff,
    [string]$RepositoryRoot,
    [string]$Report,
    [string]$TempRoot,
    [string]$CurrentOrder
)
$ErrorActionPreference = 'Stop'
if (-not $RepositoryRoot) { $RepositoryRoot = Split-Path -Parent $PSScriptRoot }
$RepositoryRoot = [IO.Path]::GetFullPath($RepositoryRoot).TrimEnd('\')
. (Join-Path $RepositoryRoot 'scripts\removal-guard.ps1')
if (-not $TempRoot) { $TempRoot = [IO.Path]::GetTempPath() }
$temp = [IO.Path]::GetFullPath($TempRoot).TrimEnd('\')

if (-not $PSBoundParameters.ContainsKey('Cutoff')) {
    $orders = @(Get-ChildItem -LiteralPath (Join-Path $RepositoryRoot 'logs\evidence') -Directory |
        Where-Object Name -match '^\d{4}-\d\d-\d\d-v\d+\.\d+\.\d+$' |
        Sort-Object { [version]($_.Name -replace '^\d{4}-\d\d-\d\d-v', '') } -Descending)
    if ($orders.Count -lt 3) { throw 'Fewer than three order folders under logs\evidence; pass -Cutoff.' }
    $named = [datetime]::ParseExact($orders[2].Name.Substring(0, 10), 'yyyy-MM-dd', $null)
    $Cutoff = @($orders[2].CreationTime, $named) | Sort-Object | Select-Object -First 1
    $cutoffSource = $orders[2].Name
} else { $cutoffSource = 'parameter' }
if (-not $CurrentOrder -and (Test-Path -LiteralPath (Join-Path $RepositoryRoot 'plan\_inflight.md'))) {
    $marker = Get-Content -LiteralPath (Join-Path $RepositoryRoot 'plan\_inflight.md') -TotalCount 1
    if ($marker -match '^(?<order>rel-[^/]+)') { $CurrentOrder = $Matches.order }
}

$tracked = @(& git -C $RepositoryRoot ls-files | ForEach-Object { $_.Replace('\','/') })
$operational = [Text.StringBuilder]::new()
foreach ($file in @(& git -C $RepositoryRoot ls-files tools scripts tests '.github/workflows')) {
    $path = Join-Path $RepositoryRoot $file
    if (Test-Path -LiteralPath $path -PathType Leaf) { $null = $operational.AppendLine([IO.File]::ReadAllText($path)) }
}
$operationalText = $operational.ToString()
$worktrees = @(& git -C $RepositoryRoot worktree list --porcelain 2>$null | Where-Object { $_ -like 'worktree *' } | ForEach-Object { [IO.Path]::GetFullPath($_.Substring(9).Replace('/', '\')).TrimEnd('\') })
$live = @(Get-Process -ErrorAction SilentlyContinue | ForEach-Object { try { $_.Path } catch { $null } } | Where-Object { $_ })

function Get-TreeInfo([string]$Path) {
    $item = Get-Item -LiteralPath $Path -Force
    if (-not $item.PSIsContainer) { return @{ bytes = [int64]$item.Length; newest = $item.LastWriteTime } }
    $bytes = [int64]0; $newest = $item.LastWriteTime; $pending = [Collections.Stack]::new(); $pending.Push($Path)
    while($pending.Count){ foreach($child in Get-ChildItem -LiteralPath ([string]$pending.Pop()) -Force -ErrorAction SilentlyContinue){ if($child.Attributes -band [IO.FileAttributes]::ReparsePoint){continue}; if($child.PSIsContainer){$pending.Push($child.FullName)}else{$bytes += $child.Length; if($child.LastWriteTime -gt $newest){$newest=$child.LastWriteTime}} } }
    return @{ bytes = $bytes; newest = $newest }
}
function Add-Candidate([Collections.ArrayList]$List, [string]$Path, [string]$Root, [string]$Area, [bool]$AgeLimited = $false, [string]$ForcedKeep = '') {
    if (Test-Path -LiteralPath $Path) { $null = $List.Add(@{ path=$Path; root=$Root; area=$Area; age=$AgeLimited; forced=$ForcedKeep }) }
}
$candidates = [Collections.ArrayList]::new()
foreach ($entry in Get-ChildItem -LiteralPath $temp -Force -ErrorAction SilentlyContinue) {
    if ($entry.Name -notmatch '^(?i)agent_?b(?=$|[-_.])') { continue }
    if ($entry.PSIsContainer -and $entry.Name -ieq 'agentb-worker') { foreach ($order in Get-ChildItem -LiteralPath $entry.FullName -Directory -Force) { Add-Candidate $candidates $order.FullName $entry.FullName 'temp' $true } }
    else { Add-Candidate $candidates $entry.FullName $temp 'temp' $true }
}
foreach ($area in '.tmp','.codex-temp','.claude-temp') { $root=Join-Path $RepositoryRoot $area; if(Test-Path -LiteralPath $root){ foreach($entry in Get-ChildItem -LiteralPath $root -Force){ Add-Candidate $candidates $entry.FullName $root $area $true } } }
foreach ($entry in Get-ChildItem -LiteralPath $RepositoryRoot -Directory -Force -Filter '.plan-candidate-*') { Add-Candidate $candidates $entry.FullName $RepositoryRoot 'plan-candidate' $false $(if($CurrentOrder -and $entry.Name -like "*$CurrentOrder*"){'current order'}else{''}) }
$candidateRoot=Join-Path $RepositoryRoot 'candidates'
if(Test-Path -LiteralPath $candidateRoot){ $newest=@(Get-ChildItem -LiteralPath $candidateRoot -Force | Sort-Object LastWriteTime -Descending | Select-Object -First 3 | ForEach-Object FullName); foreach($entry in Get-ChildItem -LiteralPath $candidateRoot -Force){ Add-Candidate $candidates $entry.FullName $candidateRoot 'candidates' $false $(if($newest -contains $entry.FullName){'one of the three newest candidates'}else{''}) } }
foreach($name in 'candidate','scratch','test-results','playwright-report'){ Add-Candidate $candidates (Join-Path $RepositoryRoot $name) $RepositoryRoot $name }
foreach($entry in Get-ChildItem -LiteralPath $RepositoryRoot -File -Force | Where-Object Extension -in '.exe','.dll','.com'){ Add-Candidate $candidates $entry.FullName $RepositoryRoot 'root-program' }

$rows = foreach ($candidate in $candidates) {
    $path=[IO.Path]::GetFullPath($candidate.path); $name=Split-Path -Leaf $path; $info=Get-TreeInfo $path
    $relative=$(if($path.StartsWith($RepositoryRoot+'\',[StringComparison]::OrdinalIgnoreCase)){$path.Substring($RepositoryRoot.Length+1).Replace('\','/')}else{''}); $reason=$candidate.forced
    if(-not $reason -and $candidate.age -and $info.newest -ge $Cutoff){$reason='within the last three orders'}
    elseif(-not $reason -and $name -like 'Agent_b-install-rollback-*'){$reason='an install rollback copy'}
    elseif(-not $reason -and @($tracked | Where-Object { $_ -eq $relative -or $_.StartsWith($relative+'/',[StringComparison]::OrdinalIgnoreCase) }).Count){$reason='git tracks it or something beneath it'}
    elseif(-not $reason -and ($candidate.area -eq 'root-program') -and $operationalText.IndexOf($name,[StringComparison]::OrdinalIgnoreCase) -ge 0){$reason='named by a tracked script, test, or workflow'}
    elseif(-not $reason -and @($worktrees | Where-Object { $_ -eq $path -or $_.StartsWith($path+'\',[StringComparison]::OrdinalIgnoreCase) }).Count){$reason='a linked git worktree or holds one'}
    elseif(-not $reason -and @($live | Where-Object { $_ -eq $path -or $_.StartsWith($path+'\',[StringComparison]::OrdinalIgnoreCase) }).Count){$reason='holds a running program'}
    [pscustomobject]@{area=$candidate.area;path=$path;root=$candidate.root;bytes=$info.bytes;newest=$info.newest.ToString('s');action=$(if($reason){'keep'}else{'remove'});reason=$reason;result=$null}
}
foreach($row in $rows | Where-Object action -eq 'remove'){
    if(-not $Apply){$row.result='whatif';continue}
    try{Remove-TreeWithinAllowedRoots -Path $row.path -AllowedRoots @($row.root) -Purpose 'temp sweep';$row.result=$(if(Test-Path -LiteralPath $row.path){'partly removed'}else{'removed'})}catch{$row.result="failed: $($_.Exception.Message)"}
}
$summary=[ordered]@{at=(Get-Date).ToString('o');applied=[bool]$Apply;cutoff=$Cutoff.ToString('s');cutoff_from=$cutoffSource;remove_count=@($rows|Where-Object action -eq 'remove').Count;remove_bytes=[int64](($rows|Where-Object action -eq 'remove'|Measure-Object bytes -Sum).Sum);keep_count=@($rows|Where-Object action -eq 'keep').Count;keep_bytes=[int64](($rows|Where-Object action -eq 'keep'|Measure-Object bytes -Sum).Sum);failed=@($rows|Where-Object result -like 'failed*').Count;rows=$rows}
if($Report){$summary|ConvertTo-Json -Depth 4|Set-Content -LiteralPath $Report -Encoding utf8}
foreach($row in $rows){'{0,-16} {1,-6} {2,12:N0}  {3}{4}' -f $row.area,$row.action,$row.bytes,$row.path,$(if($row.reason){" ($($row.reason))"}elseif($row.result -and $row.result -ne 'whatif'){" [$($row.result)]"}else{''})}
'SWEEP {0}: cutoff {1} ({2}); remove {3} ({4} bytes), keep {5} ({6} bytes); failed {7}' -f $(if($Apply){'APPLIED'}else{'WHATIF'}),$summary.cutoff,$cutoffSource,$summary.remove_count,$summary.remove_bytes,$summary.keep_count,$summary.keep_bytes,$summary.failed
if($summary.failed){exit 1}
