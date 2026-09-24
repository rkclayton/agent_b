[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$DataDirectory,
    [switch]$TestMode
)

$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'removal-guard.ps1')
. (Join-Path $PSScriptRoot 'install-registration.ps1')

$markerPath = Join-Path $DataDirectory 'migration-pending.json'
if (-not (Test-Path -LiteralPath $markerPath -PathType Leaf)) { exit 0 }
$marker = Get-Content -Raw -LiteralPath $markerPath | ConvertFrom-Json
$legacyRoot = [IO.Path]::GetFullPath([string]$marker.legacy_application_directory).TrimEnd('\')
$expected = [IO.Path]::GetFullPath((Join-Path $env:ProgramFiles 'Agent_b')).TrimEnd('\')
if ($TestMode) {
    $temp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
    if (-not $legacyRoot.StartsWith($temp, [StringComparison]::OrdinalIgnoreCase) -or (Split-Path -Leaf $legacyRoot) -ne 'Agent_b') {
        throw "MIGRATION CLEANUP REFUSED: disposable legacy root is outside the temporary directory: $legacyRoot"
    }
} elseif (-not $legacyRoot.Equals($expected, [StringComparison]::OrdinalIgnoreCase)) {
    throw "MIGRATION CLEANUP REFUSED: legacy root is not the canonical Program Files install: $legacyRoot"
}

if (Test-Path -LiteralPath (Join-Path $legacyRoot 'Agent_b.exe') -PathType Leaf) {
    Remove-TreeWithinAllowedRoots -Path $legacyRoot -AllowedRoots @(Split-Path -Parent $legacyRoot) -Purpose 'completed legacy installation migration'
}
$registrations = @(Get-AgentBInstallRegistrations -Roots @($marker.registration_search_roots) -CanonicalRegistryPath ([string]$marker.current_registry_path) | Where-Object {
    -not [string]::IsNullOrWhiteSpace($_.InstallLocation) -and
    [IO.Path]::GetFullPath($_.InstallLocation).TrimEnd('\').Equals($legacyRoot, [StringComparison]::OrdinalIgnoreCase)
})
if ($registrations.Count) { Remove-AgentBStaleRegistrations -Registrations $registrations }
Remove-Item -LiteralPath $markerPath -Force
Write-Host "MIGRATION COMPLETE: new per-user Agent_b started before legacy application and registration cleanup at $legacyRoot."
exit 0
