[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'removal-guard.ps1')

$disposable = Join-Path ([IO.Path]::GetTempPath()) ('Agent_b-removal-guard-' + [Guid]::NewGuid().ToString('N'))
$allowed = Join-Path $disposable 'child.txt'
if ((Assert-RemovalWithinAllowedRoots -Path $allowed -AllowedRoots @($disposable) -Purpose 'test cleanup') -ne [IO.Path]::GetFullPath($allowed)) {
    throw 'Allowed disposable child did not resolve to its full path.'
}

$outside = Join-Path $env:ProgramFiles 'PowerShell\7'
$refused = $false
try {
    $null = Assert-RemovalWithinAllowedRoots -Path $outside -AllowedRoots @($disposable) -Purpose 'test cleanup'
} catch {
    $refused = $_.Exception.Message -eq "Refusing test cleanup outside allowed removal roots: $([IO.Path]::GetFullPath($outside))"
}
if (-not $refused) { throw "Outside removal path was not refused: $outside" }
Write-Host 'PASS: removal guard admits a disposable child and refuses Program Files outside the allow-list'

function Assert-Refused([scriptblock]$Action, [string]$Pattern, [string]$Label) {
    $message = ''
    try { & $Action } catch { $message = $_.Exception.Message }
    if ($message -notlike $Pattern) { throw "$Label was not refused (got: '$message')" }
}

