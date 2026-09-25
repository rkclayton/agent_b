[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)][string]$ApplicationDirectory,
    [Parameter(Mandatory=$true)][string]$DataDirectory,
    [Parameter(Mandatory=$true)][int]$Port
)

$ErrorActionPreference = 'Stop'
$executable = Join-Path $ApplicationDirectory 'Agent_b.exe'
$config = Join-Path $DataDirectory 'harness.json'
if (-not (Test-Path -LiteralPath $executable -PathType Leaf)) { throw "Disposable executable not found: $executable" }
if ($Port -eq 8790) { throw 'Refusing the production port.' }

$process = Start-Process -FilePath $executable -ArgumentList @('-config', $config, '-app-root', $ApplicationDirectory, '-data-root', $DataDirectory) -PassThru -WindowStyle Hidden
try {
    $base = "http://127.0.0.1:$Port"
    $ready = $false
    for ($attempt = 0; $attempt -lt 100; $attempt++) {
        try { $null = Invoke-WebRequest -UseBasicParsing -Uri "$base/chat" -TimeoutSec 1; $ready = $true; break } catch { Start-Sleep -Milliseconds 100 }
    }
    if (-not $ready) { throw 'Disposable server did not become ready.' }
    $status = Invoke-RestMethod -Uri "$base/api/speech" -TimeoutSec 20
    Write-Output ('SPEECH STATUS: ' + ($status | ConvertTo-Json -Compress))
    try {
        $stream = Invoke-WebRequest -UseBasicParsing -Uri "$base/api/speech/stream" -TimeoutSec 20
        Write-Output "SPEECH STREAM: HTTP $($stream.StatusCode), $($stream.RawContentLength) bytes"
    } catch {
        Write-Output "SPEECH STREAM ENDED: $($_.Exception.Message)"
    }
} finally {
    if (-not $process.HasExited) {
        Stop-Process -Id $process.Id -Force
        $process.WaitForExit()
    }
    Write-Output "STOPPED DISPOSABLE PID $($process.Id)"
}

$latest = Get-ChildItem -LiteralPath (Join-Path $DataDirectory 'logs') -Filter 'Agent_b-*.jsonl' | Sort-Object LastWriteTime -Descending | Select-Object -First 1
$speech = @(Get-Content -LiteralPath $latest.FullName | Where-Object { $_ -match '"type":"speech"' })
if (-not $speech.Count) { throw "No speech events were written to $($latest.FullName)" }
$speech | Write-Output
