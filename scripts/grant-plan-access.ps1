[CmdletBinding(SupportsShouldProcess = $true)]
param(
    [ValidatePattern('^[A-Za-z0-9._-]+$')]
    [string]$AccountName = 'agentb-svc',
    [Parameter(Mandatory = $true)]
    [string]$Repository
)

$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath($Repository).TrimEnd('\')
if (-not (Test-Path -LiteralPath $root -PathType Container)) { throw "plan repository does not exist: $root" }
$account = [Security.Principal.NTAccount]::new($env:COMPUTERNAME, $AccountName)
$sid = $account.Translate([Security.Principal.SecurityIdentifier])
$acl = Get-Acl -LiteralPath $root
$rights = [Security.AccessControl.FileSystemRights]::Modify
$inheritance = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit
$propagation = [Security.AccessControl.PropagationFlags]::None
$rule = [Security.AccessControl.FileSystemAccessRule]::new($sid, $rights, $inheritance, $propagation, [Security.AccessControl.AccessControlType]::Allow)
if ($PSCmdlet.ShouldProcess($root, "Grant $AccountName inherited Modify access for the approved plan repository")) {
    $acl.SetAccessRule($rule)
    Set-Acl -LiteralPath $root -AclObject $acl
}
Write-Host "GRANTED: $AccountName Modify :: $root"
