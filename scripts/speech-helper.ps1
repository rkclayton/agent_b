# Item 2ge (v1.2.4/W3) — the dictation helper.
#
# The operator asked for Windows' own dictation without its window: "can we just
# somehow hotkey in the Win+H function and not use their GUI". v1.2.2 measured
# that the engine behind Win+H, the WinRT SpeechRecognizer, cannot be driven
# from Windows PowerShell 5.1 at all - it exposes no reachable AsTask overload,
# so its async calls can never be awaited. System.Speech is the other engine
# Windows ships, it is installed here (MS-1033-80-DESK, en-US), and it CAN be
# driven: a dictation grammar, hypotheses as they arrive, a final per utterance.
#
# Everything here is local. The recogniser is an in-process Windows component
# reading an audio buffer; no audio, and no text made from it, leaves the
# machine by any path. There is no network call in this file.
#
# It writes one JSON object per line to stdout, and nothing else:
#   {"partial":"..."}  a hypothesis, while the words are still being said
#   {"final":"..."}    a recognised utterance
#   {"done":true,"reason":"silence|end-of-audio|stopped"}
#   {"error":"..."}    a reason the caller can show, then done
[CmdletBinding()]
param(
    # A wave file instead of the microphone. This is how the gate proves the
    # helper without an audio device, and it is the ONLY input the gate uses.
    [string]$WaveFile = '',
    [int]$SilenceSeconds = 10
)

$ErrorActionPreference = 'Stop'
$OutputEncoding = [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false

function Write-Line($object) {
    [Console]::Out.WriteLine(($object | ConvertTo-Json -Compress))
    [Console]::Out.Flush()
}

function Get-FriendlySpeechError([string]$Message) {
    if ($Message -match 'privacy|access.*denied|0x80070005') { return 'microphone access is off in Windows privacy settings' }
    if ($Message -match 'audio device|input device|0x8004503A|0x80004005') { return 'no input device' }
    return $Message
}

try {
    Add-Type -AssemblyName System.Speech
} catch {
    Write-Line @{ error = 'System.Speech is not available on this host'; done = $true }
    exit 0
}

$engine = $null
try {
    $engine = New-Object System.Speech.Recognition.SpeechRecognitionEngine
    $engine.LoadGrammar((New-Object System.Speech.Recognition.DictationGrammar))
    if ([string]::IsNullOrWhiteSpace($WaveFile)) {
        $engine.SetInputToDefaultAudioDevice()
        Write-Line @{ stage = 'device'; device = 'Windows default audio input' }
    } elseif (-not (Test-Path -LiteralPath $WaveFile -PathType Leaf)) {
        Write-Line @{ error = "no such wave file: $WaveFile"; done = $true }
        exit 0
    } else {
        $engine.SetInputToWaveFile($WaveFile)
    }
} catch {
    Write-Line @{ error = (Get-FriendlySpeechError $_.Exception.Message); done = $true }
    if ($engine) { $engine.Dispose() }
    exit 0
}

$null = Register-ObjectEvent -InputObject $engine -EventName SpeechHypothesized -SourceIdentifier agentbHypothesis
$null = Register-ObjectEvent -InputObject $engine -EventName SpeechRecognized -SourceIdentifier agentbRecognized
$null = Register-ObjectEvent -InputObject $engine -EventName RecognizeCompleted -SourceIdentifier agentbCompleted

$engine.RecognizeAsync([System.Speech.Recognition.RecognizeMode]::Multiple)

$lastHeard = Get-Date
$reason = 'stopped'
try {
    while ($true) {
        # Every queued event, in the order it arrived, so a partial is written
        # while the words are still being said rather than in a batch at the end.
        $events = @(Get-Event -ErrorAction SilentlyContinue)
        foreach ($event in $events) {
            $text = $null
            try { $text = $event.SourceEventArgs.Result.Text } catch { $text = $null }
            switch ($event.SourceIdentifier) {
                'agentbHypothesis' { if ($text) { Write-Line @{ partial = $text }; $lastHeard = Get-Date } }
                'agentbRecognized' { if ($text) { Write-Line @{ final = $text }; $lastHeard = Get-Date } }
                'agentbCompleted' { $reason = 'end-of-audio' }
            }
            Remove-Event -EventIdentifier $event.EventIdentifier -ErrorAction SilentlyContinue
        }
        if ($reason -eq 'end-of-audio') { break }
        # Ten seconds of silence stops it, which is what the item asks for and
        # what keeps a forgotten microphone from listening all afternoon.
        if (((Get-Date) - $lastHeard).TotalSeconds -ge $SilenceSeconds) { $reason = 'silence'; break }
        Start-Sleep -Milliseconds 50
    }
} finally {
    try { $engine.RecognizeAsyncStop() } catch {}
    Unregister-Event -SourceIdentifier agentbHypothesis -ErrorAction SilentlyContinue
    Unregister-Event -SourceIdentifier agentbRecognized -ErrorAction SilentlyContinue
    Unregister-Event -SourceIdentifier agentbCompleted -ErrorAction SilentlyContinue
    $engine.Dispose()
}

Write-Line @{ done = $true; reason = $reason }
