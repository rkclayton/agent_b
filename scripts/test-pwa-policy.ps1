# Item 2gl, v1.2.7/W2  -  the policy entry and the app-id lookup, proved.
#
# The real policy key cannot be written without elevation (v1.2.7/W1 measured
# that: PermissionDenied, ACL ReadKey for the operator). So this exercises the
# same functions against a DISPOSABLE key under HKCU that the operator can
# write, and against fabricated Edge profiles. What that proves is the logic  - 
# free-index selection, neighbour safety, loopback-only refusal, and every
# fail-safe branch of the app-id lookup. What it CANNOT prove is that Edge
# installs the app, because no unelevated session can make it.
param([switch]$Quiet)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'pwa-policy.ps1')

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

$root = 'HKCU:\Software\Agent_b\Test\PwaPolicy'
$scratch = Join-Path ([IO.Path]::GetTempPath()) ("agentb-pwa-" + [Guid]::NewGuid().ToString('N'))
$url = 'http://127.0.0.1:8790/chat'

function Reset-PolicyRoot {
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force }
    $null = New-Item -Path $root -Force
}

function New-PreferencesProfile {
    param([string]$Json)
    $dir = Join-Path $scratch ([Guid]::NewGuid().ToString('N'))
    $null = New-Item -ItemType Directory -Path $dir -Force
    Set-Content -LiteralPath (Join-Path $dir 'Preferences') -Value $Json -Encoding utf8
    return $dir
}

