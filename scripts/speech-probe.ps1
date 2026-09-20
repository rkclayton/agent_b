[CmdletBinding()]
param()

# Item 2ge: what this host can actually do for dictation.
#
# The operator asked for Windows' own recogniser -- "can we just somehow hotkey
# in the Win+H function and not use their GUI" -- so this asks Windows, and the
# answer it gives is what the microphone's hover text says. There is no browser
# speech API here and no audio leaves the machine.
#
# v1.2.2 measured that the engine behind Win+H, the WinRT SpeechRecognizer,
# loads here but cannot be DRIVEN from Windows PowerShell 5.1: it exposes no
# reachable AsTask overload, so its async calls can never be awaited. v1.2.4
# therefore asks about System.Speech, the other recogniser Windows ships, which
# runs entirely in process and can be driven. The WinRT engine is still the
# better one and is carded for a compiled helper.
#
# This opens no audio device and starts no recognition: it constructs the engine
# to learn its name and culture, and disposes it.

$ErrorActionPreference = 'Continue'
$result = [ordered]@{ available = $false; offline = $false; reason = ''; languages = @() }

try {
    Add-Type -AssemblyName System.Speech
} catch {
    $result.reason = 'System.Speech is not available on this host'
    $result | ConvertTo-Json -Compress
    exit 0
}

try {
    $installed = @([System.Speech.Recognition.SpeechRecognitionEngine]::InstalledRecognizers())
    $result.languages = @($installed | ForEach-Object { $_.Culture.Name })
    if ($installed.Count -eq 0) {
        $result.reason = 'Windows has no speech recogniser installed'
        $result | ConvertTo-Json -Compress
        exit 0
    }
} catch {
    $result.reason = 'the installed recognisers could not be read: ' + $_.Exception.Message
    $result | ConvertTo-Json -Compress
    exit 0
}

try {
    $engine = New-Object System.Speech.Recognition.SpeechRecognitionEngine
    $null = New-Object System.Speech.Recognition.DictationGrammar
    $result.available = $true
    # System.Speech recognises in process, against a locally installed engine.
    # There is no online path in it at all, so this is offline by construction
    # rather than by a setting that could be different tomorrow.
    $result.offline = $true
    $result.reason = 'offline (System.Speech)'
    $engine.Dispose()
} catch {
    $result.reason = 'the speech engine could not be started: ' + $_.Exception.Message
}

$result | ConvertTo-Json -Compress
