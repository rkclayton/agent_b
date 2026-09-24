[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'High')]
param(
    [ValidatePattern('^[A-Za-z0-9._-]+$')]
    [string]$AccountName = 'agentb-svc',
    [string]$ModelAddress = '127.0.0.1',
    [ValidateRange(1, 65535)]
    [int]$ModelPort = 8080,
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

function Resolve-ModelAddresses {
    param([string]$Name)
    $literal = $null
    if ([Net.IPAddress]::TryParse($Name, [ref]$literal)) { return @($literal) }
    try { $addresses = @([Net.Dns]::GetHostAddresses($Name)) } catch { throw "Model host '$Name' could not be resolved: $($_.Exception.Message)" }
    if ($addresses.Count -eq 0) { throw "Model host '$Name' resolved to no addresses." }
    return @($addresses | Sort-Object -Property IPAddressToString -Unique)
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

$resolvedModelAddresses = @(Resolve-ModelAddresses -Name $ModelAddress)
$resolvedIPv4 = @($resolvedModelAddresses | Where-Object { $_.AddressFamily -eq [Net.Sockets.AddressFamily]::InterNetwork })
$unsupportedIPv6 = @($resolvedModelAddresses | Where-Object { $_.AddressFamily -eq [Net.Sockets.AddressFamily]::InterNetworkV6 -and -not $_.Equals([Net.IPAddress]::IPv6Loopback) })
if ($resolvedIPv4.Count -eq 0 -and $unsupportedIPv6.Count -gt 0) { throw "Model host '$ModelAddress' resolved only to IPv6 addresses; this policy currently requires an IPv4 address or loopback." }
$resolvedAddressText = @($resolvedModelAddresses | ForEach-Object { $_.IPAddressToString } | Sort-Object -Unique)
$allowedRanges = @([pscustomobject]@{ Start = [uint64]2130706432; End = [uint64]2147483647; Prefix = '127.0.0.0/8' })
foreach ($address in $resolvedIPv4) {
    $number = ConvertTo-IPv4Number $address
    $allowedRanges += [pscustomobject]@{ Start = $number; End = $number; Prefix = $address.IPAddressToString }
}
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
$policyDescription = "Agent_b model=$ModelAddress addresses=$($resolvedAddressText -join ',') allowed=$($configuredRangeText -join ',') lan=$($confirmedLANSubnets -join ',')"
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

function Test-RuleIntent {
    param([string]$LocalUserSddl)
    $rule = Get-NetFirewallRule -Name $ruleName -ErrorAction SilentlyContinue
    if (-not $rule) { return $false }
    if ($rule.Direction -ne 'Outbound' -or $rule.Action -ne 'Block' -or $rule.Enabled -ne 'True' -or $rule.Connection -ne 'Any') { return $false }
    if ($rule.Description -ne $policyDescription) { $script:resolutionChanged = $true; return $false }
    # LocalUser is stored on the associated network-layer security filter,
    # not on the MSFT_NetFirewallRule object returned by Get-NetFirewallRule.
    $security = Get-NetFirewallSecurityFilter -AssociatedNetFirewallRule $rule
    if ($security.LocalUser -ne $LocalUserSddl) { return $false }
    $actual = @((Get-NetFirewallAddressFilter -AssociatedNetFirewallRule $rule).RemoteAddress | Sort-Object)
    $expected = @($blockedRanges | Sort-Object)
    $blockCorrect = ($actual.Count -eq $expected.Count -and -not (Compare-Object -ReferenceObject $expected -DifferenceObject $actual))
    $icmp = Get-NetFirewallRule -Name $LANICMPRuleName -ErrorAction SilentlyContinue
    if (-not $AllowLocalNetwork) { return $blockCorrect -and -not $icmp }
    if (-not $icmp -or $icmp.Direction -ne 'Outbound' -or $icmp.Action -ne 'Allow' -or $icmp.Enabled -ne 'True' -or $icmp.Connection -ne 'Any') { return $false }
    $icmpSecurity = Get-NetFirewallSecurityFilter -AssociatedNetFirewallRule $icmp
    $icmpPort = Get-NetFirewallPortFilter -AssociatedNetFirewallRule $icmp
    $icmpAddresses = @((Get-NetFirewallAddressFilter -AssociatedNetFirewallRule $icmp).RemoteAddress | Sort-Object)
    return $blockCorrect -and $icmpSecurity.LocalUser -eq $LocalUserSddl -and $icmpPort.Protocol -eq 'ICMPv4' -and $icmpPort.IcmpType -eq '8' -and $icmpAddresses.Count -eq $confirmedLANSubnets.Count -and -not (Compare-Object -ReferenceObject $confirmedLANSubnets -DifferenceObject $icmpAddresses)
}

if (($Verify.IsPresent -and $Remove.IsPresent) -or ($Inspect.IsPresent -and ($Verify.IsPresent -or $Remove.IsPresent))) {
    [Console]::Error.WriteLine('Choose only one of -Verify, -Remove, or -Inspect.')
    exit 2
}
Write-Host 'Agent_b service-account outbound firewall policy'
Write-Host "Account: $env:COMPUTERNAME\$AccountName"
Write-Host "Model endpoint: $ModelAddress`:$ModelPort -> $($resolvedAddressText -join ', ')"
Write-Host "Policy: one user-scoped outbound Block rule; spare loopback and the configured model server$(if ($configuredRangeText.Count) { ", configured ranges $($configuredRangeText -join ', ')" } else { '' })$(if ($AllowLocalNetwork) { ", and confirmed LAN $($confirmedLANSubnets -join ', ')" } else { '' })."
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
        $status = [ordered]@{ supported = $true; account_exists = $false; applied = $false; summary = $_.Exception.Message }
        Write-Output ($statusMarker + ($status | ConvertTo-Json -Compress))
        exit 0
    }
    [Console]::Error.WriteLine("Firewall policy failed: $($_.Exception.Message)")
    exit 1
}
$localUserSddl = "D:(A;;CC;;;$sid)"
$script:resolutionChanged = $false
$correct = Test-RuleIntent -LocalUserSddl $localUserSddl
$legacyPresent = [bool](Get-NetFirewallRule -Name $legacyAllowRuleName -ErrorAction SilentlyContinue)

