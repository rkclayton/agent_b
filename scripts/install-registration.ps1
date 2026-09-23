function Get-AgentBInstallRegistrations {
    param(
        [string[]]$Roots,
        [string]$CanonicalRegistryPath
    )

    $canonicalLeaf = Split-Path -Leaf $CanonicalRegistryPath
    foreach ($root in @($Roots)) {
        if ([string]::IsNullOrWhiteSpace($root) -or -not (Test-Path -LiteralPath $root -PathType Container)) { continue }
        foreach ($key in @(Get-ChildItem -LiteralPath $root -ErrorAction Stop)) {
            $property = Get-ItemProperty -LiteralPath $key.PSPath -ErrorAction Stop
            if ([string]$property.Publisher -cne 'rkclayton' -or
                [string]$property.DisplayName -notin @('Agent_b', 'Agent_b Alpha')) { continue }
            $location = [string]$property.InstallLocation
            $executable = if ([string]::IsNullOrWhiteSpace($location)) { '' } else { Join-Path $location 'Agent_b.exe' }
            [pscustomobject]@{
                RegistryPath = $key.PSPath
                DisplayName = [string]$property.DisplayName
                InstallLocation = $location
                Executable = $executable
                UninstallString = [string]$property.UninstallString
                QuietUninstallString = [string]$property.QuietUninstallString
                IsCanonical = $key.PSChildName.Equals($canonicalLeaf, [StringComparison]::OrdinalIgnoreCase) -and
                    $root.Equals((Split-Path -Parent $CanonicalRegistryPath), [StringComparison]::OrdinalIgnoreCase)
                HasExecutable = -not [string]::IsNullOrWhiteSpace($executable) -and (Test-Path -LiteralPath $executable -PathType Leaf)
            }
        }
    }
}

function Get-AgentBRegistrationPreflight {
    param([object[]]$Registrations)

    $stale = @()
    foreach ($registration in @($Registrations)) {
        if ($registration.IsCanonical) { continue }
        if ($registration.HasExecutable) {
            $location = if ([string]::IsNullOrWhiteSpace($registration.InstallLocation)) { $registration.Executable } else { $registration.InstallLocation }
            $uninstall = if (-not [string]::IsNullOrWhiteSpace($registration.UninstallString)) { $registration.UninstallString } else { $registration.QuietUninstallString }
            if ([string]::IsNullOrWhiteSpace($uninstall)) { $uninstall = 'Remove it through Windows Installed apps.' }
            throw "Installation refused: $($registration.DisplayName) is registered at $location. Uninstall command: $uninstall"
        }
        $stale += $registration
    }
    return @($stale)
}

function Remove-AgentBStaleRegistrations {
    param([object[]]$Registrations)

    foreach ($registration in @($Registrations)) {
        if ($registration.IsCanonical -or $registration.HasExecutable) {
            throw "Refusing to remove a non-stale Agent_b registration: $($registration.RegistryPath)"
        }
        $staleRegistryPath = [string]$registration.RegistryPath
        Remove-Item -LiteralPath $staleRegistryPath -Recurse -Force
    }
}