try {
    $null = New-Item -ItemType Directory -Path $scratch -Force

    # ---- the url and the entry -------------------------------------------
    Test-Case 'the app url is /chat on the configured port' {
        Assert-Equal $url (Get-AgentBPwaUrl -Origin 'http://127.0.0.1:8790/') 'app url'
    }
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
    Test-Case 'the entry json is the shape Edge takes' {
        $entry = Get-AgentBPwaEntryJson -Url $url | ConvertFrom-Json
        Assert-Equal $url $entry.url 'url'
        Assert-Equal 'window' $entry.default_launch_container 'launch container'
        if ($entry.create_desktop_shortcut -ne $false) { throw 'create_desktop_shortcut must be false' }
    }
    Test-Case 'an entry that is not ours is refused, not written' {
        $threw = $false
        try { $null = Get-AgentBPwaEntryJson -Url 'http://evil.example/chat' } catch { $threw = $true }
        if (-not $threw) { throw 'a foreign url produced an entry' }
    }

    # ---- writing, finding, removing --------------------------------------
    Test-Case 'the first install takes index 1 and can be found again' {
        Reset-PolicyRoot
        Assert-Equal '1' (Set-AgentBPwaPolicy -Url $url -PolicyRoot $root) 'value name'
        Assert-Equal '1' (Find-AgentBPwaPolicyName -Url $url -PolicyRoot $root) 'found name'
        Assert-Equal (Get-AgentBPwaEntryJson -Url $url) (Get-ItemProperty -LiteralPath $root).'1' 'stored json'
    }
    Test-Case 'a reinstall rewrites its own value rather than adding a second' {
        Reset-PolicyRoot
        $null = Set-AgentBPwaPolicy -Url $url -PolicyRoot $root
        Assert-Equal '1' (Set-AgentBPwaPolicy -Url $url -PolicyRoot $root) 'value name on reinstall'
        Assert-Equal 1 (Get-AgentBPwaPolicyValues -PolicyRoot $root).Count 'value count'
    }
    Test-Case 'a neighbour keeps its index and its value' {
        # Somebody else's forced install must survive ours, both ways.
        Reset-PolicyRoot
        $neighbour = '{"url":"https://example.com/app","default_launch_container":"window"}'
        Set-ItemProperty -LiteralPath $root -Name '1' -Value $neighbour -Type String
        Assert-Equal '2' (Set-AgentBPwaPolicy -Url $url -PolicyRoot $root) 'our value name'
        Assert-Equal $neighbour (Get-ItemProperty -LiteralPath $root).'1' 'neighbour value after our write'
        Assert-Equal '2' (Remove-AgentBPwaPolicy -Url $url -PolicyRoot $root) 'removed value name'
        Assert-Equal $neighbour (Get-ItemProperty -LiteralPath $root).'1' 'neighbour value after our removal'
        if (-not (Test-Path -LiteralPath $root)) { throw 'the key was deleted while a neighbour still held a value' }
    }
    Test-Case 'the last removal takes the empty key with it' {
        Reset-PolicyRoot
        $null = Set-AgentBPwaPolicy -Url $url -PolicyRoot $root
        Assert-Equal '1' (Remove-AgentBPwaPolicy -Url $url -PolicyRoot $root) 'removed value name'
        if (Test-Path -LiteralPath $root) { throw 'an empty key of ours was left behind' }
    }
    Test-Case 'removing what was never installed is not an error' {
        Reset-PolicyRoot
        Assert-Null (Remove-AgentBPwaPolicy -Url $url -PolicyRoot $root) 'removal of an absent entry'
        Remove-Item -LiteralPath $root -Recurse -Force
        Assert-Null (Remove-AgentBPwaPolicy -Url $url -PolicyRoot $root) 'removal with no key at all'
    }
    Test-Case 'two installations on two ports do not collide' {
        Reset-PolicyRoot
        $other = 'http://127.0.0.1:8787/chat'
        Assert-Equal '1' (Set-AgentBPwaPolicy -Url $url -PolicyRoot $root) 'first'
        Assert-Equal '2' (Set-AgentBPwaPolicy -Url $other -PolicyRoot $root) 'second'
        $null = Remove-AgentBPwaPolicy -Url $url -PolicyRoot $root
        Assert-Equal '2' (Find-AgentBPwaPolicyName -Url $other -PolicyRoot $root) 'the survivor'
    }

    # ---- the app-id lookup, which is untrusted input ---------------------
    $id = 'abcdefghijklmnopabcdefghijklmnop'
    Test-Case 'the id is returned when the record carries our url' {
        $dir = New-PreferencesProfile ('{"web_apps":{"web_app_ids":{"' + $id + '":{"start_url":"' + $url + '"}}}}')
        Assert-Equal $id (Find-AgentBPwaAppId -Url $url -ProfileDirectory $dir) 'app id'
    }
    Test-Case 'a record for another origin is not ours' {
        $dir = New-PreferencesProfile ('{"web_apps":{"web_app_ids":{"' + $id + '":{"start_url":"https://evil.example/chat"}}}}')
        Assert-Null (Find-AgentBPwaAppId -Url $url -ProfileDirectory $dir) 'foreign record'
    }
    Test-Case 'an installation on another port is not ours' {
        $dir = New-PreferencesProfile ('{"web_apps":{"web_app_ids":{"' + $id + '":{"start_url":"http://127.0.0.1:8787/chat"}}}}')
        Assert-Null (Find-AgentBPwaAppId -Url $url -ProfileDirectory $dir) 'other port'
    }
    Test-Case 'an id of the wrong shape is refused even with our url' {
        # The id goes onto a command line. Only Edge's own alphabet, only its
        # length, so a crafted key cannot smuggle a second argument in.
        foreach ($bad in @('short', 'abcdefghijklmnopabcdefghijklmnoq', ($id + 'a'), '--disable-web-security')) {
            $dir = New-PreferencesProfile ('{"web_apps":{"web_app_ids":{"' + $bad + '":{"start_url":"' + $url + '"}}}}')
            Assert-Null (Find-AgentBPwaAppId -Url $url -ProfileDirectory $dir) "malformed id '$bad'"
        }
    }
    Test-Case 'every missing, broken or hostile profile falls back rather than guessing' {
        Assert-Null (Find-AgentBPwaAppId -Url $url -ProfileDirectory (Join-Path $scratch 'absent')) 'no profile directory'
        Assert-Null (Find-AgentBPwaAppId -Url $url -ProfileDirectory (New-PreferencesProfile 'not json at all')) 'unparseable Preferences'
        Assert-Null (Find-AgentBPwaAppId -Url $url -ProfileDirectory (New-PreferencesProfile '{}')) 'Preferences with no web apps'
        Assert-Null (Find-AgentBPwaAppId -Url $url -ProfileDirectory (New-PreferencesProfile '{"web_apps":{"web_app_ids":{}}}')) 'no installed apps'
        Assert-Null (Find-AgentBPwaAppId -Url $url -ProfileDirectory (New-PreferencesProfile ('{"web_apps":{"web_app_ids":{"' + $id + '":{}}}}'))) 'a record with no url'
    }
    Test-Case 'a lookup for a url that is not ours never returns anything' {
        $dir = New-PreferencesProfile ('{"web_apps":{"web_app_ids":{"' + $id + '":{"start_url":"https://evil.example/chat"}}}}')
        Assert-Null (Find-AgentBPwaAppId -Url 'https://evil.example/chat' -ProfileDirectory $dir) 'foreign lookup url'
    }
} finally {
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction SilentlyContinue }
    $parent = 'HKCU:\Software\Agent_b\Test'
    if ((Test-Path -LiteralPath $parent) -and -not (Get-ChildItem -LiteralPath $parent -ErrorAction SilentlyContinue)) {
        Remove-Item -LiteralPath $parent -Recurse -Force -ErrorAction SilentlyContinue
    }
    if (Test-Path -LiteralPath $scratch) { Remove-Item -LiteralPath $scratch -Recurse -Force -ErrorAction SilentlyContinue }
}

Write-Host ''
Write-Host "PWA POLICY: $script:pass PASS, $script:fail FAIL"
if ($script:fail) { exit 1 }
exit 0