$target = Join-Path $disposable 'target'
$keep = Join-Path $target 'keep.txt'
try {
    $null = New-Item -ItemType Directory -Path $target -Force
    [IO.File]::WriteAllText($keep, 'keep')

    # A tree removal outside its allow-list is refused and removes nothing.
    Assert-Refused { Remove-TreeWithinAllowedRoots -Path $target -AllowedRoots @((Join-Path $disposable 'other')) -Purpose 'test cleanup' } 'Refusing test cleanup outside allowed removal roots:*' 'Tree removal outside the allow-list'
    if (-not (Test-Path -LiteralPath $keep)) { throw 'A refused tree removal deleted its target.' }
    $volume = [IO.Path]::GetPathRoot($disposable)
    Assert-Refused { Remove-TreeWithinAllowedRoots -Path $volume -AllowedRoots @($volume) -Purpose 'test cleanup' } 'Refusing test cleanup of a volume root:*' 'Volume-root removal'
    Write-Host 'PASS: tree removal refuses a path outside its allow-list and a volume root'

    # v0.64.0/W8: a name that looks like a variable is removed as named, never expanded.
    $literal = Join-Path $disposable '%USERPROFILE%'
    $null = New-Item -ItemType Directory -Path $literal -Force
    $resolved = Assert-RemovalWithinAllowedRoots -Path $literal -AllowedRoots @($disposable) -Purpose 'test cleanup'
    if ($resolved -ne [IO.Path]::GetFullPath($literal).TrimEnd('\')) { throw "A variable-looking name was expanded: $resolved" }
    Remove-TreeWithinAllowedRoots -Path $literal -AllowedRoots @($disposable) -Purpose 'test cleanup'
    if (Test-Path -LiteralPath $literal) { throw 'The literal variable-looking directory was not removed.' }
    Write-Host 'PASS: a variable-looking name is removed literally, not expanded'

    # v0.65.0/W9 (1): an allowed root that is the target itself is no allow-list.
    $selfRooted = Join-Path $disposable 'self-rooted'
    $null = New-Item -ItemType Directory -Path $selfRooted -Force
    Assert-Refused { Remove-TreeWithinAllowedRoots -Path $selfRooted -AllowedRoots @($selfRooted) -Purpose 'test cleanup' } 'Refusing test cleanup: an allowed removal root must contain the target, not be it:*' 'Self-rooted removal'
    if (-not (Test-Path -LiteralPath $selfRooted)) { throw 'A refused self-rooted removal deleted its target.' }
    Write-Host 'PASS: an allowed root equal to its target is refused'

    # v0.65.0/W9 (2): a junction in a parent redirects the removal into its target.
    $realParent = Join-Path $disposable 'real-parent'
    $realWorkspace = Join-Path $realParent 'workspace'
    $null = New-Item -ItemType Directory -Path $realWorkspace -Force
    [IO.File]::WriteAllText((Join-Path $realWorkspace 'operator-work.txt'), 'keep')
    $linkedParent = Join-Path $disposable 'linked-parent'
    $null = New-Item -ItemType Junction -Path $linkedParent -Target $realParent
    Assert-Refused { Remove-TreeWithinAllowedRoots -Path (Join-Path $linkedParent 'workspace') -AllowedRoots @($disposable) -Purpose 'test cleanup' } 'Refusing test cleanup beneath a junction or link:*' 'Removal beneath a parent junction'
    if (-not (Test-Path -LiteralPath (Join-Path $realWorkspace 'operator-work.txt'))) { throw 'A removal beneath a parent junction emptied its target.' }
    [IO.Directory]::Delete($linkedParent, $false)
    Write-Host 'PASS: a removal beneath a parent junction is refused and its target is intact'

    # v0.65.0/W9 (3): a junction that appears after the removal began is unlinked,
    # not followed. The seam replaces a directory with a junction just before the
    # walk enters it.
    $racing = Join-Path $disposable 'racing'
    $null = New-Item -ItemType Directory -Path (Join-Path $racing 'swap\inner') -Force
    $raceTarget = Join-Path $disposable 'race-target'
    $null = New-Item -ItemType Directory -Path $raceTarget -Force
    [IO.File]::WriteAllText((Join-Path $raceTarget 'keep.txt'), 'keep')
    $global:AgentbRemovalBeforeDescend = {
        param($entered)
        if ((Split-Path -Leaf $entered) -eq 'swap' -and -not ((Get-Item -LiteralPath $entered -Force).Attributes -band [IO.FileAttributes]::ReparsePoint)) {
            [IO.Directory]::Delete($entered, $true)
            $null = New-Item -ItemType Junction -Path $entered -Target $raceTarget
        }
    }
    try { Remove-TreeWithinAllowedRoots -Path $racing -AllowedRoots @($disposable) -Purpose 'test cleanup' }
    finally { Remove-Variable -Name AgentbRemovalBeforeDescend -Scope Global -ErrorAction SilentlyContinue }
    if ((Test-Path -LiteralPath $racing) -or -not (Test-Path -LiteralPath (Join-Path $raceTarget 'keep.txt'))) { throw 'A junction created during the removal was followed.' }
    Write-Host 'PASS: a junction created after the removal began is unlinked and its target is intact'

    # v0.65.0/W15 cold review: a directory ABOVE the entry in hand swapped for a
    # junction while the walk is inside it. The walk must stop, not follow it.
    $ancestorRace = Join-Path $disposable 'ancestor-race'
    $null = New-Item -ItemType Directory -Path (Join-Path $ancestorRace 'a\b') -Force
    [IO.File]::WriteAllText((Join-Path $ancestorRace 'a\b\own.txt'), 'own')
    $victim = Join-Path $disposable 'ancestor-victim'
    $null = New-Item -ItemType Directory -Path (Join-Path $victim 'b') -Force
    [IO.File]::WriteAllText((Join-Path $victim 'b\secret.txt'), 'keep')
    $global:AgentbRemovalBeforeDescend = {
        param($entered)
        if ((Split-Path -Leaf $entered) -eq 'b' -and (Split-Path -Leaf (Split-Path -Parent $entered)) -eq 'a' -and (Test-Path -LiteralPath (Join-Path $ancestorRace 'a\b\own.txt'))) {
            [IO.Directory]::Move((Join-Path $ancestorRace 'a'), (Join-Path $ancestorRace 'a-moved'))
            $null = New-Item -ItemType Junction -Path (Join-Path $ancestorRace 'a') -Target $victim
        }
    }.GetNewClosure()
    $ancestorMessage = ''
    try { Remove-TreeWithinAllowedRoots -Path $ancestorRace -AllowedRoots @($disposable) -Purpose 'test cleanup' }
    catch { $ancestorMessage = $_.Exception.Message }
    finally { Remove-Variable -Name AgentbRemovalBeforeDescend -Scope Global -ErrorAction SilentlyContinue }
    if (-not (Test-Path -LiteralPath (Join-Path $victim 'b\secret.txt'))) { throw 'A junction swapped in above the walk was followed and its target emptied.' }
    if ($ancestorMessage -notlike '*became a junction*') { throw "The ancestor swap was not refused (got: '$ancestorMessage')" }
    if (Test-Path -LiteralPath (Join-Path $ancestorRace 'a')) { [IO.Directory]::Delete((Join-Path $ancestorRace 'a'), $false) }
    Write-Host 'PASS: a junction swapped in above the entry in hand stops the removal and its target is intact'

    # A junction inside a removed tree is unlinked, never descended into.
    $tree = Join-Path $disposable 'tree'
    $null = New-Item -ItemType Directory -Path (Join-Path $tree 'a\b') -Force
    $null = New-Item -ItemType Junction -Path (Join-Path $tree 'a\b\node_modules') -Target $target
    Remove-TreeWithinAllowedRoots -Path $tree -AllowedRoots @($disposable) -Purpose 'test cleanup'
    if ((Test-Path -LiteralPath $tree) -or -not (Test-Path -LiteralPath $keep)) { throw 'Tree removal did not keep the junction target intact.' }
    Write-Host 'PASS: tree removal unlinks a nested junction and leaves its target intact'

    # The v0.64.0/W1 incident: a linked worktree holding a junction.
    $git = (Get-Command git.exe -ErrorAction Stop).Source
    $repository = Join-Path $disposable 'repo'
    $worktree = Join-Path $disposable 'worktree'
    & $git init -q $repository
    & $git -C $repository -c user.name=guard -c user.email=guard@example.invalid commit -q --allow-empty -m guard
    & $git -C $repository worktree add -q $worktree HEAD 2>$null
    if ($LASTEXITCODE -ne 0) { throw 'could not create the disposable worktree' }
    $null = New-Item -ItemType Junction -Path (Join-Path $worktree 'node_modules') -Target $target
    $remover = Join-Path $PSScriptRoot 'remove-worktree.ps1'
    Assert-Refused { & $remover -Path $repository -Repository $repository } 'Refusing to remove the main worktree:*' 'Main worktree removal'
    Assert-Refused { & $remover -Path $target -Repository $repository } 'Refusing to remove a path that is not a linked worktree*' 'Non-worktree removal'
    & $remover -Path $worktree -Repository $repository | Out-Null
    if ((Test-Path -LiteralPath $worktree) -or -not (Test-Path -LiteralPath $keep)) { throw 'Worktree removal emptied a junction target.' }
    Write-Host 'PASS: remove-worktree refuses the main worktree and a non-worktree, and keeps a junction target intact'

    # v0.65.0/W15 cold review: a worktree record edited to name another folder.
    $forged = Join-Path $disposable 'forged-worktree'
    & $git -C $repository worktree add -q $forged HEAD 2>$null
    if ($LASTEXITCODE -ne 0) { throw 'could not create the forged-record worktree' }
    $documents = Join-Path $disposable 'operator-documents'
    $null = New-Item -ItemType Directory -Path $documents -Force
    [IO.File]::WriteAllText((Join-Path $documents 'report.docx'), 'keep')
    $forgedRecord = Join-Path $repository '.git\worktrees\forged-worktree\gitdir'
    [IO.File]::WriteAllText($forgedRecord, ((Join-Path $documents '.git').Replace('\', '/') + "`n"))
    Assert-Refused { & $remover -Path $documents -Repository $repository } 'Refusing: * has no .git file linking it to *' 'Removal of a folder named by a forged worktree record'
    if (-not (Test-Path -LiteralPath (Join-Path $documents 'report.docx'))) { throw 'A forged worktree record led to deleting the folder it named.' }
    Write-Host 'PASS: remove-worktree refuses a folder that a forged worktree record names'
} finally {
    if (Test-Path -LiteralPath $disposable) { Remove-TreeWithinAllowedRoots -Path $disposable -AllowedRoots @([IO.Path]::GetTempPath()) -Purpose 'removal-guard test cleanup' }
}
