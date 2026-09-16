function Assert-RemovalWithinAllowedRoots {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string[]]$AllowedRoots,
        [string]$Purpose = 'removal'
    )
    if ([string]::IsNullOrWhiteSpace($Path) -or -not $AllowedRoots.Count) {
        throw "Refusing $Purpose without a path and explicit allowed removal roots."
    }
    $full = [IO.Path]::GetFullPath([Environment]::ExpandEnvironmentVariables($Path)).TrimEnd('\')
    foreach ($rootPath in $AllowedRoots) {
        if ([string]::IsNullOrWhiteSpace($rootPath)) { continue }
        $root = [IO.Path]::GetFullPath([Environment]::ExpandEnvironmentVariables($rootPath)).TrimEnd('\')
        if ($full.Equals($root, [StringComparison]::OrdinalIgnoreCase) -or
            $full.StartsWith($root + '\', [StringComparison]::OrdinalIgnoreCase)) {
            return $full
        }
    }
    throw "Refusing $Purpose outside allowed removal roots: $full"
}

# Remove-TreeWithinAllowedRoots is the only recursive removal scripts use. It
# asserts the allow-list, refuses a volume root, and unlinks every junction or
# symbolic link inside the tree before removing it, so a removal can never
# descend into a directory it did not create. v0.64.0/W1: a worktree holding
# junctions to the repository's node_modules and .tools\go was removed with
# `git worktree remove --force`, which emptied both targets.
function Remove-ReparsePointsWithin {
    param([Parameter(Mandatory = $true)][string]$Path)
    $pending = New-Object System.Collections.Generic.Stack[string]
    $pending.Push($Path)
    while ($pending.Count) {
        $directory = New-Object IO.DirectoryInfo ($pending.Pop())
        foreach ($child in $directory.GetFileSystemInfos()) {
            if ($child.Attributes -band [IO.FileAttributes]::ReparsePoint) {
                if ($child -is [IO.DirectoryInfo]) { [IO.Directory]::Delete($child.FullName, $false) } else { [IO.File]::Delete($child.FullName) }
            } elseif ($child -is [IO.DirectoryInfo]) {
                $pending.Push($child.FullName)
            }
        }
    }
}

function Remove-TreeWithinAllowedRoots {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string[]]$AllowedRoots,
        [string]$Purpose = 'removal'
    )
    $full = Assert-RemovalWithinAllowedRoots -Path $Path -AllowedRoots $AllowedRoots -Purpose $Purpose
    if ([IO.Path]::GetPathRoot($full).TrimEnd('\').Equals($full, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing $Purpose of a volume root: $full"
    }
    if (-not (Test-Path -LiteralPath $full)) { return }
    $item = Get-Item -LiteralPath $full -Force
    if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) {
        if ($item.PSIsContainer) { [IO.Directory]::Delete($full, $false) } else { [IO.File]::Delete($full) }
        return
    }
    if ($item.PSIsContainer) { Remove-ReparsePointsWithin -Path $full }
    Remove-Item -LiteralPath $full -Recurse -Force
}
