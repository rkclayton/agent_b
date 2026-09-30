[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'High')]
param(
    [ValidatePattern('^[A-Za-z0-9._-]+$')]
    [string]$AccountName = 'agentb-svc',
    [ValidatePattern('^[A-Za-z0-9._-]+$')]
    [string]$RuleName = 'AgentB-Svc-Outbound-Block',
    [ValidatePattern('^[A-Za-z0-9._-]+$')]
    [string]$LegacyAllowRuleName = 'AgentB-Svc-Model-Allow',
    [ValidatePattern('^[A-Za-z0-9._-]+$')]
    [string]$LANICMPRuleName = 'AgentB-Svc-LAN-ICMP-Allow',
    [switch]$AllowLocalNetwork,
    [string[]]$LocalSubnet = @(),
    [string[]]$AllowedRange = @(),
    [switch]$Verify,
    [switch]$Remove,
	[switch]$Inspect,
	[switch]$NoPrompt
)

$ErrorActionPreference = 'Stop'
$LocalSubnet = @(($LocalSubnet -join ',') -split ',' | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
$AllowedRange = @(($AllowedRange -join ',') -split ',' | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
$ruleName = $RuleName
$legacyAllowRuleName = $LegacyAllowRuleName
$statusMarker = 'AGENTB_FIREWALL_STATUS='
$metadataAddress = [Net.IPAddress]::Parse('169.254.169.254')

function ConvertTo-IPv4Number {
    param([Net.IPAddress]$Address)
    $bytes = $Address.GetAddressBytes()
    if ($bytes.Length -ne 4) { throw "Only IPv4 LAN subnets are supported: $Address" }
    return ([uint64]$bytes[0] * 16777216) + ([uint64]$bytes[1] * 65536) + ([uint64]$bytes[2] * 256) + [uint64]$bytes[3]
}

function ConvertFrom-IPv4Number {
    param([uint64]$Value)
    return "$([math]::Floor($Value / 16777216) % 256).$([math]::Floor($Value / 65536) % 256).$([math]::Floor($Value / 256) % 256).$($Value % 256)"
}

function Resolve-LANRange {
    param([string]$Prefix)
    if ($Prefix -notmatch '^([^/]+)/([0-9]{1,2})$') { throw "LocalSubnet must be an IPv4 CIDR prefix: '$Prefix'" }
    $address = [Net.IPAddress]::Parse($Matches[1])
    $bits = [int]$Matches[2]
    if ($bits -lt 8 -or $bits -gt 32) { throw "LocalSubnet prefix length must be between 8 and 32: '$Prefix'" }
    $value = ConvertTo-IPv4Number $address
    $size = [math]::Pow(2, 32 - $bits)
    $start = [uint64]([math]::Floor($value / $size) * $size)
    $end = [uint64]($start + $size - 1)
    $first = [byte]([math]::Floor($start / 16777216) % 256)
    $second = [byte]([math]::Floor($start / 65536) % 256)
    $private = $first -eq 10 -or ($first -eq 172 -and $second -ge 16 -and $second -le 31) -or ($first -eq 192 -and $second -eq 168)
    if (-not $private) { throw "LocalSubnet must be RFC1918 private space: '$Prefix'" }
    $metadata = ConvertTo-IPv4Number $metadataAddress
    if ($start -le $metadata -and $end -ge $metadata) { throw "LocalSubnet may not include the metadata address: '$Prefix'" }
    return [pscustomobject]@{ Start = $start; End = $end; Prefix = "$(ConvertFrom-IPv4Number $start)/$bits" }
}

function Resolve-ConfiguredRange {
    param([string]$Prefix)
    if ($Prefix -notmatch '^([^/]+)/([0-9]{1,2})$') { throw "AllowedRange must be an IPv4 CIDR prefix: '$Prefix'" }
    $address = [Net.IPAddress]::Parse($Matches[1])
    $bits = [int]$Matches[2]
    if ($bits -lt 8 -or $bits -gt 32) { throw "AllowedRange prefix length must be between 8 and 32: '$Prefix'" }
    $value = ConvertTo-IPv4Number $address
    $size = [math]::Pow(2, 32 - $bits)
    $start = [uint64]([math]::Floor($value / $size) * $size)
    $end = [uint64]($start + $size - 1)
    $metadata = ConvertTo-IPv4Number $metadataAddress
    if ($start -le $metadata -and $end -ge $metadata) { throw "AllowedRange may not include the metadata address: '$Prefix'" }
    return [pscustomobject]@{ Start = $start; End = $end; Prefix = "$(ConvertFrom-IPv4Number $start)/$bits" }
}

function Resolve-BlockedRanges {
    param([object[]]$Allowed)
    $ranges = @([pscustomobject]@{ Start = [uint64]0; End = [uint64]4294967295 })
    foreach ($allow in $Allowed) {
        $next = @()
        foreach ($range in $ranges) {
            if ($allow.End -lt $range.Start -or $allow.Start -gt $range.End) { $next += $range; continue }
            if ($allow.Start -gt $range.Start) { $next += [pscustomobject]@{ Start = $range.Start; End = [uint64]($allow.Start - 1) } }
            if ($allow.End -lt $range.End) { $next += [pscustomobject]@{ Start = [uint64]($allow.End + 1); End = $range.End } }
        }
        $ranges = $next
    }
    $result = @($ranges | ForEach-Object {
        $first = ConvertFrom-IPv4Number $_.Start
        $last = ConvertFrom-IPv4Number $_.End
        if ($first -eq $last) { $first } else { "$first-$last" }
    })
    return $result + @('::', '::2-ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff')
}

# Item 2nx (a): A STATIC RULE. Loopback, plus the operator's own explicit exceptions --
# his confirmed LAN prefixes and shell.allowed_model_ranges -- and nothing derived from a
# model connection. Nothing here resolves a name, so applying protection cannot fail
# because a host is unreachable, and changing the active connection changes nothing.
$allowedRanges = @([pscustomobject]@{ Start = [uint64]2130706432; End = [uint64]2147483647; Prefix = '127.0.0.0/8' })
$configuredRanges = @()
foreach ($prefix in $AllowedRange) { $configuredRanges += Resolve-ConfiguredRange $prefix }
$allowedRanges += $configuredRanges

$allowedLANRanges = @()
if ($AllowLocalNetwork) {
    foreach ($prefix in $LocalSubnet) {
        if (-not [string]::IsNullOrWhiteSpace($prefix)) { $allowedLANRanges += Resolve-LANRange $prefix }
    }
    if ($allowedLANRanges.Count -eq 0) { throw 'AllowLocalNetwork requires at least one confirmed LocalSubnet.' }
}
$confirmedLANSubnets = @($allowedLANRanges | ForEach-Object { $_.Prefix } | Sort-Object -Unique)
if ($AllowLocalNetwork) { $allowedRanges += $allowedLANRanges }
$configuredRangeText = @($configuredRanges | ForEach-Object { $_.Prefix } | Sort-Object -Unique)
$blockedRanges = Resolve-BlockedRanges $allowedRanges
$policyDescription = "Agent_b allowed=$($configuredRangeText -join ',') lan=$($confirmedLANSubnets -join ',')"
$script:confirmationSuppressed = $NoPrompt -or ($PSBoundParameters.ContainsKey('Confirm') -and -not [bool]$PSBoundParameters['Confirm'])
if ($NoPrompt) { $ConfirmPreference = 'None' }

function Test-IsAdministrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Write-Summary {
    param([string[]]$Changed, [string[]]$NotChanged, [string[]]$Next)
    Write-Host ''
    Write-Host 'SUMMARY'
    Write-Host "Changed: $(if ($Changed.Count) { $Changed -join '; ' } else { 'nothing' })"
    Write-Host "Not changed: $(if ($NotChanged.Count) { $NotChanged -join '; ' } else { 'nothing' })"
    Write-Host "Next: $(if ($Next.Count) { $Next -join '; ' } else { 'no further action requested' })"
}

function Stop-UnsafeConfirmation {
    param([string]$Message)
    [Console]::Error.WriteLine("PROMPT REFUSED: $Message")
    [Console]::Error.WriteLine('Run this command alone in an interactive console, or use -Confirm:$false only after reviewing -WhatIf output.')
    Write-Summary -Changed @() -NotChanged @('firewall rules', 'firewall connection defaults') -Next @('rerun safely after reviewing -WhatIf output')
    exit 2
}

function Assert-SafeConfirmationInput {
    $redirected = $true
    try { $redirected = [Console]::IsInputRedirected } catch {
        Stop-UnsafeConfirmation -Message 'a usable console input stream could not be established.'
    }
    if ($redirected -or $Host.Name -ne 'ConsoleHost') {
        Stop-UnsafeConfirmation -Message 'input is redirected or the current PowerShell host has no usable interactive console.'
    }
    $buffered = $false
    try {
        while ([Console]::KeyAvailable) {
            $buffered = $true
            $null = [Console]::ReadKey($true)
        }
    } catch {
        Stop-UnsafeConfirmation -Message 'console input availability could not be verified safely.'
    }
    if ($buffered) {
        Stop-UnsafeConfirmation -Message 'queued console input was detected and drained. This commonly happens when a multi-line command block is pasted.'
    }
}

function Test-ConfirmationPromptExpected {
    return (-not $WhatIfPreference -and
        -not $script:confirmationSuppressed -and
        $ConfirmPreference -ne [Management.Automation.ConfirmImpact]::None -and
        [int][Management.Automation.ConfirmImpact]::High -ge [int]$ConfirmPreference)
}

function Resolve-LocalUserSid {
    param([string]$Name)
    $user = Get-LocalUser -Name $Name -ErrorAction SilentlyContinue
    if (-not $user) { throw "Local account '$Name' does not exist. Create and verify it in Agent_b Settings first." }
    return $user.SID.Value
}

function ConvertTo-AddressInterval {
    param([string]$Text)
    $text = $Text.Trim()
    $parts = @($text -split '-', 2)
    $prefix = $null
    if ($parts.Count -eq 1 -and $text -match '^(.+)/([0-9]{1,3})$') {
        $parts = @($Matches[1])
        $prefix = [int]$Matches[2]
    }
    $first = [Net.IPAddress]::Parse($parts[0]).GetAddressBytes()
    $last = if ($parts.Count -eq 2) { [Net.IPAddress]::Parse($parts[1]).GetAddressBytes() } else { [byte[]]$first.Clone() }
    if ($first.Length -ne $last.Length) { throw "Address range mixes IPv4 and IPv6: '$Text'" }
    if ($null -ne $prefix) {
        if ($prefix -lt 0 -or $prefix -gt ($first.Length * 8)) { throw "Address prefix is invalid: '$Text'" }
        for ($index = 0; $index -lt $first.Length; $index++) {
            $remaining = $prefix - ($index * 8)
            $mask = if ($remaining -ge 8) { 255 } elseif ($remaining -le 0) { 0 } else { (256 - [math]::Pow(2, 8 - $remaining)) }
            $first[$index] = $first[$index] -band [byte]$mask
            $last[$index] = $first[$index] -bor [byte](255 - $mask)
        }
    }
    $family = if ($first.Length -eq 4) { '4' } else { '6' }
    $firstHex = [BitConverter]::ToString($first).Replace('-', '')
    $lastHex = [BitConverter]::ToString($last).Replace('-', '')
    return "$family`:$firstHex-$lastHex"
}

function Test-RuleIntent {
    param([string]$LocalUserSddl, [switch]$Emit)
    $drifts = [Collections.Generic.List[string]]::new()
    function Compare-Property {
        param([string]$Name, $Expected, $Actual, [switch]$Addresses)
        $expectedValues = @($Expected | ForEach-Object { [string]$_ })
        $actualValues = @($Actual | ForEach-Object { [string]$_ })
        $expectedText = if ($expectedValues.Count) { $expectedValues -join ', ' } else { '<absent>' }
        $actualText = if ($actualValues.Count) { $actualValues -join ', ' } else { '<absent>' }
        if ($Addresses) {
            $expectedCompare = @($expectedValues | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | ForEach-Object { ConvertTo-AddressInterval $_ } | Sort-Object -Unique)
            $actualCompare = @($actualValues | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | ForEach-Object { ConvertTo-AddressInterval $_ } | Sort-Object -Unique)
        } else {
            $expectedCompare = @($expectedValues)
            $actualCompare = @($actualValues)
        }
        $same = $expectedCompare.Count -eq $actualCompare.Count -and -not (Compare-Object -ReferenceObject $expectedCompare -DifferenceObject $actualCompare)
        if (-not $same) { [void]$drifts.Add($Name) }
        if ($Emit) { Write-Host "$(if ($same) { 'PASS' } else { 'DRIFT' }) $Name`: expected $expectedText got $actualText" }
    }

    $rule = Get-NetFirewallRule -Name $ruleName -ErrorAction SilentlyContinue
    $security = if ($rule) { Get-NetFirewallSecurityFilter -AssociatedNetFirewallRule $rule } else { $null }
    $addresses = if ($rule) { @((Get-NetFirewallAddressFilter -AssociatedNetFirewallRule $rule).RemoteAddress) } else { @() }
    Compare-Property Rule 'present' $(if ($rule) { 'present' } else { $null })
    Compare-Property Direction 'Outbound' $(if ($rule) { $rule.Direction } else { $null })
    Compare-Property Action 'Block' $(if ($rule) { $rule.Action } else { $null })
    Compare-Property Enabled 'True' $(if ($rule) { $rule.Enabled } else { $null })
    Compare-Property Profile 'Any' $(if ($rule) { $rule.Profile } else { $null })
    Compare-Property Description $policyDescription $(if ($rule) { $rule.Description } else { $null })
    Compare-Property LocalUser $LocalUserSddl $(if ($security) { $security.LocalUser } else { $null })
    Compare-Property RemoteAddress $blockedRanges $addresses -Addresses

    $icmp = Get-NetFirewallRule -Name $LANICMPRuleName -ErrorAction SilentlyContinue
    if (-not $AllowLocalNetwork) {
        Compare-Property ICMP '<absent>' $(if ($icmp) { 'present' } else { '<absent>' })
    } else {
        $icmpSecurity = if ($icmp) { Get-NetFirewallSecurityFilter -AssociatedNetFirewallRule $icmp } else { $null }
        $icmpPort = if ($icmp) { Get-NetFirewallPortFilter -AssociatedNetFirewallRule $icmp } else { $null }
        $icmpAddresses = if ($icmp) { @((Get-NetFirewallAddressFilter -AssociatedNetFirewallRule $icmp).RemoteAddress) } else { @() }
        Compare-Property ICMP.Rule 'present' $(if ($icmp) { 'present' } else { $null })
        Compare-Property ICMP.Direction 'Outbound' $(if ($icmp) { $icmp.Direction } else { $null })
        Compare-Property ICMP.Action 'Allow' $(if ($icmp) { $icmp.Action } else { $null })
        Compare-Property ICMP.Enabled 'True' $(if ($icmp) { $icmp.Enabled } else { $null })
        Compare-Property ICMP.Profile 'Any' $(if ($icmp) { $icmp.Profile } else { $null })
        Compare-Property ICMP.LocalUser $LocalUserSddl $(if ($icmpSecurity) { $icmpSecurity.LocalUser } else { $null })
        Compare-Property ICMP.Protocol 'ICMPv4' $(if ($icmpPort) { $icmpPort.Protocol } else { $null })
        Compare-Property ICMP.IcmpType '8' $(if ($icmpPort) { $icmpPort.IcmpType } else { $null })
        Compare-Property ICMP.RemoteAddress $confirmedLANSubnets $icmpAddresses -Addresses
    }
    return [pscustomobject]@{ Correct = ($drifts.Count -eq 0); Drifts = @($drifts) }
}

if (($Verify.IsPresent -and $Remove.IsPresent) -or ($Inspect.IsPresent -and ($Verify.IsPresent -or $Remove.IsPresent))) {
    [Console]::Error.WriteLine('Choose only one of -Verify, -Remove, or -Inspect.')
    exit 2
}
Write-Host 'Agent_b service-account outbound firewall policy'
Write-Host "Account: $env:COMPUTERNAME\$AccountName"
Write-Host "Policy: one user-scoped outbound Block rule; spare loopback$(if ($configuredRangeText.Count) { ", configured ranges $($configuredRangeText -join ', ')" } else { '' })$(if ($AllowLocalNetwork) { ", and confirmed LAN $($confirmedLANSubnets -join ', ')" } else { '' })."
Write-Host "$(if ($AllowLocalNetwork) { 'One account-scoped outbound ICMPv4 echo Allow rule is created for the confirmed LAN subnets.' } else { 'No Allow rule is created.' }) Machine-wide DefaultOutboundAction is not changed."

if (-not (Test-IsAdministrator) -and -not $WhatIfPreference -and -not $Verify -and -not $Inspect) {
    [Console]::Error.WriteLine('Administrator elevation is required to apply or remove the firewall rule.')
    Write-Summary -Changed @() -NotChanged @('firewall rules', 'firewall connection defaults') -Next @('use Agent_b Settings or reopen PowerShell as Administrator')
    exit 1
}

if ($Remove) {
$present = @(Get-NetFirewallRule -Name $ruleName, $legacyAllowRuleName, $LANICMPRuleName -ErrorAction SilentlyContinue)
    if ($present.Count -eq 0) {
        Write-Host 'UNCHANGED: Agent_b reserved firewall rules are already absent.'
        Write-Summary -Changed @() -NotChanged @('firewall rules', 'firewall connection defaults') -Next @('remove ACLs before deleting the service account if rolling back fully')
        exit 0
    }
    if (Test-ConfirmationPromptExpected) { Assert-SafeConfirmationInput }
    if ($PSCmdlet.ShouldProcess(($present.Name -join ', '), 'Remove Agent_b firewall rules')) {
        $present | Remove-NetFirewallRule
        Write-Host "REMOVED: $($present.Name -join ', ')"
    }
    Write-Summary -Changed @('reserved Agent_b firewall rules removed') -NotChanged @('firewall connection defaults') -Next @('remove ACLs before deleting the service account if rolling back fully')
    exit 0
}

if ($WhatIfPreference) {
    Write-Host 'Mode: WhatIf; no firewall rule or connection setting will be changed.'
    $null = $PSCmdlet.ShouldProcess($ruleName, "Create or repair user-scoped outbound Block over: $($blockedRanges -join ', '); confirmed LAN: $($confirmedLANSubnets -join ', ')")
	if ($AllowLocalNetwork) { $null = $PSCmdlet.ShouldProcess($LANICMPRuleName, "Create account-scoped outbound ICMPv4 echo Allow for: $($confirmedLANSubnets -join ', ')") }
    Write-Summary -Changed @() -NotChanged @('firewall rules', 'firewall connection defaults') -Next @('apply from Agent_b Settings, then verify')
    exit 0
}

$sid = $null
try { $sid = Resolve-LocalUserSid -Name $AccountName } catch {
    if ($Inspect) {
        $status = [ordered]@{ supported = $true; account_exists = $false; applied = $false; summary = $_.Exception.Message; items = @([ordered]@{ rule = $ruleName; expected = 'account-scoped outbound Block policy'; found = 'service account missing' }) }
        Write-Output ($statusMarker + ($status | ConvertTo-Json -Compress))
        exit 0
    }
    [Console]::Error.WriteLine("Firewall policy failed: $($_.Exception.Message)")
    exit 1
}
$localUserSddl = "D:(A;;CC;;;$sid)"
$verification = Test-RuleIntent -LocalUserSddl $localUserSddl -Emit:$Verify
$correct = $verification.Correct
$legacyPresent = [bool](Get-NetFirewallRule -Name $legacyAllowRuleName -ErrorAction SilentlyContinue)

if ($Inspect) {
    # Item 2nx (c) and (d): there is no model address to drift, so the only two answers
    # are that the rule is right or that it is missing or different from the policy.
    $summary = if ($correct -and -not $legacyPresent) { 'user-scoped outbound policy verified' } elseif ($legacyPresent) { 'a conflicting legacy Allow rule is present; apply protection again to remove it' } else { 'the outbound rule is missing or differs from this policy; apply protection again' }
    $items = @()
    if (-not $correct) { $items += [ordered]@{ rule = $ruleName; expected = 'outbound Block except loopback and the operator-approved ranges'; found = $(if (Get-NetFirewallRule -Name $ruleName -ErrorAction SilentlyContinue) { 'rule differs from configured identity or destinations' } else { 'rule missing' }) } }
    if ($legacyPresent) { $items += [ordered]@{ rule = $legacyAllowRuleName; expected = 'absent'; found = 'conflicting legacy Allow rule present' } }
    $status = [ordered]@{ supported = $true; account_exists = $true; applied = ($correct -and -not $legacyPresent); summary = $summary; items = $items }
    Write-Output ($statusMarker + ($status | ConvertTo-Json -Compress))
    exit 0
}
if ($Verify) {
    Write-Host "$(if (-not $legacyPresent) { 'PASS' } else { 'DRIFT' }) LegacyAllow: expected <absent> got $(if ($legacyPresent) { $legacyAllowRuleName } else { '<absent>' })"
    $drifted = @($verification.Drifts)
    if ($legacyPresent) { $drifted += 'LegacyAllow' }
    Write-Host "$(if ($drifted.Count) { 'DRIFT' } else { 'PASS' }) $ruleName$(if ($drifted.Count) { ': ' + ($drifted -join ', ') } else { '' })"
    Write-Summary -Changed @() -NotChanged @('firewall rules', 'firewall connection defaults') -Next @($(if ($correct -and -not $legacyPresent) { 'firewall verification complete' } else { 'apply again to repair drift' }))
    if (-not $correct -or $legacyPresent) { exit 1 }
    exit 0
}

if ($correct -and -not $legacyPresent) {
    Write-Host "UNCHANGED: exact firewall policy already exists :: $ruleName"
    Write-Summary -Changed @() -NotChanged @('firewall rule', 'firewall connection defaults') -Next @('run the RBAC network check')
    exit 0
}

if (Test-ConfirmationPromptExpected) { Assert-SafeConfirmationInput }
# Item 2nl (b): -Profile Any, not -Connection Any. New-NetFirewallRule has no
# -Connection parameter -- its profile selector is -Profile -- and because both calls
# below sit inside ShouldProcess, every -WhatIf run bound cleanly and said so while
# every real Apply died here with "A parameter cannot be found that matches parameter
# name 'Connection'." The account step and the ACL step had already succeeded, so what
# the operator saw was a Repair that got most of the way and then refused.
if ($PSCmdlet.ShouldProcess($ruleName, 'Create or repair Agent_b user-scoped outbound Block rule')) {
    Get-NetFirewallRule -Name $ruleName, $legacyAllowRuleName, $LANICMPRuleName -ErrorAction SilentlyContinue | Remove-NetFirewallRule
    $null = New-NetFirewallRule -Name $ruleName -DisplayName $ruleName -Description $policyDescription -Direction Outbound -Action Block -Enabled True -Profile Any -LocalUser $localUserSddl -RemoteAddress $blockedRanges
    if ($AllowLocalNetwork) {
        $null = New-NetFirewallRule -Name $LANICMPRuleName -DisplayName $LANICMPRuleName -Description 'Allows outbound ICMPv4 echo to operator-confirmed LAN subnets for the Agent_b service identity.' -Direction Outbound -Action Allow -Enabled True -Profile Any -LocalUser $localUserSddl -Protocol ICMPv4 -IcmpType 8 -RemoteAddress $confirmedLANSubnets
    }
    $appliedVerification = Test-RuleIntent -LocalUserSddl $localUserSddl -Emit
    if (-not $appliedVerification.Correct) {
        Write-Host "FAILED: $ruleName drifted after write: $($appliedVerification.Drifts -join ', ')"
        Write-Summary -Changed @('firewall rule written but not verified') -NotChanged @('firewall connection defaults') -Next @('review the named drift above')
        exit 1
    }
    Write-Host "APPLIED: $ruleName (verified)"
}
Write-Summary -Changed @('user-scoped outbound Block rule applied') -NotChanged @('firewall connection defaults') -Next @('verify from Settings', 'run the RBAC network check')
exit 0
