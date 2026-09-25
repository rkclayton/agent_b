[CmdletBinding()]
param(
    [ValidatePattern('^[A-Za-z0-9._-]+$')]
    [string]$AccountName = 'agentb-svc',
    [Parameter(Mandatory = $true)][string]$CredentialStore,
    [switch]$ResetPassword,
    [Parameter(Mandatory = $true)][string]$ApplicationDirectory,
    [Parameter(Mandatory = $true)][string]$DataDirectory,
    [Parameter(Mandatory = $true)][string]$WorkspaceDirectory,
    [Parameter(Mandatory = $true)][string]$ExchangeDirectory,
    [Parameter(Mandatory = $true)][string]$ModelAddress,
    [ValidateRange(1, 65535)][int]$ModelPort,
    [switch]$AllowLocalNetwork,
    [string[]]$LocalSubnet = @(),
    [string[]]$AllowedRange = @()
)

$ErrorActionPreference = 'Stop'

function Test-IsAdministrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Invoke-AgentBScript {
    param([string]$Path, [string[]]$Arguments)
    $powershell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
    & $powershell -NoLogo -NoProfile -NonInteractive -File $Path @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$(Split-Path -Leaf $Path) exited $LASTEXITCODE" }
}

if (-not (Test-IsAdministrator)) { throw 'Administrator elevation is required to provision the Agent_b service identity.' }

$accountArguments = @('-AccountName', $AccountName, '-CredentialStore', $CredentialStore, '-NoPrompt', '-Confirm:$false')
if ($ResetPassword) { $accountArguments += '-ResetPassword' }
Invoke-AgentBScript -Path (Join-Path $PSScriptRoot 'setup-service-account.ps1') -Arguments $accountArguments

$protectionArguments = @(
    '-Mode', 'Apply', '-AccountName', $AccountName,
    '-ApplicationDirectory', $ApplicationDirectory, '-DataDirectory', $DataDirectory,
    '-WorkspaceDirectory', $WorkspaceDirectory, '-ExchangeDirectory', $ExchangeDirectory,
    '-ModelAddress', $ModelAddress, '-ModelPort', $ModelPort.ToString()
)
if ($AllowLocalNetwork) { $protectionArguments += '-AllowLocalNetwork' }
if ($LocalSubnet.Count) { $protectionArguments += @('-LocalSubnet', ($LocalSubnet -join ',')) }
if ($AllowedRange.Count) { $protectionArguments += @('-AllowedRange', ($AllowedRange -join ',')) }
Invoke-AgentBScript -Path (Join-Path $PSScriptRoot 'apply-hardening.ps1') -Arguments $protectionArguments

Write-Host 'AGENTB_SERVICE_IDENTITY_PROVISIONED'
exit 0
