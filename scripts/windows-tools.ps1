# Item 2gc (v1.1.1/W1): resolve Windows' own utilities by their absolute path,
# never by bare name.
#
# WALK-3 found the cost of a bare name: `manage-signing.ps1` ran
# `& whoami.exe /groups`, and a harness started from a shell whose PATH reaches
# Git's `usr/bin` first got the POSIX `whoami`, which rejects `/groups`. The
# signing status then answered HTTP 500 instead of a status the operator can
# read. A name on PATH is whatever the PATH says it is; System32 is where these
# utilities actually live.
#
# Developer toolchains (git, node, go) are a different matter and stay
# PATH-resolved on purpose: the operator chooses which one is on the path.

function Get-WindowsTool {
    <#
        .SYNOPSIS
        The absolute path of a utility that ships in System32.
    #>
    param([Parameter(Mandatory = $true)][string]$Name)
    $system32 = Join-Path ([Environment]::GetFolderPath('System')) $Name
    if (Test-Path -LiteralPath $system32) { return $system32 }
    # A 32-bit host on a 64-bit Windows sees System32 redirected; Sysnative is
    # the un-redirected view.
    $sysnative = Join-Path (Join-Path $env:SystemRoot 'Sysnative') $Name
    if (Test-Path -LiteralPath $sysnative) { return $sysnative }
    throw "$Name is not in System32; this host cannot run it by absolute path."
}

function Get-WindowsPowerShell {
    <#
        .SYNOPSIS
        Windows PowerShell 5.1 by absolute path, for scripts that launch a
        child host. It is the host these scripts are written for; PowerShell 7
        is chosen deliberately where it is wanted, never by PATH accident.
    #>
    $powershell = Join-Path ([Environment]::GetFolderPath('System')) 'WindowsPowerShell\v1.0\powershell.exe'
    if (Test-Path -LiteralPath $powershell) { return $powershell }
    $sysnative = Join-Path $env:SystemRoot 'Sysnative\WindowsPowerShell\v1.0\powershell.exe'
    if (Test-Path -LiteralPath $sysnative) { return $sysnative }
    throw 'Windows PowerShell is not in System32; this host cannot run it by absolute path.'
}
