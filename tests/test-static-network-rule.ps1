#requires -Version 5.1
<#
Item 2nx: THE SERVICE IDENTITY'S OUTBOUND RULE KNOWS NOTHING ABOUT MODEL CONNECTIONS.

It applies nothing. Every case here runs the script in -Inspect or -WhatIf, which read and
report; the live Apply is the operator's, and it is the one thing that needs elevation.

The failure this comes from is the operator's own, 2026-09-28 23:53: Repair reported
"network policy NOT applied — apply-hardening.ps1 exited 1" because the model host could
not be resolved while he was off the network that resolves it. The model is reached by
Agent_b, which runs as him; the service identity never speaks to it.
#>
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$firewall = Join-Path $root 'scripts\apply-firewall-rule.ps1'
$hardening = Join-Path $root 'scripts\apply-hardening.ps1'
$failures = @()
function Check([string]$name, [scriptblock]$body) {
    try {
        & $body
        Write-Host "PASS $name"
    } catch {
        $script:failures += "$name — $($_.Exception.Message)"
        Write-Host "FAIL $name — $($_.Exception.Message)"
    }
}

# (a) The scripts take no model address or port at all: a parameter that does not exist
# cannot be resolved, and cannot fail an apply because a name is unreachable.
Check 'the firewall script has no model parameters' {
    $parameters = (Get-Command $firewall).Parameters.Keys
    foreach ($name in @('ModelAddress', 'ModelPort')) {
        if ($parameters -contains $name) { throw "apply-firewall-rule.ps1 still takes -$name" }
    }
}
Check 'the hardening script has no model parameters' {
    $parameters = (Get-Command $hardening).Parameters.Keys
    foreach ($name in @('ModelAddress', 'ModelPort')) {
        if ($parameters -contains $name) { throw "apply-hardening.ps1 still takes -$name" }
    }
}

# (a) and (c): nothing in either script resolves a name or reports drift about one.
Check 'neither script resolves a host name or reports model drift' {
    foreach ($script in @($firewall, $hardening)) {
        $text = Get-Content -LiteralPath $script -Raw
        foreach ($forbidden in @('GetHostAddresses', 'Resolve-ModelAddresses', 'resolution_changed', 'resolutionChanged', 'ModelAddress', 'ModelPort')) {
            if ($text -match [regex]::Escape($forbidden)) { throw "$(Split-Path -Leaf $script) still carries $forbidden" }
        }
    }
}

# The acceptance's first line: an unresolvable model host used to fail the whole step.
# There is no model host any more, so -Inspect answers with the policy and exits 0.
Check 'inspect succeeds with no model host in the picture' {
    $output = & $firewall -AccountName 'agentb-svc' -RuleName 'AgentB-Svc-2nx-Inspect' -Inspect 6>&1 2>&1
    if ($LASTEXITCODE -ne 0) { throw "inspect exited $LASTEXITCODE`n$($output -join "`n")" }
    $text = $output -join "`n"
    if ($text -match 'could not be resolved') { throw "inspect still resolves a host: $text" }
    if ($text -match 'model server' -or $text -match 'Model endpoint') { throw "inspect still describes a model exemption: $text" }
}

# (b) The legacy Allow rule is still named, because an install that carries one must lose
# it on the next Apply.
Check 'the legacy model-allow rule is still removed' {
    $text = Get-Content -LiteralPath $firewall -Raw
    if ($text -notmatch 'AgentB-Svc-Model-Allow') { throw 'the legacy Allow rule is no longer named, so an old install would keep it' }
}

# (a) keeps the operator's own exceptions: they are his choices, not a connection's.
Check 'the operator exceptions survive' {
    $parameters = (Get-Command $firewall).Parameters.Keys
    foreach ($name in @('AllowLocalNetwork', 'LocalSubnet', 'AllowedRange')) {
        if ($parameters -notcontains $name) { throw "apply-firewall-rule.ps1 lost -$name" }
    }
}

# (d) The cause leads the message that reaches Settings; the exit code follows it.
Check 'a failing step names its cause first' {
    $text = Get-Content -LiteralPath $hardening -Raw
    if ($text -match 'throw "\$\(Split-Path -Leaf \$Path\) exited') { throw 'the failure message still leads with the script name and exit code' }
    if ($text -notmatch 'causeText') { throw "the failure message does not lead with the child's own last line" }
}

if ($failures.Count -gt 0) {
    Write-Host "STATIC NETWORK RULE: $($failures.Count) failure(s)"
    exit 1
}
Write-Host 'STATIC NETWORK RULE: all cases passed'
