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
            throw "Installation refused: another Agent_b installation is registered at $location. Remove it through Windows Installed apps before installing this copy."
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
        Remove-Item -LiteralPath $registration.RegistryPath -Recurse -Force
    }
}
