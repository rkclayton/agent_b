# Item 2gl, v1.2.7/W2  -  the PWA policy entry, shared by the installer, the
# uninstaller and the launcher.
#
# WHY A POLICY. Installing a web app in Edge is otherwise a click in Edge's own
# UI, and the operator: "just go the installer route please, i don't want to
# dick around". Edge installs a web app with no click when
# WebAppInstallForceList tells it to.
#
# WHAT W1 MEASURED, AND WHAT IT COULD NOT. The policy shape below is right: one
# REG_SZ value per app, named "1", "2"..., each holding a JSON object. What W1
# could NOT do is apply it: writing that key returned PermissionDenied, because
# SOFTWARE\Policies is ReadKey for the operator and FullControl only for SYSTEM
# and Administrators  -  under HKCU as well as HKLM, which is what makes policy
# mean anything. So the policy is written by the ELEVATED INSTALLER, which is
# already administrator and costs no second UAC, and NOTHING HERE HAS BEEN SEEN
# TO PRODUCE AN INSTALLED APP. Every function below therefore fails safe: the
# launcher falls back to --app= on any doubt at all, which is exactly what
# production does today, so the worst case is the behaviour we already ship.
#
# THE TWO RULES THIS FILE EXISTS TO ENFORCE:
#   1. The entry may point ONLY at this installation's loopback app URL. A
#      policy that Edge obeys without asking is worth attacking; a hostile HKLM
#      writer could point "Agent_b" at any origin they liked and the window
#      would still say Agent_b. We write nothing else, and we read nothing else
#      back as ours.
#   2. The app id we hand to --app-id= must belong to OUR url. Edge's profile
#      directory is writable by whoever runs as the operator, so the id found
#      there is untrusted input, not a fact.

# DELIBERATELY NO Set-StrictMode. This file is dot-sourced into the installer,
# the uninstaller and the launcher, and strict mode is dynamically scoped: a
# first draft set it here and broke the install outright, because the installer
# reads a global that is not always set and strict mode turned that into a
# fatal error mid-copy. A library dot-sourced into somebody else's script does
# not get to change the language that script is written in.

$script:AgentBPwaPolicyRoot = 'HKLM:\SOFTWARE\Policies\Microsoft\Edge\WebAppInstallForceList'

function Get-AgentBPwaUrl {
    param([Parameter(Mandatory = $true)][string]$Origin)
    return ([Uri]::new([Uri]$Origin, '/chat')).AbsoluteUri
}

function Test-AgentBPwaUrl {
    # Rule 1. Loopback only, http only, and a path of our own. Anything else is
    # not ours and is never written, never removed, never launched.
    param([string]$Url)
    if (-not $Url) { return $false }
    $parsed = $null
    if (-not [Uri]::TryCreate($Url, [UriKind]::Absolute, [ref]$parsed)) { return $false }
    if ($parsed.Scheme -ne 'http') { return $false }
    if ($parsed.Host -ne '127.0.0.1') { return $false }
    if ($parsed.AbsolutePath -ne '/chat') { return $false }
    return $true
}

function Get-AgentBPwaEntryJson {
    param([Parameter(Mandatory = $true)][string]$Url)
    if (-not (Test-AgentBPwaUrl $Url)) { throw "refusing a PWA policy entry that is not this installation's loopback app URL: $Url" }
    # create_desktop_shortcut stays false: the Start Menu shortcut the installer
    # already writes is the one entry point, and a second icon Edge owns would
    # outlive an uninstall we do not control.
    return ([ordered]@{
        url                      = $Url
        default_launch_container = 'window'
        create_desktop_shortcut  = $false
    } | ConvertTo-Json -Compress)
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
    foreach ($pair in (Get-AgentBPwaPolicyValues -PolicyRoot $PolicyRoot).GetEnumerator()) {
        if ($pair.Value -and $pair.Value.Contains('"' + $Url + '"')) { return $pair.Key }
    }
    return $null
}

