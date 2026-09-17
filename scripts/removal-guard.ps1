function Assert-RemovalWithinAllowedRoots {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string[]]$AllowedRoots,
        [string]$Purpose = 'removal'
    )
    if ([string]::IsNullOrWhiteSpace($Path) -or -not $AllowedRoots.Count) {
        throw "Refusing $Purpose without a path and explicit allowed removal roots."
    }
    # Literal paths: a name such as %HOMEPATH% is a file name here, never a
    # variable (v0.64.0/W8).
    $full = [IO.Path]::GetFullPath($Path).TrimEnd('\')
    $contained = $false
    foreach ($rootPath in $AllowedRoots) {
        if ([string]::IsNullOrWhiteSpace($rootPath)) { continue }
        $root = [IO.Path]::GetFullPath($rootPath).TrimEnd('\')
        # v0.65.0/W9: an allowed root is the container a removal stays inside.
        # A root equal to its own target allowed anything that was not a volume
        # root, which is no allow-list at all.
        if ($full.Equals($root, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing ${Purpose}: an allowed removal root must contain the target, not be it: $full"
        }
        if ($full.StartsWith($root + '\', [StringComparison]::OrdinalIgnoreCase)) { $contained = $true }
    }
    if (-not $contained) { throw "Refusing $Purpose outside allowed removal roots: $full" }
    # v0.65.0/W9: the text of a path says nothing about where it leads. A junction
    # or symbolic link in any existing ancestor redirects the whole removal into
    # its target, so any such ancestor refuses the removal.
    $volume = [IO.Path]::GetPathRoot($full).TrimEnd('\')
    $ancestor = Split-Path -Parent $full
    while ($ancestor -and -not $ancestor.TrimEnd('\').Equals($volume, [StringComparison]::OrdinalIgnoreCase)) {
        if (Test-Path -LiteralPath $ancestor) {
            if ((Get-Item -LiteralPath $ancestor -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) {
                throw "Refusing $Purpose beneath a junction or link: $ancestor"
            }
        }
        $ancestor = Split-Path -Parent $ancestor
    }
    return $full
}

# Remove-TreeWithinAllowedRoots is the only recursive removal scripts use. It
# asserts the allow-list, refuses a volume root, and removes the tree itself,
# bottom-up: every entry's attributes are read again at the moment it is acted
# on, a junction or link is unlinked and never entered, and only a real
# directory is descended into. No recursive delete is handed the tree, so a
# junction that appears after the removal started is unlinked, not followed.
# v0.64.0/W1: a worktree holding junctions to the repository's node_modules and
# .tools\go was removed with `git worktree remove --force`, which emptied both.
function Remove-EntryWithoutFollowing {
    param([Parameter(Mandatory = $true)][string]$Path)
    $info = New-Object IO.DirectoryInfo $Path
    if (-not $info.Exists) {
        $file = New-Object IO.FileInfo $Path
        if ($file.Exists) {
            if ($file.Attributes -band [IO.FileAttributes]::ReadOnly) { $file.Attributes = $file.Attributes -band -bnot [IO.FileAttributes]::ReadOnly }
            [IO.File]::Delete($Path)
        }
        return
    }
    if ($info.Attributes -band [IO.FileAttributes]::ReparsePoint) {
        [IO.Directory]::Delete($Path, $false)
        return
    }
    # Test seam (scripts/test-removal-guard.ps1): something replacing this
    # directory with a junction just before it is entered.
    if ($global:AgentbRemovalBeforeDescend -is [scriptblock]) { & $global:AgentbRemovalBeforeDescend $Path }
    $info = New-Object IO.DirectoryInfo $Path
    if ($info.Exists -and ($info.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
        [IO.Directory]::Delete($Path, $false)
        return
    }
    if (-not $info.Exists) { return }
    foreach ($child in $info.GetFileSystemInfos()) {
        Remove-EntryWithoutFollowing -Path $child.FullName
    }
    # The directory is read again before it is removed: if it became a link while
    # its children were being removed, the link is unlinked, not its target emptied.
    $again = New-Object IO.DirectoryInfo $Path
    if ($again.Exists) {
        if ($again.Attributes -band [IO.FileAttributes]::ReadOnly) { $again.Attributes = $again.Attributes -band -bnot [IO.FileAttributes]::ReadOnly }
        [IO.Directory]::Delete($Path, $false)
    }
}

function Remove-TreeWithinAllowedRoots {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string[]]$AllowedRoots,
        [string]$Purpose = 'removal'
    )
    if (-not [string]::IsNullOrWhiteSpace($Path)) {
        $candidate = [IO.Path]::GetFullPath($Path).TrimEnd('\')
        if ([IO.Path]::GetPathRoot($candidate).TrimEnd('\').Equals($candidate, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing $Purpose of a volume root: $candidate"
        }
    }
    $full = Assert-RemovalWithinAllowedRoots -Path $Path -AllowedRoots $AllowedRoots -Purpose $Purpose
    if (-not (Test-Path -LiteralPath $full)) { return }
    Remove-EntryWithoutFollowing -Path $full
}
