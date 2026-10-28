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

# Item 2na: ONE HELPER, NOT A FLAG PER SITE.
#
# The operator works on this machine while the suite runs. Every child a gate starts
# used to take whatever window the platform gave it: a console for each pwsh, a real
# window for the product and the installer. Thirteen scripts started processes and
# eleven of them had remembered `-WindowStyle Hidden`; the point of a helper is that
# the fourteenth cannot forget.
#
# Start-Quiet starts a process WITHOUT a window and WITHOUT activation and returns it,
# so a gate can wait on it and stop it. Start-QuietMinimized is for the one case a
# window is genuinely needed — the product under a window probe — and shows it
# minimized and non-activating, so it never comes to the front of the operator's
# screen. Neither ever calls SetForegroundWindow or AppActivate, and nothing here
# should.

function Start-Quiet {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$FilePath,
        [string[]]$ArgumentList = @(),
        [string]$WorkingDirectory,
        # Redirection is opt-in: a gate that needs the child's output names files for
        # it, because a hidden child's console is not there to read.
        [string]$StandardOutput,
        [string]$StandardError
    )
    $parameters = @{ FilePath = $FilePath; PassThru = $true; WindowStyle = 'Hidden' }
    if ($ArgumentList.Count) { $parameters.ArgumentList = $ArgumentList }
    if ($WorkingDirectory) { $parameters.WorkingDirectory = $WorkingDirectory }
    if ($StandardOutput) { $parameters.RedirectStandardOutput = $StandardOutput }
    if ($StandardError) { $parameters.RedirectStandardError = $StandardError }
    return Start-Process @parameters
}

function Start-QuietMinimized {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$FilePath,
        [string[]]$ArgumentList = @(),
        [string]$WorkingDirectory
    )
    # MinimizedNoActivate is Start-Process's own SW_SHOWMINNOACTIVE: the window
    # exists, so a probe that needs one has one, and the operator's focus is left
    # where it was.
    $parameters = @{ FilePath = $FilePath; PassThru = $true; WindowStyle = 'Minimized' }
    if ($ArgumentList.Count) { $parameters.ArgumentList = $ArgumentList }
    if ($WorkingDirectory) { $parameters.WorkingDirectory = $WorkingDirectory }
    return Start-Process @parameters
}

# Item 2x4 (c): Defender's machine-learning check can quarantine a freshly
# test-signed Agent_b binary in a disposable root. That is named, with the one
# exclusion the operator can add, instead of reading as an install or signing
# failure. Only he adds it; nothing here changes Defender. A file that was there
# and is gone, Defender's own refusal text, or its detection event on an
# Agent_b- disposable path since the run began is the evidence.
function Get-DefenderQuarantineLine {
    [CmdletBinding()]
    param(
        [string[]]$Text = @(),
        [string]$Path = '',
        [datetime]$Since = [datetime]::MinValue
    )
    $evidence = ''
    if ($Path -and -not (Test-Path -LiteralPath $Path)) { $evidence = "$(Split-Path -Leaf $Path) is gone" }
    if (-not $evidence -and ($Text -join "`n") -match 'contains a virus or potentially unwanted software') { $evidence = 'Defender refused to open a disposable file' }
    if (-not $evidence -and $Since -ne [datetime]::MinValue) {
        try {
            $detection = Get-WinEvent -FilterHashtable @{ LogName = 'Microsoft-Windows-Windows Defender/Operational'; Id = 1116, 1117; StartTime = $Since } -MaxEvents 20 -ErrorAction Stop |
                Where-Object { $_.Message -like '*\Agent_b-*' } | Select-Object -First 1
            if ($detection) { $evidence = "Defender event $($detection.Id): $(([regex]::Match($detection.Message, 'Name:\s*(\S+)')).Groups[1].Value)" }
        } catch {}
    }
    if (-not $evidence) { return '' }
    return 'signing: quarantined by Defender (' + $evidence + '); to let the disposable runs through, add once, elevated: Add-MpPreference -ExclusionPath "$env:TEMP\Agent_b-*"'
}