function Set-AgentBPwaPolicy {
    # Elevated-only. Returns the value name written, which the installer records
    # so the uninstaller removes exactly ours.
    param(
        [Parameter(Mandatory = $true)][string]$Url,
        [string]$PolicyRoot = $script:AgentBPwaPolicyRoot
    )
    $json = Get-AgentBPwaEntryJson -Url $Url
    # NOT New-Item -Force on an existing key: -Force DELETES AND RECREATES it,
    # which silently discards every other forced install on the machine. The
    # proof in test-pwa-policy.ps1 caught exactly that.
    if (-not (Test-Path -LiteralPath $PolicyRoot)) { $null = New-Item -Path $PolicyRoot -Force }
    $name = Find-AgentBPwaPolicyName -Url $Url -PolicyRoot $PolicyRoot
    if (-not $name) {
        # A free index, never a blind "1": clobbering someone else's forced
        # install would be a silent removal of software we did not install.
        $taken = (Get-AgentBPwaPolicyValues -PolicyRoot $PolicyRoot).Keys
        $index = 1
        while ($taken -contains [string]$index) { $index++ }
        $name = [string]$index
    }
    Set-ItemProperty -LiteralPath $PolicyRoot -Name $name -Value $json -Type String
    return $name
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

function Get-AgentBEdgeProfileDirectory {
    param([string]$UserDataDir)
    if ($UserDataDir) { return (Join-Path $UserDataDir 'Default') }
    if (-not $env:LOCALAPPDATA) { return $null }
    return (Join-Path $env:LOCALAPPDATA 'Microsoft\Edge\User Data\Default')
}

function Find-AgentBPwaAppId {
    # Rule 2. Edge records installed web apps in the profile's Preferences under
    # web_apps.web_app_ids, keyed by a 32-character id. That directory is
    # writable by anything running as the operator, so the id is untrusted: we
    # return one ONLY when the record it sits in carries our own url. A miss,
    # malformed JSON, a hostile record, or an id of the wrong shape all return
    # $null, and $null means --app=  -  today's behaviour.
    #
    # NOT VERIFIED AGAINST A REAL INSTALLED APP: W1 could not apply the policy,
    # so no app has ever been installed for this to read. It is written from the
    # documented layout and is deliberately the fail-safe half of the pair.
    param(
        [Parameter(Mandatory = $true)][string]$Url,
        [string]$ProfileDirectory
    )
    if (-not (Test-AgentBPwaUrl $Url)) { return $null }
    if (-not $ProfileDirectory) { $ProfileDirectory = Get-AgentBEdgeProfileDirectory }
    if (-not $ProfileDirectory) { return $null }
    $preferences = Join-Path $ProfileDirectory 'Preferences'
    if (-not (Test-Path -LiteralPath $preferences -PathType Leaf)) { return $null }

    $document = $null
    try {
        $document = Get-Content -Raw -LiteralPath $preferences -ErrorAction Stop | ConvertFrom-Json -ErrorAction Stop
    } catch {
        return $null
    }
    if (-not $document) { return $null }

    $apps = $null
    try { $apps = $document.web_apps.web_app_ids } catch { return $null }
    if (-not $apps) { return $null }

    $origin = ([Uri]::new([Uri]$Url, '/')).AbsoluteUri
    foreach ($property in $apps.PSObject.Properties) {
        $id = [string]$property.Name
        if ($id -notmatch '^[a-p]{32}$') { continue }
        $record = $property.Value
        $candidates = @()
        foreach ($field in @('start_url', 'manifest_url', 'scope')) {
            try { if ($record.$field) { $candidates += [string]$record.$field } } catch { }
        }
        foreach ($candidate in $candidates) {
            # The record must carry OUR origin. A record claiming our name, our
            # icon or our id but another origin is somebody else's app.
            if ($candidate -eq $Url -or $candidate -eq $origin) { return $id }
        }
    }
    return $null
}
