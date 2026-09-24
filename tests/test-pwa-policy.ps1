# Item 2gl, v1.3.0/W3 - the Edge policy REMOVAL, proved.
#
# v1.2.7 wrote a WebAppInstallForceList entry and this suite proved the write,
# the free-index selection and the app-id lookup. The operator measured the
# overlay on a policy-installed app and it does not activate, so the write is
# gone and those cases went with it. What is left is the half that must keep
# working: an uninstall on a machine where a v1.2.7 install wrote an entry has
# to take that entry away, and take away nothing else.
#
# The real policy key cannot be written without elevation (v1.2.7/W1 measured
# that: PermissionDenied, ACL ReadKey for the operator), so this runs against a
# DISPOSABLE key under HKCU that the operator can write.
param([switch]$Quiet)

$ErrorActionPreference = 'Stop'
. (Join-Path (Split-Path -Parent $PSScriptRoot) 'scripts\pwa-policy.ps1')

$script:pass = 0
$script:fail = 0
function Test-Case {
    param([string]$Name, [scriptblock]$Body)
    try {
        & $Body
        $script:pass++
        if (-not $Quiet) { Write-Host "PASS  $Name" }
    } catch {
        $script:fail++
        Write-Host "FAIL  $Name  -  $($_.Exception.Message)"
    }
}
function Assert-Equal {
    param($Expected, $Actual, [string]$What)
    if ([string]$Expected -ne [string]$Actual) { throw "$What`: expected '$Expected', got '$Actual'" }
}
function Assert-Null { param($Value, [string]$What) if ($null -ne $Value -and '' -ne [string]$Value) { throw "$What`: expected nothing, got '$Value'" } }

$registryRoot = 'HKCU:\Software\Agent_b\Test\PwaPolicy'
$url = 'http://127.0.0.1:8790/chat'
# Exactly what a v1.2.7 install wrote, so these are the bytes uninstall meets.
$ours = '{"url":"http://127.0.0.1:8790/chat","default_launch_container":"window","create_desktop_shortcut":false}'
$neighbour = '{"url":"https://example.com/app","default_launch_container":"window"}'

function Reset-PolicyRoot {
    if (Test-Path -LiteralPath $registryRoot) { Remove-Item -LiteralPath $registryRoot -Recurse -Force }
    $null = New-Item -Path $registryRoot -Force
}

