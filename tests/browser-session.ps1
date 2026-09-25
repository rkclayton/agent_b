function New-AgentBBrowserClient {
    param([Parameter(Mandatory = $true)][string]$BaseUri)
    $base = $BaseUri.TrimEnd('/')
    $session = New-Object Microsoft.PowerShell.Commands.WebRequestSession
    $document = Invoke-WebRequest -UseBasicParsing -Uri "$base/chat" -WebSession $session -TimeoutSec 5
    $match = [regex]::Match([string]$document.Content, '<meta name="agentb-mutation-token" content="([^"]+)">')
    if ($document.StatusCode -ne 200 -or -not $match.Success) { throw "Agent_b browser session bootstrap failed at $base/chat" }
    return [pscustomobject]@{ BaseUri = $base; Session = $session; MutationToken = $match.Groups[1].Value }
}

function Get-AgentBBrowserState {
    param([Parameter(Mandatory = $true)]$Client)
    return Invoke-RestMethod -Uri "$($Client.BaseUri)/api/state" -WebSession $Client.Session -TimeoutSec 5
}

function Test-AgentBDocumentReady {
    param([Parameter(Mandatory = $true)][string]$BaseUri, [int]$TimeoutSec = 3)
    try {
        $response = Invoke-WebRequest -UseBasicParsing -Uri ($BaseUri.TrimEnd('/') + '/chat') -TimeoutSec $TimeoutSec
        return $response.StatusCode -eq 200
    } catch { return $false }
}
