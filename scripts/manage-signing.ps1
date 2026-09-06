[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('Create', 'Import', 'Select', 'Export', 'Verify', 'Sign')]
    [string]$Action,
    [string]$RequestBase64 = ''
)

$ErrorActionPreference = 'Stop'
Import-Module (Join-Path $PSHOME 'Modules\Microsoft.PowerShell.Security\Microsoft.PowerShell.Security.psd1') -ErrorAction Stop
Import-Module (Join-Path $PSHOME 'Modules\PKI\PKI.psd1') -ErrorAction Stop
$inputText = if ($RequestBase64) { [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($RequestBase64)) } else { [Console]::In.ReadToEnd() }
$request = if ([string]::IsNullOrWhiteSpace($inputText)) { [pscustomobject]@{} } else { $inputText | ConvertFrom-Json }
$thumbprint = ([string]$request.thumbprint -replace '[^0-9A-Fa-f]', '').ToUpperInvariant()

function Test-CanManage {
    $groups = (& whoami.exe /groups /fo csv /nh 2>$null) -join "`n"
    return $groups -match 'S-1-5-32-544'
}

function Test-IsElevated {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    return [Security.Principal.WindowsPrincipal]::new($identity).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Get-CodeSigningCertificate {
    param([string]$Value, [switch]$RequireKey)
    if ([string]::IsNullOrWhiteSpace($Value)) { return $null }
    $certificate = $null
    foreach ($store in @('Cert:\LocalMachine\My', 'Cert:\CurrentUser\My')) {
        $candidate = Get-ChildItem -LiteralPath (Join-Path $store $Value) -ErrorAction SilentlyContinue
        if ($candidate) { $certificate = $candidate; break }
    }
    if (-not $certificate) { throw "certificate $Value is not in LocalMachine\My or CurrentUser\My" }
    $codeSigning = @($certificate.EnhancedKeyUsageList | Where-Object { ([string]$_.ObjectId) -eq '1.3.6.1.5.5.7.3.3' }).Count -gt 0
    if (-not $codeSigning) { throw 'selected certificate does not have the code-signing EKU' }
    if ($RequireKey -and -not $certificate.HasPrivateKey) { throw 'selected certificate has no private key' }
    return $certificate
}

function Get-TargetFiles {
    param([string]$ApplicationRoot)
    $files = @((Join-Path $ApplicationRoot 'Agent_b.exe'))
    $files += @(Get-ChildItem -LiteralPath $ApplicationRoot -Filter '*.ps1' -File -Recurse | ForEach-Object FullName)
    return @($files | Sort-Object -Unique)
}

function Get-RelativeTargetPath([string]$Root, [string]$Path) {
    return $Path.Substring($Root.TrimEnd('\').Length).TrimStart('\')
}

function Get-Verification {
    param([string]$ApplicationRoot, [string]$Value)
    $certificate = $null
    if ($Value) { $certificate = Get-CodeSigningCertificate -Value $Value }
    $chainValid = $false
    if ($certificate) {
        $chain = [Security.Cryptography.X509Certificates.X509Chain]::new()
        try { $chainValid = $chain.Build($certificate) } finally { $chain.Dispose() }
    }
    $files = foreach ($path in Get-TargetFiles $ApplicationRoot) {
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { continue }
        $signature = Get-AuthenticodeSignature -LiteralPath $path
        [ordered]@{
            path = Get-RelativeTargetPath $ApplicationRoot $path
            status = [string]$signature.Status
            signer = if ($signature.SignerCertificate) { $signature.SignerCertificate.Subject } else { '' }
            thumbprint = if ($signature.SignerCertificate) { $signature.SignerCertificate.Thumbprint } else { '' }
            timestamped = $null -ne $signature.TimeStamperCertificate
            timestamp_by = if ($signature.TimeStamperCertificate) { $signature.TimeStamperCertificate.Subject } else { '' }
        }
    }
    $certificates = foreach ($store in @('Cert:\LocalMachine\My', 'Cert:\CurrentUser\My')) {
        foreach ($candidate in Get-ChildItem $store) {
            if (@($candidate.EnhancedKeyUsageList | Where-Object { ([string]$_.ObjectId) -eq '1.3.6.1.5.5.7.3.3' }).Count -gt 0) {
                [ordered]@{ thumbprint = $candidate.Thumbprint; subject = $candidate.Subject; has_private_key = $candidate.HasPrivateKey; store = $store }
            }
        }
    }
    return [ordered]@{
        supported = $true
        can_manage = Test-CanManage
        configured = $null -ne $certificate
        thumbprint = if ($certificate) { $certificate.Thumbprint } else { $Value }
        subject = if ($certificate) { $certificate.Subject } else { '' }
        has_private_key = [bool]($certificate -and $certificate.HasPrivateKey)
        code_signing_eku = [bool]$certificate
        chain_valid = $chainValid
        files = @($files)
        certificates = @($certificates)
    }
}

function Write-Result([object]$Value) {
    $Value | ConvertTo-Json -Depth 8 -Compress
}

$applicationRoot = Split-Path -Parent $PSScriptRoot
if ($Action -eq 'Create' -and -not (Test-IsElevated)) {
    if (-not (Test-CanManage)) { throw 'certificate creation requires an operator account that can approve UAC' }
    $before = @(Get-ChildItem Cert:\LocalMachine\My | ForEach-Object Thumbprint)
    $powershell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
    $encodedRequest = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($inputText))
    $arguments = '-NoLogo -NoProfile -File "' + $PSCommandPath + '" -Action Create -RequestBase64 "' + $encodedRequest + '"'
    $process = Start-Process -FilePath $powershell -ArgumentList $arguments -Verb RunAs -WindowStyle Hidden -Wait -PassThru
    if ($process.ExitCode -ne 0) { throw "elevated certificate creation failed with exit code $($process.ExitCode)" }
    $certificate = Get-ChildItem Cert:\LocalMachine\My | Where-Object {
        $_.Subject -eq 'CN=Agent_b Operator Code Signing' -and $_.Thumbprint -notin $before -and
        @($_.EnhancedKeyUsageList | Where-Object { ([string]$_.ObjectId) -eq '1.3.6.1.5.5.7.3.3' }).Count -gt 0
    } | Sort-Object NotBefore -Descending | Select-Object -First 1
    if (-not $certificate) { throw 'elevated certificate creation completed without a new code-signing certificate' }
    Write-Result ([ordered]@{ thumbprint = $certificate.Thumbprint; subject = $certificate.Subject; message = 'Administrator-gated certificate created and trusted for this Windows user.' })
    exit 0
}
if ($Action -eq 'Sign' -and -not (Test-IsElevated)) {
    if (-not (Test-CanManage)) { throw 'signing requires an operator account that can approve UAC' }
    $powershell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
    $encodedRequest = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($inputText))
    $arguments = '-NoLogo -NoProfile -File "' + $PSCommandPath + '" -Action Sign -RequestBase64 "' + $encodedRequest + '"'
    $null = Start-Process -FilePath $powershell -ArgumentList $arguments -Verb RunAs -WindowStyle Hidden -PassThru
    Write-Result ([ordered]@{ thumbprint = $thumbprint; subject = ''; message = 'Signing was approved; Agent_b will restart after the staged signatures verify.' })
    exit 0
}
switch ($Action) {
    'Create' {
        if (-not (Test-CanManage)) { throw 'certificate creation requires an operator account that can approve UAC' }
        $certificate = New-SelfSignedCertificate -Type CodeSigningCert -Subject 'CN=Agent_b Operator Code Signing' -CertStoreLocation 'Cert:\LocalMachine\My' -KeyAlgorithm RSA -KeyLength 3072 -HashAlgorithm SHA256 -KeyExportPolicy NonExportable -NotAfter ([DateTime]::Now.AddYears(3))
        $temp = Join-Path ([IO.Path]::GetTempPath()) ("agentb-public-{0}.cer" -f [Guid]::NewGuid().ToString('N'))
        try {
            $null = Export-Certificate -Cert $certificate -FilePath $temp -Force
            $null = Import-Certificate -FilePath $temp -CertStoreLocation 'Cert:\CurrentUser\TrustedPublisher'
            $null = Import-Certificate -FilePath $temp -CertStoreLocation 'Cert:\CurrentUser\Root'
        } finally { Remove-Item -LiteralPath $temp -Force -ErrorAction SilentlyContinue }
        Write-Result ([ordered]@{ thumbprint = $certificate.Thumbprint; subject = $certificate.Subject; message = 'Administrator-gated certificate created and its public certificate trusted for this Windows user.' })
    }
    'Import' {
        if (-not (Test-CanManage)) { throw 'certificate import requires an operator account that can approve UAC' }
        $passwordText = [string]$request.password
        if ([string]::IsNullOrEmpty($passwordText)) { throw 'PFX password is required' }
        $bytes = [Convert]::FromBase64String([string]$request.pfx_base64)
        $temp = Join-Path ([IO.Path]::GetTempPath()) ("agentb-import-{0}.pfx" -f [Guid]::NewGuid().ToString('N'))
        try {
            [IO.File]::WriteAllBytes($temp, $bytes)
            $secure = ConvertTo-SecureString -String $passwordText -AsPlainText -Force
            $certificate = Import-PfxCertificate -FilePath $temp -CertStoreLocation 'Cert:\CurrentUser\My' -Password $secure -ProtectPrivateKey
            $certificate = Get-CodeSigningCertificate -Value $certificate.Thumbprint -RequireKey
        } finally {
            $passwordText = $null; $secure = $null; [Array]::Clear($bytes, 0, $bytes.Length)
            Remove-Item -LiteralPath $temp -Force -ErrorAction SilentlyContinue
        }
        Write-Result ([ordered]@{ thumbprint = $certificate.Thumbprint; subject = $certificate.Subject; message = 'Code-signing certificate imported into CurrentUser\My; trust stores were not changed.' })
    }
    'Select' {
        $certificate = Get-CodeSigningCertificate -Value $thumbprint -RequireKey
        Write-Result ([ordered]@{ thumbprint = $certificate.Thumbprint; subject = $certificate.Subject; message = 'Existing code-signing certificate selected.' })
    }
    'Export' {
        $certificate = Get-CodeSigningCertificate -Value $thumbprint
        $temp = Join-Path ([IO.Path]::GetTempPath()) ("agentb-export-{0}.cer" -f [Guid]::NewGuid().ToString('N'))
        try {
            $null = Export-Certificate -Cert $certificate -FilePath $temp -Force
            Write-Result ([ordered]@{ cer_base64 = [Convert]::ToBase64String([IO.File]::ReadAllBytes($temp)) })
        } finally { Remove-Item -LiteralPath $temp -Force -ErrorAction SilentlyContinue }
    }
    'Verify' {
        Write-Result (Get-Verification -ApplicationRoot $applicationRoot -Value $thumbprint)
    }
    'Sign' {
        if (-not (Test-CanManage)) { throw 'signing requires an operator account that can approve UAC' }
        $expectedHash = ([string]$request.expected_hash).ToLowerInvariant()
        $processId = [int]$request.process_id
        if ($processId -le 0) { throw 'Agent_b process id is required' }
        $target = Get-CimInstance Win32_Process -Filter ("ProcessId={0}" -f $processId)
        if (-not $target -or [IO.Path]::GetFileName($target.ExecutablePath) -ne 'Agent_b.exe') { throw 'the verified Agent_b process is no longer running' }
        $actualHash = (Get-FileHash -LiteralPath $target.ExecutablePath -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actualHash -ne $expectedHash) { throw "Agent_b executable hash mismatch: expected $expectedHash, got $actualHash" }
        $certificate = Get-CodeSigningCertificate -Value $thumbprint -RequireKey
        $appRoot = Split-Path -Parent $target.ExecutablePath
        $commandLine = [string]$target.CommandLine
        $dataRoot = if ($commandLine -match '(?i)-data-root\s+"([^"]+)"') { $Matches[1] } elseif ($commandLine -match '(?i)-data-root\s+(\S+)') { $Matches[1] } else { Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'Agent_b' }
        $stage = Join-Path ([IO.Path]::GetTempPath()) ("agentb-sign-{0}" -f [Guid]::NewGuid().ToString('N'))
        $null = New-Item -ItemType Directory -Path $stage -Force
        try {
            $targets = Get-TargetFiles $appRoot
            foreach ($path in $targets) {
                $relative = Get-RelativeTargetPath $appRoot $path
                $copy = Join-Path $stage $relative
                $null = New-Item -ItemType Directory -Path (Split-Path -Parent $copy) -Force
                Copy-Item -LiteralPath $path -Destination $copy -Force
                $signature = Set-AuthenticodeSignature -LiteralPath $copy -Certificate $certificate -HashAlgorithm SHA256 -TimestampServer ([string]$request.timestamp_url)
                if ($signature.Status -ne 'Valid') { throw "signature failed for $relative`: $($signature.Status) $($signature.StatusMessage)" }
            }
            Start-Sleep -Seconds 2
            $pending = Join-Path $dataRoot 'signing-applied.pending.json'
            Stop-Process -Id $processId -Force
            Wait-Process -Id $processId -ErrorAction SilentlyContinue
            foreach ($path in $targets) {
                $relative = Get-RelativeTargetPath $appRoot $path
                Copy-Item -LiteralPath (Join-Path $stage $relative) -Destination $path -Force
            }
            $appliedAt = [DateTime]::UtcNow.ToString('o')
            [IO.File]::WriteAllText($pending, ([ordered]@{ thumbprint = $certificate.Thumbprint; signer = $certificate.Subject; timestamp_url = [string]$request.timestamp_url; timestamp = $appliedAt; files = @($targets | ForEach-Object { Get-RelativeTargetPath $appRoot $_ }); applied_at = $appliedAt } | ConvertTo-Json -Depth 5), [Text.UTF8Encoding]::new($false))
            $launcher = Join-Path $appRoot 'scripts\launch-Agent_b.ps1'
            $powershell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
            $arguments = '-NoLogo -NoProfile -File "' + $launcher + '" -ApplicationDirectory "' + $appRoot + '" -DataDirectory "' + $dataRoot + '" -Detached -NoBrowser -NoPause'
            $taskName = 'Agent_b Signed Restart ' + [Guid]::NewGuid().ToString('N')
            $taskAction = New-ScheduledTaskAction -Execute $powershell -Argument $arguments -WorkingDirectory $dataRoot
            $principal = New-ScheduledTaskPrincipal -UserId ([Security.Principal.WindowsIdentity]::GetCurrent().Name) -LogonType Interactive -RunLevel Limited
            try {
                Register-ScheduledTask -TaskName $taskName -InputObject (New-ScheduledTask -Action $taskAction -Principal $principal) -Force | Out-Null
                Start-ScheduledTask -TaskName $taskName
                Start-Sleep -Milliseconds 750
            } finally { Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue }
        } finally { Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction SilentlyContinue }
    }
}
