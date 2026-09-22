[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'install-registration.ps1')
. (Join-Path $PSScriptRoot 'removal-guard.ps1')

$root = 'HKCU:\Software\Agent_b-Registration-Test-' + [Guid]::NewGuid().ToString('N')
$canonical = Join-Path $root 'Agent_b'
$stale = Join-Path $root 'Agent_b-Alpha'
$conflict = Join-Path $root 'Agent_b-Old'
$temp = Join-Path ([IO.Path]::GetTempPath()) ('Agent_b-registration-test-' + [Guid]::NewGuid().ToString('N'))
try {
    $null = New-Item -Path $canonical -Force
    $null = New-ItemProperty -Path $canonical -Name DisplayName -Value 'Agent_b' -Force
    $null = New-ItemProperty -Path $canonical -Name Publisher -Value 'rkclayton' -Force
    $null = New-ItemProperty -Path $canonical -Name InstallLocation -Value (Join-Path $temp 'canonical') -Force

    $null = New-Item -Path $stale -Force
    $null = New-ItemProperty -Path $stale -Name DisplayName -Value 'Agent_b Alpha' -Force
    $null = New-ItemProperty -Path $stale -Name Publisher -Value 'rkclayton' -Force
    $null = New-ItemProperty -Path $stale -Name InstallLocation -Value (Join-Path $temp 'absent') -Force

    $conflictRoot = Join-Path $temp 'alternate'
    $null = New-Item -ItemType Directory -Path $conflictRoot -Force
    $null = New-Item -ItemType File -Path (Join-Path $conflictRoot 'Agent_b.exe') -Force
    $null = New-Item -Path $conflict -Force
    $null = New-ItemProperty -Path $conflict -Name DisplayName -Value 'Agent_b' -Force
    $null = New-ItemProperty -Path $conflict -Name Publisher -Value 'rkclayton' -Force
    $null = New-ItemProperty -Path $conflict -Name InstallLocation -Value $conflictRoot -Force

    $registrations = @(Get-AgentBInstallRegistrations -Roots @($root) -CanonicalRegistryPath $canonical)
    if ($registrations.Count -ne 3 -or @($registrations | Where-Object IsCanonical).Count -ne 1) {
        throw 'Registration inventory did not distinguish the single canonical entry.'
    }
    try {
        $null = Get-AgentBRegistrationPreflight -Registrations $registrations
        throw 'A live alternate installation was accepted.'
    } catch {
        if ($_.Exception.Message -notmatch 'another Agent_b installation') { throw }
    }

    $conflictRegistryPath = $conflict
    Remove-Item -LiteralPath $conflictRegistryPath -Recurse -Force
    $registrations = @(Get-AgentBInstallRegistrations -Roots @($root) -CanonicalRegistryPath $canonical)
    $staleResult = @(Get-AgentBRegistrationPreflight -Registrations $registrations)
    if ($staleResult.Count -ne 1 -or $staleResult[0].DisplayName -ne 'Agent_b Alpha') {
        throw 'A stale Agent_b-owned registration was not selected for cleanup.'
    }
    Remove-AgentBStaleRegistrations -Registrations $staleResult
    if (Test-Path -LiteralPath $stale) { throw 'The selected stale registration was not removed.' }
    try {
        Remove-AgentBStaleRegistrations -Registrations @($registrations | Where-Object IsCanonical)
        throw 'The canonical registration was accepted for stale cleanup.'
    } catch {
        if ($_.Exception.Message -notmatch 'Refusing to remove a non-stale') { throw }
    }
    Write-Host 'PASS: installer registration preflight permits one canonical install, refuses a live alternate, and identifies only stale owned entries for cleanup'
} finally {
    $testRegistryPath = $root
    Remove-Item -LiteralPath $testRegistryPath -Recurse -Force -ErrorAction SilentlyContinue
    if (Test-Path -LiteralPath $temp) { Remove-TreeWithinAllowedRoots -Path $temp -AllowedRoots @([IO.Path]::GetTempPath()) -Purpose 'registration-test cleanup' }
}
