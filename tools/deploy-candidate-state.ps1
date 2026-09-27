function Get-StagedCandidateState {
    param([Parameter(Mandatory = $true)][string]$Candidate)
    $root = [IO.Path]::GetFullPath($Candidate)
    $manifestPath = Join-Path $root 'candidate-final.json'
    $tag = ''
    $commit = ''
    if (Test-Path -LiteralPath $manifestPath -PathType Leaf) {
        try {
            $manifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json
            $tag = [string]$manifest.tag
            $commit = [string]$manifest.commit
        } catch {}
    }
    $signatures = @()
    foreach ($name in @('Agent_b.exe', 'Agent_b-setup.exe')) {
        $path = Join-Path $root $name
        $status = if (Test-Path -LiteralPath $path -PathType Leaf) { [string](Get-AuthenticodeSignature -LiteralPath $path).Status } else { 'missing' }
        $signatures += "$name=$status"
    }
    return [pscustomobject]@{ Tag = $tag; Commit = $commit; SignedState = ($signatures -join ', ') }
}

function Remove-MatchingStagedCandidate {
    param(
        [Parameter(Mandatory = $true)][string]$Candidate,
        [Parameter(Mandatory = $true)][string]$CandidatesRoot,
        [Parameter(Mandatory = $true)][string]$ExpectedTag
    )
    $state = Get-StagedCandidateState -Candidate $Candidate
    $shownTag = if ($state.Tag) { $state.Tag } else { '<unknown>' }
    $shownCommit = if ($state.Commit) { $state.Commit } else { '<unknown>' }
    Write-Host "STALE CANDIDATE: path=$([IO.Path]::GetFullPath($Candidate)) tag=$shownTag commit=$shownCommit signed-state=$($state.SignedState)"
    if ($state.Tag -cne $ExpectedTag) {
        # rel-1.27.0/W4: the recovery command named THIS file, which does not declare
        # the guard function, so pasting it failed with "not recognized as a name of a
        # cmdlet". A recovery command that does not run is the dead end card 3 was
        # written to remove.
        $guardScript = [IO.Path]::GetFullPath((Join-Path (Split-Path -Parent (Split-Path -Parent $PSCommandPath)) 'scripts\removal-guard.ps1'))
        # rel-1.25.0 card 3: a deploy that fails mid-staging -- as one did at
        # rel-1.25.0 when the timestamp server timed out -- leaves a candidate
        # directory with copied sources, no manifest and no binaries. This guard
        # refuses to delete what it cannot identify, which is right, and the next
        # deploy then refuses too. Naming the recovery here is the difference
        # between a correct guard and a dead end.
        throw ("DEPLOY REFUSED: staged candidate does not identify itself as $ExpectedTag; it was retained. " +
               "If this is a partial candidate from a failed deploy (no candidate-final.json, no Agent_b.exe), remove it through the removal guard and deploy again:`n" +
               "  pwsh -NoProfile -Command "". '$guardScript'; Remove-TreeWithinAllowedRoots -Path '$Candidate' -AllowedRoots @('$CandidatesRoot') -Purpose 'partial candidate removal'""")
    }
    Remove-TreeWithinAllowedRoots -Path $Candidate -AllowedRoots @($CandidatesRoot) -Purpose "restage $ExpectedTag"
    Write-Host "STALE CANDIDATE REMOVED: $ExpectedTag"
}
