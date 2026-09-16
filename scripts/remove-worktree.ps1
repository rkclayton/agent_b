[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Path,
    [string]$Repository = ''
)

# The one way scripts and workers remove a linked git worktree. `git worktree
# remove --force` descends through junctions and empties their targets (git
# 2.53, reproduced at v0.64.0/W1), and baseline worktrees are routinely given
# junctions to this repository's node_modules and .tools\go. This refuses
# anything that is not a registered linked worktree of the repository, unlinks
# every reparse point inside it first, and only then lets git remove it.

$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($Repository)) { $Repository = Split-Path -Parent $PSScriptRoot }
. (Join-Path $PSScriptRoot 'removal-guard.ps1')

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
$removalPath = Assert-RemovalWithinAllowedRoots -Path $full -AllowedRoots @($full) -Purpose 'linked worktree removal'
if (Test-Path -LiteralPath $removalPath) { Remove-ReparsePointsWithin -Path $removalPath }
& $git -C $Repository worktree remove --force $removalPath
if ($LASTEXITCODE -ne 0) { throw "git worktree remove exited $LASTEXITCODE for $removalPath" }
Write-Host "REMOVED WORKTREE: $removalPath"
