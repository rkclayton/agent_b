[CmdletBinding()]
param(
    [ValidatePattern('^[A-Za-z0-9._-]+$')]
    [string]$AccountName = 'agentb-svc',
    [string]$CredentialStore,
    [switch]$ResetPassword,
    [string]$ApplicationDirectory,
    [string]$DataDirectory,
    [string]$WorkspaceDirectory,
    [string]$ExchangeDirectory,
    [string]$ModelAddress,
    [ValidateRange(1, 65535)][int]$ModelPort,
    [switch]$AllowLocalNetwork,
    [string[]]$LocalSubnet = @(),
    [string[]]$AllowedRange = @(),
    # Item 2li (c): one invocation, no UI, no prompt, nothing that waits on a
    # desktop. A management tool runs this as SYSTEM on every machine on every
    # pass; (d) is why it may do that safely.
    [switch]$Unattended,
    # Item 2li (f): the same tool undoes it.
    [switch]$RemoveMachineCredential,
    # Item 2kk (a): where to write the result. The caller names it; this script
    # writes what happened there, and the caller reports FROM THE FILE rather
    # than from a stream it had to parse.
    [string]$ResultFile
)

$ErrorActionPreference = 'Stop'
# Item 2kk (b): a PS 5.1 child serializes progress and verbose records to
# stderr as `#< CLIXML`. Nothing here produces them.
$ProgressPreference = 'SilentlyContinue'
$VerbosePreference = 'SilentlyContinue'
$InformationPreference = 'SilentlyContinue'

# Item 2li (c): the result is a structured line, not a prose summary a management
# tool has to scrape. It follows the marker-line discipline [[2kk]] and [[2lb]]
# established, so a leading banner or a trailing warning cannot break the parse.
$script:resultMarker = 'AGENTB_PROVISION_RESULT'

function Write-ProvisionResult {
    param([string]$Outcome, [string]$Message, [hashtable]$Detail = @{})
    $payload = [ordered]@{ outcome = $Outcome; account = $AccountName; message = $Message }
    foreach ($key in $Detail.Keys) { $payload[$key] = $Detail[$key] }
    $payload['ok'] = ($Outcome -eq 'ready' -or $Outcome -eq 'removed' -or $Outcome -eq 'unchanged')
    $json = $payload | ConvertTo-Json -Depth 5 -Compress
    Write-Output ("$script:resultMarker " + $json)
    # Item 2kk (a): the same payload as a FILE. stdout is discarded for an
    # elevated child -- Start-Process redirects nothing -- so the line above is
    # invisible on that route and the file is the only thing the caller reads.
    if ($ResultFile) {
        try {
            $directory = Split-Path -Parent $ResultFile
            if ($directory -and -not (Test-Path -LiteralPath $directory)) { $null = New-Item -ItemType Directory -Path $directory -Force }
            [IO.File]::WriteAllText($ResultFile, $json, [Text.UTF8Encoding]::new($false))
        } catch {
            # A result file that cannot be written must not become the failure:
            # the exit code still carries the outcome.
            Write-Host "RESULT FILE NOT WRITTEN: $($_.Exception.Message)"
        }
    }
}

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

# The machine-scoped blob's name and its access list are the Go side's, in
# internal/credential: the filename IS the scope, and the list is exactly
# Administrators, SYSTEM and the service account. Both halves are restated here
# because this script writes the file that Go later refuses if the list is wrong.
$script:machineCredentialName = '.agentb-shell-credential-machine.dpapi'

function Get-MachineCredentialPath {
    if ([string]::IsNullOrWhiteSpace($DataDirectory)) { throw 'PROVISION REFUSED: -DataDirectory is required to locate the machine credential.' }
    return Join-Path ([IO.Path]::GetFullPath($DataDirectory)) $script:machineCredentialName
}

function Set-MachineCredentialAccessList {
    param([Parameter(Mandatory)][string]$Path, [string]$AccountSid)
    $sddl = 'D:P(A;;FA;;;BA)(A;;FA;;;SY)'
    if ($AccountSid) { $sddl += "(A;;FR;;;$AccountSid)" }
    $acl = New-Object Security.AccessControl.FileSecurity
    $acl.SetSecurityDescriptorSddlForm($sddl)
    Set-Acl -LiteralPath $Path -AclObject $acl
}

