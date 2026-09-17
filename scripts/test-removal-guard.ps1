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

    # A junction inside a removed tree is unlinked, never descended into.
    $tree = Join-Path $disposable 'tree'
    $null = New-Item -ItemType Directory -Path (Join-Path $tree 'a\b') -Force
    $null = New-Item -ItemType Junction -Path (Join-Path $tree 'a\b\node_modules') -Target $target
    Remove-TreeWithinAllowedRoots -Path $tree -AllowedRoots @($tree) -Purpose 'test cleanup'
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
} finally {
    if (Test-Path -LiteralPath $disposable) { Remove-TreeWithinAllowedRoots -Path $disposable -AllowedRoots @($disposable) -Purpose 'removal-guard test cleanup' }
}
