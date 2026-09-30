[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)][string]$CandidateDirectory,
    [string]$EvidenceDirectory
)

$ErrorActionPreference = 'Stop'
$root = Join-Path ([IO.Path]::GetTempPath()) ('Agent_b-native-policy-' + [Guid]::NewGuid().ToString('N'))
$application = Join-Path $root 'Application\Agent_b'
$data = Join-Path $root 'Data\Agent_b'
$workspace = Join-Path $root 'Workspace'
$startMenu = Join-Path $root 'StartMenu'
$sendTo = Join-Path $root 'SendTo'
$registryPS = 'HKCU:\Software\Agent_b-Installer-Test-' + [Guid]::NewGuid().ToString('N')
$policyPS = 'HKCU:\SOFTWARE\Microsoft\PowerShell\1\ShellIds\Microsoft.PowerShell'
$policyExisted = Test-Path -LiteralPath $policyPS
$oldPolicy = $null
if ($policyExisted) {
    $item = Get-ItemProperty -LiteralPath $policyPS
    if ($item.PSObject.Properties.Name -contains 'ExecutionPolicy') { $oldPolicy = [string]$item.ExecutionPolicy }
}
$installed = Join-Path $application 'Agent_b.exe'
$process = $null
$oldProcessPolicy = $env:PSExecutionPolicyPreference
. (Join-Path (Split-Path -Parent $PSScriptRoot) 'scripts\removal-guard.ps1')
try {
    $null = New-Item -ItemType Directory -Path $policyPS -Force
    $null = New-ItemProperty -LiteralPath $policyPS -Name ExecutionPolicy -PropertyType String -Value Restricted -Force
    Remove-Item Env:PSExecutionPolicyPreference -ErrorAction SilentlyContinue
    $candidate = Join-Path ([IO.Path]::GetFullPath($CandidateDirectory)) 'Agent_b.exe'
    $arguments = @('--quiet', '--install-source', ([IO.Path]::GetFullPath($CandidateDirectory)), '--install-data', $data, '-NoStart', '--install',
        '-ApplicationDirectory', $application, '-DataDirectory', $data, '-WorkspaceDirectory', $workspace,
        '-StartMenuDirectory', $startMenu, '-SendToDirectory', $sendTo, '-UninstallRegistryPath', $registryPS, '-NativeTestMode')
    $savedPreference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try { $output = (& $candidate @arguments 2>&1 | Out-String) } finally { $ErrorActionPreference = $savedPreference }
    if ($LASTEXITCODE -ne 0) { throw "Native policy-blocked install exited $LASTEXITCODE.`n$output" }
    $log = Get-ChildItem -LiteralPath (Join-Path $data 'logs') -Filter 'installer-*.log' -File | Sort-Object LastWriteTimeUtc -Descending | Select-Object -First 1
    if (-not $log) { throw 'Native install wrote no transcript.' }
    $transcript = Get-Content -Raw -LiteralPath $log.FullName
    $wording = 'Windows policy on this machine disables PowerShell scripts (Restricted, set by CurrentUser); the service identity cannot be set up here'
    if ($transcript -notmatch 'execution policy: Restricted from CurrentUser' -or $transcript -notmatch [regex]::Escape($wording)) { throw "Policy and scope are absent from the transcript.`n$transcript" }
    if ($transcript -match '(?i)powershell\.exe|install-Agent_b\.ps1') { throw "Native install invoked or named PowerShell.`n$transcript" }
    if ((Get-Content -Raw -LiteralPath (Join-Path $data 'execution-policy.txt')).Trim() -ne $wording) { throw 'Settings policy record does not contain the exact blocked-step wording.' }
    if (-not (Test-Path -LiteralPath $installed -PathType Leaf)) { throw 'Native install produced no application executable.' }
    $registration = Get-ItemProperty -LiteralPath $registryPS
    if ($registration.InstallLocation -ne $application -or $registration.UninstallString -notmatch 'uninstall-registry-path') { throw 'Native install registration differs from its disposable roots.' }

    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $listener.Start(); $port = ([Net.IPEndPoint]$listener.LocalEndpoint).Port; $listener.Stop()
    $configPath = Join-Path $data 'harness.json'
    $config = Get-Content -Raw -LiteralPath $configPath | ConvertFrom-Json
    $config.listen = "127.0.0.1:$port"
    [IO.File]::WriteAllText($configPath, ($config | ConvertTo-Json -Depth 100) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
    $process = Start-Process -FilePath $installed -ArgumentList @('-config', $configPath, '-app-root', $application, '-data-root', $data) -WorkingDirectory $data -WindowStyle Hidden -PassThru
    $status = $null
    $deadline = (Get-Date).AddSeconds(20)
    while ((Get-Date) -lt $deadline) {
        try { $status = Invoke-RestMethod -UseBasicParsing -Uri "http://127.0.0.1:$port/api/service-account" -TimeoutSec 2; break } catch { Start-Sleep -Milliseconds 200 }
    }
    if (-not $status -or $status.execution_policy_message -ne $wording) { throw 'The running app did not expose the policy wording in Settings status.' }
    if ($EvidenceDirectory) {
        $null = New-Item -ItemType Directory -Path $EvidenceDirectory -Force
        & node (Join-Path $PSScriptRoot 'capture-policy-settings.mjs') "http://127.0.0.1:$port/chat?setup=skip#settings/shell" (Join-Path $EvidenceDirectory 'policy-blocked-settings.png') $wording | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "Headless Settings capture exited $LASTEXITCODE." }
    }

    $savedPreference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try { & $installed --uninstall --purge-data --app-root $application --data-root $data --start-menu-root $startMenu --send-to-root $sendTo --uninstall-registry-path $registryPS } finally { $ErrorActionPreference = $savedPreference }
    if ($LASTEXITCODE -ne 0) { throw "Native uninstall launcher exited $LASTEXITCODE." }
    $deadline = (Get-Date).AddSeconds(60)
    while ((Get-Date) -lt $deadline -and ((Test-Path -LiteralPath $application) -or (Test-Path -LiteralPath $registryPS))) { Start-Sleep -Milliseconds 250 }
    $left = @()
    foreach ($item in @($application, $data, $registryPS, (Join-Path $startMenu 'Agent_b.lnk'), (Join-Path $sendTo 'Agent_b.lnk'))) { if (Test-Path -LiteralPath $item) { $left += $item } }
    if ($left.Count) { throw "Native uninstall left disposable artifact(s): $($left -join ', ')" }
    Write-Host "PASS policy-blocked native install: $($log.FullName)"
    Write-Host 'PASS running Settings status carried the exact Restricted/CurrentUser wording; native uninstall removed application, data, links, and registration'
} finally {
    if ($null -ne $oldProcessPolicy) { $env:PSExecutionPolicyPreference = $oldProcessPolicy } else { Remove-Item Env:PSExecutionPolicyPreference -ErrorAction SilentlyContinue }
    if ($oldPolicy -ne $null) { $null = New-ItemProperty -LiteralPath $policyPS -Name ExecutionPolicy -PropertyType String -Value $oldPolicy -Force } else { Remove-ItemProperty -LiteralPath $policyPS -Name ExecutionPolicy -ErrorAction SilentlyContinue }
    if (-not $policyExisted -and (Test-Path -LiteralPath $policyPS)) { Remove-Item -LiteralPath $policyPS -Force }
    if (Test-Path -LiteralPath $installed -PathType Leaf) {
        $savedPreference = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        try { & $installed --uninstall --purge-data --app-root $application --data-root $data --start-menu-root $startMenu --send-to-root $sendTo --uninstall-registry-path $registryPS 2>$null } finally { $ErrorActionPreference = $savedPreference }
        $deadline = (Get-Date).AddSeconds(40)
        while ((Get-Date) -lt $deadline -and (Test-Path -LiteralPath $application)) { Start-Sleep -Milliseconds 250 }
    }
    Remove-Item -LiteralPath $registryPS -Recurse -Force -ErrorAction SilentlyContinue
    if (Test-Path -LiteralPath $root) { Remove-TreeWithinAllowedRoots -Path $root -AllowedRoots @([IO.Path]::GetTempPath()) -Purpose 'native policy install cleanup' }
}
