$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'install-root-policy.ps1')

$suiteRoot = Join-Path ([IO.Path]::GetTempPath()) ('Agent_b-root-policy-' + [Guid]::NewGuid().ToString('N'))
$safe = @{
    TestMode = $true
    TestRoot = $suiteRoot
    ApplicationDirectory = Join-Path $suiteRoot 'Application\Agent_b'
    DataDirectory = Join-Path $suiteRoot 'Data\Agent_b'
    WorkspaceDirectory = Join-Path $suiteRoot 'Workspace\workspace'
    StartMenuDirectory = Join-Path $suiteRoot 'StartMenu'
    UninstallRegistryPath = 'HKCU:\Software\Agent_b-Test-root-policy'
}
$resolved = Resolve-AgentBInstallRoots @safe
if ($resolved.LegacyApplicationDirectory) { throw 'TestMode inferred a legacy migration root without an explicit disposable path.' }

$productionData = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'Agent_b'
$cases = @(
    @{ Name = 'application Program Files'; Changes = @{ ApplicationDirectory = 'C:\Program Files\Agent_b' } },
    @{ Name = 'data production root'; Changes = @{ DataDirectory = $productionData } },
    @{ Name = 'workspace production root'; Changes = @{ WorkspaceDirectory = (Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'Agent_b-workspace') } },
    @{ Name = 'real uninstall registry'; Changes = @{ UninstallRegistryPath = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\Agent_b' } },
    @{ Name = 'migration Program Files seam'; Changes = @{ LegacyApplicationDirectory = 'C:\Program Files\Agent_b' } }
)
foreach ($case in $cases) {
    $arguments = @{} + $safe
    foreach ($entry in $case.Changes.GetEnumerator()) { $arguments[$entry.Key] = $entry.Value }
    try {
        $null = Resolve-AgentBInstallRoots @arguments
        throw "resolver accepted forbidden TestMode input: $($case.Name)"
    } catch {
        if ($_.Exception.Message -like 'resolver accepted*') { throw }
    }
}

$safe.LegacyApplicationDirectory = Join-Path $suiteRoot 'Legacy\Agent_b'
$resolved = Resolve-AgentBInstallRoots @safe
if (-not $resolved.LegacyApplicationDirectory.StartsWith($suiteRoot, [StringComparison]::OrdinalIgnoreCase)) { throw 'resolver lost the explicit disposable migration seam' }
Write-Host 'PASS TestMode resolver rejects production application, data, workspace, registry, and migration roots'
