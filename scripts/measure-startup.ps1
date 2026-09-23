[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Exe,
    [Parameter(Mandatory = $true)][string]$ApplicationRoot,
    [Parameter(Mandatory = $true)][string]$DataRoot,
    [Parameter(Mandatory = $true)][int]$Port,
    [int]$Count = 5
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'agentb-stop.ps1')
$config = Join-Path $DataRoot 'harness.json'
$startup = Join-Path $DataRoot 'startup.log'
$samples = @()
foreach ($number in 1..$Count) {
    $watch = [Diagnostics.Stopwatch]::StartNew()
    $process = Start-Process -FilePath $Exe -ArgumentList @('-config', $config, '-data-root', $DataRoot, '-app-root', $ApplicationRoot, '-startup-log', $startup) -PassThru -WindowStyle Hidden
    $ready = $false
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    do {
        Start-Sleep -Milliseconds 50
        try {
            $state = Invoke-RestMethod "http://127.0.0.1:$Port/api/state" -TimeoutSec 1
            $ready = $state.process_id -eq $process.Id
        } catch { }
    } until ($ready -or [DateTime]::UtcNow -gt $deadline)
    if (-not $ready) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        throw "timing launch $number did not become ready"
    }
    $samples += [pscustomobject]@{ run = $number; pid = $process.Id; ready_ms = $watch.ElapsedMilliseconds }
    $null = Request-AgentbGracefulStop -ApplicationRoot $ApplicationRoot -ProcessId $process.Id
    if (-not $process.WaitForExit(15000)) {
        Stop-Process -Id $process.Id -Force
        throw "timing launch $number did not stop"
    }
    Start-Sleep -Milliseconds 150
}
[pscustomobject]@{
    samples = $samples
    phases = @(Select-String -Path $startup -Pattern 'startup phases:' | ForEach-Object { $_.Line })
} | ConvertTo-Json -Depth 5
