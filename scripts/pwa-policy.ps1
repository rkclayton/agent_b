# Item 2gl (v1.3.0/W3) - REMOVAL ONLY. This file used to write an Edge
# WebAppInstallForceList entry so Edge would install the app with no click, and
# the launcher opened the installed app, because an installed PWA was the last
# place the Window Controls Overlay might have activated and given us the
# window's top edge.
#
# The operator installed v1.2.7 elevated on 2026-09-21 and measured it: the
# overlay does not activate on a policy-installed app either. So the entry
# bought nothing, and a machine-wide policy that buys nothing is not worth
# writing. The write is gone, and so is the app-id lookup that only existed to
# open what the write installed.
#
# WHAT REMAINS, AND WHY IT MUST: a v1.2.7 install may have written an entry on
# this machine. Uninstall has to take it away, and it has to take away exactly
# the value that install wrote and no neighbour's - another vendor's forced
# install is not ours to delete. That is the whole of this file now.
#
# DELIBERATELY NO Set-StrictMode. This is dot-sourced into the uninstaller, and
# strict mode is dynamically scoped: a v1.2.7 draft set it here and broke the
# INSTALL outright, because the installer reads a global that is not always set.
# A library dot-sourced into somebody else's script does not get to change the
# language that script is written in.

$script:AgentBPwaPolicyRoot = 'HKLM:\SOFTWARE\Policies\Microsoft\Edge\WebAppInstallForceList'

function Test-AgentBPwaUrl {
    # Loopback only, http only, and a path of our own. A url that is not ours
    # is never matched and therefore never removed.
    param([string]$Url)
    if (-not $Url) { return $false }
    $parsed = $null
    if (-not [Uri]::TryCreate($Url, [UriKind]::Absolute, [ref]$parsed)) { return $false }
    if ($parsed.Scheme -ne 'http') { return $false }
    if ($parsed.Host -ne '127.0.0.1') { return $false }
    if ($parsed.AbsolutePath -ne '/chat') { return $false }
    return $true
}

function Get-AgentBPwaPolicyValues {
    param([string]$PolicyRoot = $script:AgentBPwaPolicyRoot)
    $values = @{}
    if (-not (Test-Path -LiteralPath $PolicyRoot)) { return $values }
    $key = Get-Item -LiteralPath $PolicyRoot
    foreach ($name in $key.GetValueNames()) {
        if ($name) { $values[$name] = [string]$key.GetValue($name) }
    }
    return $values
}

function Find-AgentBPwaPolicyName {
    # The value holding OUR url, if any. Other software and the operator's own
    # management may own other indices; we never touch them.
    param([Parameter(Mandatory = $true)][string]$Url, [string]$PolicyRoot = $script:AgentBPwaPolicyRoot)
    if (-not (Test-AgentBPwaUrl $Url)) { return $null }
    foreach ($pair in (Get-AgentBPwaPolicyValues -PolicyRoot $PolicyRoot).GetEnumerator()) {
        if ($pair.Value -and $pair.Value.Contains('"' + $Url + '"')) { return $pair.Key }
    }
    return $null
}

function Remove-AgentBPwaPolicy {
    # Removes only the value that holds our url. Returns the name removed, or
    # $null when there was nothing of ours to remove.
    param(
        [Parameter(Mandatory = $true)][string]$Url,
        [string]$PolicyRoot = $script:AgentBPwaPolicyRoot
    )
    $name = Find-AgentBPwaPolicyName -Url $Url -PolicyRoot $PolicyRoot
    if (-not $name) { return $null }
    Remove-ItemProperty -LiteralPath $PolicyRoot -Name $name -Force
    # Only if we left it empty, and only the leaf: the parent Edge policy key
    # belongs to the operator's machine, not to us.
    if ((Get-AgentBPwaPolicyValues -PolicyRoot $PolicyRoot).Count -eq 0) {
        Remove-Item -LiteralPath $PolicyRoot -Force -ErrorAction SilentlyContinue
    }
    return $name
}
