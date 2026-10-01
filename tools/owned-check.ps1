[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$ownedRoot = Join-Path $env:USERPROFILE '.agentb'
$listPath = Join-Path $ownedRoot 'OWNED-agent_b.md'

function Complete-OwnedCheck([string]$Line, [int]$Code) {
    [Console]::Out.WriteLine($Line)
    exit $Code
}

if (-not (Test-Path -LiteralPath $listPath -PathType Leaf)) {
    Complete-OwnedCheck "owned: no list at $listPath" 2
}

$lines = [IO.File]::ReadAllLines($listPath, [Text.Encoding]::UTF8)
$header = '| name | kind | where | what | read by | stops | recreate | sha256 |'
$headerIndex = [Array]::IndexOf($lines, $header)
if ($lines.Count -lt 3 -or $lines[0] -cne "# OWNED $([char]0x2014) agent_b" -or $headerIndex -lt 1) {
    Complete-OwnedCheck 'owned: missing list-format' 1
}

$rows = [Collections.Generic.List[object]]::new()
for ($index = $headerIndex + 2; $index -lt $lines.Count; $index++) {
    if ([string]::IsNullOrWhiteSpace($lines[$index])) { continue }
    $cells = @($lines[$index].Trim().Trim('|').Split('|') | ForEach-Object { $_.Trim() })
    if ($cells.Count -ne 8 -or [string]::IsNullOrWhiteSpace($cells[0])) {
        Complete-OwnedCheck 'owned: missing list-format' 1
    }
    $rows.Add([pscustomobject]@{ Name=$cells[0]; Kind=$cells[1]; Where=$cells[2]; Sha256=$cells[7] })
}

foreach ($row in $rows) {
    $present = $false
    try {
        switch ($row.Kind) {
            { $_ -in @('file', 'repo') } {
                $present = Test-Path -LiteralPath $row.Where -PathType Leaf
                if ($present -and $row.Sha256) {
                    $present = (Get-FileHash -LiteralPath $row.Where -Algorithm SHA256).Hash.ToLowerInvariant() -ceq $row.Sha256.ToLowerInvariant()
                }
            }
            'env' { $present = -not [string]::IsNullOrWhiteSpace([Environment]::GetEnvironmentVariable($row.Where)) }
            'cert' {
                $certificate = $row.Where -split '/', 3
                if ($certificate.Count -eq 3) {
                    $present = @(Get-ChildItem -LiteralPath "Cert:\$($certificate[0])\$($certificate[1])" | Where-Object Subject -CEQ $certificate[2]).Count -gt 0
                }
            }
            'account' { $present = $null -ne (Get-LocalUser -Name $row.Where -ErrorAction SilentlyContinue) }
            'host' { $present = Test-Path -LiteralPath $row.Where }
            'manual' { $present = $true }
            'keychain' { $present = $false }
        }
    } catch { $present = $false }
    if (-not $present) { Complete-OwnedCheck "owned: missing $($row.Name)" 1 }
}

Complete-OwnedCheck "owned: ok $($rows.Count) rows" 0
