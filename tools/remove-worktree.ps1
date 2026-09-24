[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Path,
    [string]$Repository = ''
)

# The one way scripts and workers remove a linked git worktree. `git worktree
# remove --force` descends through junctions and empties their targets (git
# 2.53, reproduced at v0.64.0/W1), and baseline worktrees are routinely given
# junctions to this repository's node_modules and .tools\go. This refuses
# anything that is not a registered linked worktree of the repository, removes
# the tree itself with the removal guard's bottom-up walk (a junction is
# unlinked, never entered, even one created after the removal began), and only
# then has git prune the worktree's record. Git never walks the tree.

$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($Repository)) { $Repository = Split-Path -Parent $PSScriptRoot }
. (Join-Path $Repository 'scripts\removal-guard.ps1')

$git = (Get-Command git.exe -ErrorAction Stop).Source
$full = [IO.Path]::GetFullPath($Path).TrimEnd('\')
$porcelain = @(& $git -C $Repository worktree list --porcelain)
if ($LASTEXITCODE -ne 0) { throw "git worktree list failed in $Repository" }
$registered = @($porcelain | Where-Object { $_ -like 'worktree *' } | ForEach-Object { [IO.Path]::GetFullPath($_.Substring(9).Replace('/', '\')).TrimEnd('\') })
if (-not $registered.Count) { throw "no worktrees listed for $Repository" }
if ($registered[0].Equals($full, [StringComparison]::OrdinalIgnoreCase)) { throw "Refusing to remove the main worktree: $full" }
if (-not ($registered | Where-Object { $_.Equals($full, [StringComparison]::OrdinalIgnoreCase) })) {
    throw "Refusing to remove a path that is not a linked worktree of ${Repository}: $full"
}
# v0.65.0/W15 cold review: the worktree list is read from .git/worktrees/*/gitdir,
# which anything able to write .git can edit. A path is removed only when its own
# .git file names a record under this repository's worktrees folder and that
# record names this path back, and the record is not locked. Only that record is
# removed afterwards; other stale records are left for git.
$commonDir = [string](& $git -C $Repository rev-parse --path-format=absolute --git-common-dir | Select-Object -First 1)
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($commonDir)) { throw "git rev-parse --git-common-dir failed in $Repository" }
$worktreesRoot = [IO.Path]::GetFullPath((Join-Path $commonDir.Trim() 'worktrees')).TrimEnd('\')
$dotGit = Join-Path $full '.git'
if (-not (Test-Path -LiteralPath $dotGit -PathType Leaf)) { throw "Refusing: $full has no .git file linking it to $Repository" }
$pointer = (Get-Content -LiteralPath $dotGit -TotalCount 1) -replace '^gitdir:\s*', ''
$record = [IO.Path]::GetFullPath($pointer.Trim().Replace('/', '\')).TrimEnd('\')
if (-not $record.StartsWith($worktreesRoot + '\', [StringComparison]::OrdinalIgnoreCase) -or (Split-Path -Parent $record) -ne $worktreesRoot) {
    throw "Refusing: $full's .git names $record, not a worktree record of $Repository"
}
$backLink = Join-Path $record 'gitdir'
if (-not (Test-Path -LiteralPath $backLink -PathType Leaf) -or
    -not ([IO.Path]::GetFullPath(((Get-Content -LiteralPath $backLink -TotalCount 1).Trim()).Replace('/', '\')).TrimEnd('\')).Equals($dotGit, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Refusing: the worktree record $record does not name $full back"
}
if (Test-Path -LiteralPath (Join-Path $record 'locked')) { throw "Refusing to remove a locked worktree: $full" }
Remove-TreeWithinAllowedRoots -Path $full -AllowedRoots @(Split-Path -Parent $full) -Purpose 'linked worktree removal'
$removalPath = $full
Remove-TreeWithinAllowedRoots -Path $record -AllowedRoots @($worktreesRoot) -Purpose 'linked worktree record removal'
if (@(& $git -C $Repository worktree list --porcelain) | Where-Object { $_ -like 'worktree *' -and [IO.Path]::GetFullPath($_.Substring(9).Replace('/', '\')).TrimEnd('\').Equals($full, [StringComparison]::OrdinalIgnoreCase) }) {
    throw "git still lists the removed worktree: $full"
}
Write-Host "REMOVED WORKTREE: $removalPath"