if ($Inspect) {
    $summary = if ($correct -and -not $legacyPresent) { 'user-scoped outbound policy verified' } elseif ($script:resolutionChanged) { "model host resolution changed; apply protection again ($ModelAddress -> $($resolvedAddressText -join ', '))" } else { 'firewall rule missing or drifted' }
    $status = [ordered]@{ supported = $true; account_exists = $true; applied = ($correct -and -not $legacyPresent); summary = $summary; resolved_addresses = $resolvedAddressText; resolution_changed = $script:resolutionChanged }
    Write-Output ($statusMarker + ($status | ConvertTo-Json -Compress))
    exit 0
}
if ($Verify) {
    Write-Host "$(if ($correct) { 'PASS' } else { 'DRIFT' }): $ruleName"
    Write-Host "$(if (-not $legacyPresent) { 'PASS' } else { 'DRIFT' }): no conflicting legacy Allow rule"
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
if ($PSCmdlet.ShouldProcess($ruleName, 'Create or repair Agent_b user-scoped outbound Block rule')) {
    Get-NetFirewallRule -Name $ruleName, $legacyAllowRuleName, $LANICMPRuleName -ErrorAction SilentlyContinue | Remove-NetFirewallRule
    $null = New-NetFirewallRule -Name $ruleName -DisplayName $ruleName -Description $policyDescription -Direction Outbound -Action Block -Enabled True -Connection Any -LocalUser $localUserSddl -RemoteAddress $blockedRanges
    if ($AllowLocalNetwork) {
        $null = New-NetFirewallRule -Name $LANICMPRuleName -DisplayName $LANICMPRuleName -Description 'Allows outbound ICMPv4 echo to operator-confirmed LAN subnets for the Agent_b service identity.' -Direction Outbound -Action Allow -Enabled True -Connection Any -LocalUser $localUserSddl -Protocol ICMPv4 -IcmpType 8 -RemoteAddress $confirmedLANSubnets
    }
    Write-Host "APPLIED: $ruleName"
}
Write-Summary -Changed @('user-scoped outbound Block rule applied') -NotChanged @('firewall connection defaults') -Next @('verify from Settings', 'run the RBAC network check')
exit 0