try {
    Test-Case 'only a loopback http /chat url is ours' {
        foreach ($good in @('http://127.0.0.1:8790/chat', 'http://127.0.0.1:8787/chat')) {
            if (-not (Test-AgentBPwaUrl $good)) { throw "refused our own url $good" }
        }
        foreach ($bad in @(
            'http://evil.example/chat',          # another origin
            'https://127.0.0.1:8790/chat',       # another scheme
            'http://localhost:8790/chat',        # a name that can be repointed
            'http://127.0.0.1:8790/',            # another path
            'http://127.0.0.1:8790/chat/../x',   # a path that walks out
            'file:///C:/chat',
            '', $null)) {
            if (Test-AgentBPwaUrl $bad) { throw "accepted a url that is not ours: $bad" }
        }
    }

    Test-Case "a v1.2.7 install's entry is found and removed" {
        Reset-PolicyRoot
        Set-ItemProperty -LiteralPath $registryRoot -Name '1' -Value $ours -Type String
        Assert-Equal '1' (Find-AgentBPwaPolicyName -Url $url -PolicyRoot $registryRoot) 'found name'
        Assert-Equal '1' (Remove-AgentBPwaPolicy -Url $url -PolicyRoot $registryRoot) 'removed name'
        if (Test-Path -LiteralPath $registryRoot) { throw 'an empty key of ours was left behind' }
    }

    Test-Case 'a neighbour keeps its index and its value' {
        # Somebody else's forced install must survive our uninstall.
        Reset-PolicyRoot
        Set-ItemProperty -LiteralPath $registryRoot -Name '1' -Value $neighbour -Type String
        Set-ItemProperty -LiteralPath $registryRoot -Name '2' -Value $ours -Type String
        Assert-Equal '2' (Remove-AgentBPwaPolicy -Url $url -PolicyRoot $registryRoot) 'removed name'
        Assert-Equal $neighbour (Get-ItemProperty -LiteralPath $registryRoot).'1' 'neighbour value after our removal'
        if (-not (Test-Path -LiteralPath $registryRoot)) { throw 'the key was deleted while a neighbour still held a value' }
    }

    Test-Case 'an entry for another port is not ours to remove' {
        Reset-PolicyRoot
        $other = '{"url":"http://127.0.0.1:8787/chat","default_launch_container":"window"}'
        Set-ItemProperty -LiteralPath $registryRoot -Name '1' -Value $other -Type String
        Assert-Null (Remove-AgentBPwaPolicy -Url $url -PolicyRoot $registryRoot) 'removal of another installation'
        Assert-Equal $other (Get-ItemProperty -LiteralPath $registryRoot).'1' 'the other installation survives'
    }

    Test-Case 'removing what was never installed is not an error' {
        Reset-PolicyRoot
        Assert-Null (Remove-AgentBPwaPolicy -Url $url -PolicyRoot $registryRoot) 'removal of an absent entry'
        Remove-Item -LiteralPath $registryRoot -Recurse -Force
        Assert-Null (Remove-AgentBPwaPolicy -Url $url -PolicyRoot $registryRoot) 'removal with no key at all'
    }

    Test-Case 'a url that is not ours can never drive a removal' {
        # The url uninstall uses comes out of the registry, so it is input.
        Reset-PolicyRoot
        Set-ItemProperty -LiteralPath $registryRoot -Name '1' -Value $neighbour -Type String
        Assert-Null (Remove-AgentBPwaPolicy -Url 'https://example.com/app' -PolicyRoot $registryRoot) 'foreign url'
        Assert-Equal $neighbour (Get-ItemProperty -LiteralPath $registryRoot).'1' 'the neighbour survives a foreign url'
    }

    Test-Case 'the write path is gone, not merely unused' {
        $module = Get-Content -Raw -LiteralPath (Join-Path (Split-Path -Parent $PSScriptRoot) 'scripts\pwa-policy.ps1')
        foreach ($gone in @('Set-AgentBPwaPolicy', 'Get-AgentBPwaEntryJson', 'Find-AgentBPwaAppId')) {
            if ($module -match ("function\s+" + [regex]::Escape($gone))) { throw "$gone is still defined" }
        }
        $installer = Get-Content -Raw -LiteralPath (Join-Path (Split-Path -Parent $PSScriptRoot) 'scripts\install-Agent_b.ps1')
        if ($installer -match 'Set-AgentBPwaPolicy') { throw 'the installer still writes the policy' }
        $launcher = Get-Content -Raw -LiteralPath (Join-Path (Split-Path -Parent $PSScriptRoot) 'scripts\launch-Agent_b.ps1')
        if ($launcher -match '\-\-app-id=') { throw 'the launcher still opens an installed app' }
    }
} finally {
    if (Test-Path -LiteralPath $registryRoot) { Remove-Item -LiteralPath $registryRoot -Recurse -Force -ErrorAction SilentlyContinue }
    $registryParent = 'HKCU:\Software\Agent_b\Test'
    if ((Test-Path -LiteralPath $registryParent) -and -not (Get-ChildItem -LiteralPath $registryParent -ErrorAction SilentlyContinue)) {
        Remove-Item -LiteralPath $registryParent -Recurse -Force -ErrorAction SilentlyContinue
    }
}

Write-Host ''
Write-Host "PWA POLICY REMOVAL: $script:pass PASS, $script:fail FAIL"
if ($script:fail) { exit 1 }
exit 0
