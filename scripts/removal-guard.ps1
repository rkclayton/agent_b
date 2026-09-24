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
# v0.65.0/W15 cold review: re-reading only the entry in hand is not enough. A
# directory above it (inside the tree) renamed and replaced by a junction would
# lead the walk into the junction's target. Every entry therefore proves that
# each directory between it and the removal root is still a real directory
# before anything is removed or entered.
function Assert-NoLinkAbove {
    param([Parameter(Mandatory = $true)][string]$Path, [Parameter(Mandatory = $true)][string]$Root)
    $ancestor = Split-Path -Parent $Path
    while ($ancestor -and $ancestor.Length -ge $Root.Length) {
        $directory = New-Object IO.DirectoryInfo $ancestor
        if (-not $directory.Exists -or ($directory.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
            throw "Refusing removal: $ancestor became a junction, a link or went missing while $Root was being removed"
        }
        if ($ancestor.Equals($Root, [StringComparison]::OrdinalIgnoreCase)) { return }
        $ancestor = Split-Path -Parent $ancestor
    }
}

function Remove-EntryWithoutFollowing {
    param([Parameter(Mandatory = $true)][string]$Path, [string]$Root = $Path)
    if (-not $Path.Equals($Root, [StringComparison]::OrdinalIgnoreCase)) { Assert-NoLinkAbove -Path $Path -Root $Root }
    $info = New-Object IO.DirectoryInfo $Path
    if (-not $info.Exists) {
        $file = New-Object IO.FileInfo $Path
        if ($file.Exists) {
            # A file link is deleted as a link; its target's attributes are never touched.
            if (-not ($file.Attributes -band [IO.FileAttributes]::ReparsePoint) -and ($file.Attributes -band [IO.FileAttributes]::ReadOnly)) {
                $file.Attributes = $file.Attributes -band -bnot [IO.FileAttributes]::ReadOnly
            }
            # Real-time scanners can briefly retain a just-copied script after
            # the owning process exits. Keep the guarded target and link checks
            # unchanged, but give that transient handle a bounded chance to
            # close; a persistent failure still stops the removal.
            for ($attempt = 0; $attempt -lt 20; $attempt++) {
                try {
                    [IO.File]::Delete($Path)
                    break
                } catch {
                    if ($attempt -eq 19) { throw }
                    Start-Sleep -Milliseconds 100
                }
            }
        }
        return
    }
    if ($info.Attributes -band [IO.FileAttributes]::ReparsePoint) {
        [IO.Directory]::Delete($Path, $false)
        return
    }
    # Test seam (tests/test-removal-guard.ps1): something replacing a directory
    # with a junction just before it is entered.
    if ($global:AgentbRemovalBeforeDescend -is [scriptblock]) { & $global:AgentbRemovalBeforeDescend $Path }
    if (-not $Path.Equals($Root, [StringComparison]::OrdinalIgnoreCase)) { Assert-NoLinkAbove -Path $Path -Root $Root }
    $info = New-Object IO.DirectoryInfo $Path
    if ($info.Exists -and ($info.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
        [IO.Directory]::Delete($Path, $false)
        return
    }
    if (-not $info.Exists) { return }
    foreach ($child in $info.GetFileSystemInfos()) {
        Remove-EntryWithoutFollowing -Path $child.FullName -Root $Root
    }
    # The directory is read again before it is removed: if it became a link while
    # its children were being removed, the link is unlinked, not its target emptied.
    if (-not $Path.Equals($Root, [StringComparison]::OrdinalIgnoreCase)) { Assert-NoLinkAbove -Path $Path -Root $Root }
    $again = New-Object IO.DirectoryInfo $Path
    if ($again.Exists) {
        if ($again.Attributes -band [IO.FileAttributes]::ReparsePoint) { [IO.Directory]::Delete($Path, $false); return }
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
    Remove-EntryWithoutFollowing -Path $full -Root $full
}
