function Get-AgentBResolvedPath {
    param([Parameter(Mandatory)][string]$Path)
    return [IO.Path]::GetFullPath([Environment]::ExpandEnvironmentVariables($Path)).TrimEnd('\')
}

function Test-AgentBPathInside {
    param([string]$Child, [string]$Parent)
    return $Child.StartsWith($Parent.TrimEnd('\') + '\', [StringComparison]::OrdinalIgnoreCase)
}

function Resolve-AgentBInstallRoots {
    [CmdletBinding()]
    param(
        [switch]$AllUsers,
        [switch]$TestMode,
        [string]$ApplicationDirectory,
        [string]$DataDirectory,
        [string]$WorkspaceDirectory,
        [string]$StartMenuDirectory,
        [string]$SendToDirectory,
        [string]$UninstallRegistryPath,
        [string]$LegacyApplicationDirectory,
        [string]$OperatorLocalAppData = [Environment]::GetFolderPath('LocalApplicationData'),
        [string]$TestRoot = [IO.Path]::GetTempPath()
    )
    $legacyExplicit = -not [string]::IsNullOrWhiteSpace($LegacyApplicationDirectory)
    if ([string]::IsNullOrWhiteSpace($DataDirectory)) { $DataDirectory = Join-Path $OperatorLocalAppData 'Agent_b' }
    if ([string]::IsNullOrWhiteSpace($ApplicationDirectory)) { $ApplicationDirectory = if ($AllUsers) { Join-Path $env:ProgramFiles 'Agent_b' } else { Join-Path $OperatorLocalAppData 'Programs\Agent_b' } }
    if ([string]::IsNullOrWhiteSpace($WorkspaceDirectory)) { $WorkspaceDirectory = if ($AllUsers) { Join-Path $env:ProgramData 'Agent_b\workspace' } else { Join-Path $OperatorLocalAppData 'Agent_b-workspace' } }
    if ([string]::IsNullOrWhiteSpace($StartMenuDirectory)) { $StartMenuDirectory = Join-Path ([Environment]::GetFolderPath('StartMenu')) 'Programs' }
    # Item 2mv (e): the Send-to folder must be THE OPERATOR'S, never the account an
    # over-the-shoulder elevation ran as. GetFolderPath('SendTo') would return the
    # elevated account's, so it is derived from the operator's LocalAppData instead,
    # which the installer is already given for exactly this reason. Both resolve to
    # <profile>\AppData\Roaming\Microsoft\Windows\SendTo when they agree.
    if ([string]::IsNullOrWhiteSpace($SendToDirectory)) {
        if ($TestMode) {
            # A test never touches the operator's real Send-to folder, so TestMode
            # gets a disposable one beside the disposable Start menu.
            $SendToDirectory = Join-Path $StartMenuDirectory 'SendTo'
        } else {
            $SendToDirectory = Join-Path (Split-Path -Parent (Get-AgentBResolvedPath $OperatorLocalAppData)) 'Roaming\Microsoft\Windows\SendTo'
        }
    }
    if ([string]::IsNullOrWhiteSpace($UninstallRegistryPath)) { $UninstallRegistryPath = if ($AllUsers) { 'HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\Agent_b' } else { 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\Agent_b' } }
    if (-not $legacyExplicit) { $LegacyApplicationDirectory = Join-Path $env:ProgramFiles 'Agent_b' }

    $resolved = [ordered]@{
        ApplicationDirectory = Get-AgentBResolvedPath $ApplicationDirectory
        DataDirectory = Get-AgentBResolvedPath $DataDirectory
        WorkspaceDirectory = Get-AgentBResolvedPath $WorkspaceDirectory
        StartMenuDirectory = Get-AgentBResolvedPath $StartMenuDirectory
        SendToDirectory = Get-AgentBResolvedPath $SendToDirectory
        UninstallRegistryPath = $UninstallRegistryPath.Replace('/', '\')
        LegacyApplicationDirectory = $(if ($legacyExplicit -or -not $TestMode) { Get-AgentBResolvedPath $LegacyApplicationDirectory } else { $null })
        LegacyApplicationExplicit = $legacyExplicit
    }
    if ($TestMode) {
        $testRootFull = Get-AgentBResolvedPath $TestRoot
        foreach ($name in @('ApplicationDirectory', 'DataDirectory', 'WorkspaceDirectory', 'StartMenuDirectory', 'SendToDirectory')) {
            if (-not (Test-AgentBPathInside $resolved[$name] $testRootFull)) {
                throw "TestMode $name must stay beneath the disposable suite root: $($resolved[$name])"
            }
        }
        if ($resolved.LegacyApplicationDirectory -and -not (Test-AgentBPathInside $resolved.LegacyApplicationDirectory $testRootFull)) {
            throw "TestMode LegacyApplicationDirectory must stay beneath the disposable suite root: $($resolved.LegacyApplicationDirectory)"
        }
        if (-not $resolved.UninstallRegistryPath.StartsWith('HKCU:\Software\', [StringComparison]::OrdinalIgnoreCase) -or
            $resolved.UninstallRegistryPath -notmatch '(?i)Agent_b[^\\]*(?:Test|Acceptance|[0-9a-f]{16,})') {
            throw "TestMode UninstallRegistryPath must use a disposable Agent_b test or acceptance key: $($resolved.UninstallRegistryPath)"
        }
    }
    return [pscustomobject]$resolved
}
