<#
Item 2mv (e): SEND TO -> AGENT_B.

Explorer's Send-to menu hands the selected paths to this script, which copies them
into Agent_b's exchange folder. That is the whole of it: no service, no hotkey, no
session chooser, and nothing is asked of the operator.

It runs hidden, through the same launch-hidden.vbs the sign-in start uses, so
sending a file never flashes a console window.

Two rules it keeps rather than invents:

  - the exchange folder is read FLAT, so every file lands in its root and a folder
    is copied in by name rather than nested deeper;
  - a name already taken becomes name-2.ext, which is the same rule Agent_b itself
    applies when it writes there. Nothing in the exchange folder is overwritten.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$ExchangeDirectory,
    [Parameter(ValueFromRemainingArguments = $true)][string[]]$Path
)

$ErrorActionPreference = 'Stop'

function Get-AvailablePath {
    param([string]$Candidate)
    if (-not (Test-Path -LiteralPath $Candidate)) { return $Candidate }
    $directory = Split-Path -Parent $Candidate
    $extension = [IO.Path]::GetExtension($Candidate)
    $stem = [IO.Path]::GetFileNameWithoutExtension($Candidate)
    for ($index = 2; $index -lt 10000; $index++) {
        $next = Join-Path $directory ($stem + '-' + $index + $extension)
        if (-not (Test-Path -LiteralPath $next)) { return $next }
    }
    throw "too many files named like $Candidate"
}

$attachments = [IO.Path]::GetFullPath($ExchangeDirectory)
$null = New-Item -ItemType Directory -Path $attachments -Force
$log = Join-Path (Split-Path -Parent $attachments) 'send-to.log'
$sent = 0

foreach ($one in @($Path)) {
    if ([string]::IsNullOrWhiteSpace($one)) { continue }
    try {
        $source = [IO.Path]::GetFullPath($one)
        if (-not (Test-Path -LiteralPath $source)) { throw 'no longer there' }
        $leaf = Split-Path -Leaf $source
        $target = Get-AvailablePath (Join-Path $attachments $leaf)
        # A copy INTO the folder being copied from would recurse; refuse it.
        if ($attachments.TrimEnd('\') -eq $source.TrimEnd('\')) { throw 'that is the exchange folder itself' }
        if (Test-Path -LiteralPath $source -PathType Container) {
            Copy-Item -LiteralPath $source -Destination $target -Recurse -Force
        } else {
            Copy-Item -LiteralPath $source -Destination $target -Force
        }
        $sent++
        Add-Content -LiteralPath $log -Value ("{0}  sent  {1}" -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), (Split-Path -Leaf $target))
    } catch {
        # Hidden means nobody sees a message, so every refusal is written down
        # instead. Losing a file silently is the one outcome not allowed.
        Add-Content -LiteralPath $log -Value ("{0}  NOT sent  {1}  --  {2}" -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $one, $_.Exception.Message)
    }
}

if ($sent -eq 0) { exit 1 }
exit 0