# Item 2li (b) and @keep: the password is generated here and never shown, never
# typed and never placed on a command line. It reaches setup-service-account.ps1
# as a file path, exactly as the operator's own Settings path already does.
function New-MachineCredential {
    param([Parameter(Mandatory)][string]$Path, [string]$AccountSid)
    $bytes = [byte[]]::new(24)
    [Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
    # Base64 of 24 bytes is 32 characters, comfortably past the 14-character
    # floor setup-service-account.ps1 enforces, and it cannot look like a pasted
    # command.
    $password = [Convert]::ToBase64String($bytes)
    $plain = [Text.Encoding]::UTF8.GetBytes($password)
    try {
        Add-Type -AssemblyName System.Security
        $protected = [Security.Cryptography.ProtectedData]::Protect(
            $plain, $null, [Security.Cryptography.DataProtectionScope]::LocalMachine)
        $directory = Split-Path -Parent $Path
        if (-not (Test-Path -LiteralPath $directory)) { $null = New-Item -ItemType Directory -Path $directory -Force }
        # Written, then locked down, then published -- the same order the Go
        # store uses, and for the same reason: a list that excludes the writer
        # takes away the rename it needs on its own temporary file.
        $temporary = "$Path.provision"
        [IO.File]::WriteAllBytes($temporary, $protected)
        [IO.File]::Copy($temporary, $Path, $true)
        Remove-Item -LiteralPath $temporary -Force -ErrorAction SilentlyContinue
        Set-MachineCredentialAccessList -Path $Path -AccountSid $AccountSid
    } finally {
        [Array]::Clear($plain, 0, $plain.Length)
        $password = $null
    }
}

function Get-AccountSid {
    param([string]$Name)
    try { return (New-Object Security.Principal.NTAccount($env:COMPUTERNAME, $Name)).Translate([Security.Principal.SecurityIdentifier]).Value }
    catch { return $null }
}

# ---------------------------------------------------------------- (f) removal

if ($RemoveMachineCredential) {
    if (-not (Test-IsAdministrator)) {
        Write-ProvisionResult -Outcome 'refused' -Message 'Administrator or SYSTEM rights are required to remove the machine-scoped credential.'
        exit 1
    }
    $path = Get-MachineCredentialPath
    if (Test-Path -LiteralPath $path -PathType Leaf) {
        Remove-Item -LiteralPath $path -Force
        Write-ProvisionResult -Outcome 'removed' -Message 'the machine-scoped credential was removed; every user of this machine falls back to their own.' -Detail @{ path = $path }
    } else {
        Write-ProvisionResult -Outcome 'unchanged' -Message 'there was no machine-scoped credential to remove.' -Detail @{ path = $path }
    }
    exit 0
}

# ------------------------------------------------------------- (c) unattended

if ($Unattended) {
    foreach ($required in @('ApplicationDirectory', 'DataDirectory', 'WorkspaceDirectory', 'ExchangeDirectory', 'ModelAddress')) {
        if ([string]::IsNullOrWhiteSpace((Get-Variable -Name $required -ValueOnly))) {
            Write-ProvisionResult -Outcome 'refused' -Message "-$required is required."
            exit 1
        }
    }
    if (-not (Test-IsAdministrator)) {
        Write-ProvisionResult -Outcome 'refused' -Message 'Administrator or SYSTEM rights are required to provision the service identity.'
        exit 1
    }
    if ($CredentialStore) {
        # (a): this mode's whole purpose is the machine scope. A caller handing
        # it a user-scoped blob would produce exactly the state 2kg calls
        # "account exists, credential missing" for every other user.
        Write-ProvisionResult -Outcome 'refused' -Message '-CredentialStore is not accepted in unattended mode; the credential is generated and stored machine-scoped.'
        exit 1
    }

    $machinePath = Get-MachineCredentialPath
    $existing = Get-LocalUser -Name $AccountName -ErrorAction SilentlyContinue

    # (d), idempotence. A management tool runs this on every pass, so the first
    # question is whether anything needs doing at all: an account that exists
    # with a machine credential that logs on is already the finished state.
    if ($existing -and (Test-Path -LiteralPath $machinePath -PathType Leaf)) {
        $check = & (Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe') `
            -NoLogo -NoProfile -NonInteractive -File (Join-Path $PSScriptRoot 'setup-service-account.ps1') `
            -AccountName $AccountName -CredentialStore $machinePath -ValidateCredentialStore 2>&1
        if ($LASTEXITCODE -eq 0) {
            Write-ProvisionResult -Outcome 'ready' -Message 'the service identity was already provisioned; nothing was changed.' -Detail @{ changed = $false; scope = 'machine'; credential = $machinePath }
            exit 0
        }
    }

    # 2kg's states decide the rest: create when absent, adopt by password reset
    # when present. Either way the password is new, generated here, and stored
    # machine-scoped before the account is touched, so a failure half way leaves
    # a credential this machine can still read rather than one nobody can.
    New-MachineCredential -Path $machinePath -AccountSid (Get-AccountSid -Name $AccountName)

    $accountArguments = @('-AccountName', $AccountName, '-CredentialStore', $machinePath, '-NoPrompt', '-Confirm:$false')
    if ($existing) { $accountArguments += '-ResetPassword' }
    try {
        Invoke-AgentBScript -Path (Join-Path $PSScriptRoot 'setup-service-account.ps1') -Arguments $accountArguments
    } catch {
        Remove-Item -LiteralPath $machinePath -Force -ErrorAction SilentlyContinue
        Write-ProvisionResult -Outcome 'failed' -Message "the account could not be $(if ($existing) { 'adopted' } else { 'created' }): $($_.Exception.Message)"
        exit 2
    }

    # The account now exists, so its SID does too: the access list is rewritten
    # to include it, which the first write could not do when the account was new.
    Set-MachineCredentialAccessList -Path $machinePath -AccountSid (Get-AccountSid -Name $AccountName)

    $protectionArguments = @(
        '-Mode', 'Apply', '-AccountName', $AccountName,
        '-ApplicationDirectory', $ApplicationDirectory, '-DataDirectory', $DataDirectory,
        '-WorkspaceDirectory', $WorkspaceDirectory, '-ExchangeDirectory', $ExchangeDirectory,
        '-ModelAddress', $ModelAddress, '-ModelPort', $ModelPort.ToString()
    )
    if ($AllowLocalNetwork) { $protectionArguments += '-AllowLocalNetwork' }
    if ($LocalSubnet.Count) { $protectionArguments += @('-LocalSubnet', ($LocalSubnet -join ',')) }
    if ($AllowedRange.Count) { $protectionArguments += @('-AllowedRange', ($AllowedRange -join ',')) }
    try {
        Invoke-AgentBScript -Path (Join-Path $PSScriptRoot 'apply-hardening.ps1') -Arguments $protectionArguments
    } catch {
        Write-ProvisionResult -Outcome 'failed' -Message "the account was provisioned but its protections were not applied: $($_.Exception.Message)" -Detail @{ credential = $machinePath }
        exit 2
    }

    # (c)'s verification: a real logon, through the same LogonUser path the
    # interactive route uses. rel-1.15.0/W0 confirmed it needs a token, not a
    # desktop, so this runs under SYSTEM like everything else here.
    & (Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe') `
        -NoLogo -NoProfile -NonInteractive -File (Join-Path $PSScriptRoot 'setup-service-account.ps1') `
        -AccountName $AccountName -CredentialStore $machinePath -ValidateCredentialStore | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Write-ProvisionResult -Outcome 'failed' -Message 'the identity was provisioned but the credential did not verify by a real logon.' -Detail @{ credential = $machinePath }
        exit 2
    }

    Write-ProvisionResult -Outcome 'ready' -Message "the service identity was $(if ($existing) { 'adopted' } else { 'created' }) and verified by a real logon." -Detail @{ changed = $true; scope = 'machine'; credential = $machinePath }
    exit 0
}

# ---------------------------------------------------- the operator's own path

# Item 2li (e): unchanged. Settings still provisions user-scoped for someone with
# admin rights, through exactly the arguments it always passed.
foreach ($required in @('CredentialStore', 'ApplicationDirectory', 'DataDirectory', 'WorkspaceDirectory', 'ExchangeDirectory', 'ModelAddress')) {
    if ([string]::IsNullOrWhiteSpace((Get-Variable -Name $required -ValueOnly))) {
        throw "-$required is required."
    }
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
