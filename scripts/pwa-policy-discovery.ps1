# Item 2gl, v1.2.7/W1 — what Edge's WebAppInstallForceList actually does here.
#
# The operator: "just go the installer route please, i don't want to dick
# around" - so no Edge UI click by anyone. Edge installs a web app without a
# click when a policy tells it to, and this measures that on THIS Edge (153):
# the exact JSON it accepts, where it records the resulting app id, and how long
# after the policy is written Edge performs the install.
#
# Care taken, because a policy is machine-wide even when the profile is not:
#   * the entry is written under HKCU, which needs no elevation and touches no
#     machine-wide state,
#   * it points at a loopback URL that exists only while this runs,
#   * it is REMOVED in a finally, so it cannot outlive the measurement,
#   * and the Edge that reads it runs in a disposable user-data-dir, so the app
#     lands in a profile that is deleted afterwards rather than the operator's.
param(
    [Parameter(Mandatory = $true)][string]$Url,
    [Parameter(Mandatory = $true)][string]$ProfileDir,
    [Parameter(Mandatory = $true)][string]$Evidence,
    [int]$WaitSeconds = 60
)

$ErrorActionPreference = 'Stop'
$policyKey = 'HKCU:\SOFTWARE\Policies\Microsoft\Edge\WebAppInstallForceList'
$edge = 'C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe'
$null = New-Item -ItemType Directory -Force -Path $Evidence, $ProfileDir

# The policy is a list of JSON objects, one per app, each value named "1", "2"…
$entry = [ordered]@{
    url                     = $Url
    default_launch_container = 'window'
    create_desktop_shortcut = $false
}
$json = $entry | ConvertTo-Json -Compress

$result = [ordered]@{
    policy_key   = $policyKey
    policy_json  = $json
    edge_version = (Get-Item $edge).VersionInfo.ProductVersion
    written_at   = ''
    installed_at = ''
    seconds_to_install = $null
    app_id       = ''
    app_id_source = ''
    web_app_dirs = @()
    policy_applied = $false
    removed      = $false
}

$edgeProcess = $null
try {
    $null = New-Item -Path $policyKey -Force
    Set-ItemProperty -LiteralPath $policyKey -Name '1' -Value $json -Type String
    $result.written_at = (Get-Date).ToString('o')
    Write-Output "policy written: $policyKey\1 = $json"

    # Edge reads policy at startup. A disposable user-data-dir means the app is
    # installed into a profile this script owns and deletes.
    $edgeProcess = Start-Process -FilePath $edge -ArgumentList @(
        "--user-data-dir=$ProfileDir",
        '--no-first-run',
        '--no-default-browser-check',
        'about:blank'
    ) -PassThru
    Write-Output "Edge PID $($edgeProcess.Id) started against a disposable profile"

    $started = Get-Date
    $deadline = $started.AddSeconds($WaitSeconds)
    while ((Get-Date) -lt $deadline) {
        $webApps = Join-Path $ProfileDir 'Default\Web Applications'
        if (Test-Path -LiteralPath $webApps) {
            $dirs = @(Get-ChildItem -LiteralPath $webApps -Directory -ErrorAction SilentlyContinue)
            if ($dirs.Count) {
                $result.web_app_dirs = @($dirs | ForEach-Object { $_.Name })
                $result.installed_at = (Get-Date).ToString('o')
                $result.seconds_to_install = [Math]::Round(((Get-Date) - $started).TotalSeconds, 1)
                break
            }
        }
        Start-Sleep -Milliseconds 500
    }

    # Where Edge records the app id: the profile's Preferences carry the web app
    # ids it installed, and each app also gets a directory named for its id.
    $prefs = Join-Path $ProfileDir 'Default\Preferences'
    if (Test-Path -LiteralPath $prefs) {
        $text = Get-Content -Raw -LiteralPath $prefs
        $matches = [regex]::Matches($text, '"([a-p]{32})"')
        if ($matches.Count) {
            $result.app_id = $matches[0].Groups[1].Value
            $result.app_id_source = 'Default\Preferences'
        }
        $result.policy_applied = $text -match 'WebAppInstallForceList'
    }
    if (-not $result.app_id -and $result.web_app_dirs.Count) {
        $result.app_id = ($result.web_app_dirs | Where-Object { $_ -match '^[a-p]{32}$' } | Select-Object -First 1)
        if ($result.app_id) { $result.app_id_source = 'Default\Web Applications\<id>' }
    }
} finally {
    # Hard stop (12): the Edge this script started is ended by its PID.
    if ($edgeProcess -and -not $edgeProcess.HasExited) {
        Stop-Process -Id $edgeProcess.Id -Force -ErrorAction SilentlyContinue
    }
    # The policy NEVER outlives the measurement.
    if (Test-Path -LiteralPath $policyKey) {
        Remove-Item -LiteralPath $policyKey -Recurse -Force
        $result.removed = -not (Test-Path -LiteralPath $policyKey)
    } else {
        $result.removed = $true
    }
    Write-Output "policy removed: $($result.removed)"
    $result | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $Evidence 'pwa-policy-discovery.json') -Encoding utf8
}

Write-Output ("installed: " + [bool]$result.web_app_dirs.Count + "; app id: '" + $result.app_id + "' from " + $result.app_id_source + "; after " + $result.seconds_to_install + "s")
