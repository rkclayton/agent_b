[CmdletBinding()]
param()

# Item 2ge (v1.2.2/W1): what this host can actually do for dictation.
#
# The operator asked for Windows' own recogniser -- "can we just somehow hotkey
# in the Win+H function and not use their GUI" -- so this asks Windows, and the
# answer it gives is what the microphone's hover text says. There is no browser
# speech API here and no audio leaves the machine.
#
# It starts no recognition session and opens no audio device: it loads types
# and reads the installed languages, nothing more.

$ErrorActionPreference = 'Continue'
$result = [ordered]@{ available = $false; offline = $false; reason = ''; languages = @() }

try {
    $null = [Windows.Media.SpeechRecognition.SpeechRecognizer, Windows.Media, ContentType = WindowsRuntime]
} catch {
    $result.reason = 'the Windows speech recogniser is not available on this host'
    $result | ConvertTo-Json -Compress
    exit 0
}

try {
    $grammar = [Windows.Media.SpeechRecognition.SpeechRecognizer]::SupportedGrammarLanguages
    $result.languages = @($grammar | ForEach-Object { $_.LanguageTag })
    $result.offline = $result.languages.Count -gt 0
} catch {
    $result.languages = @()
}

# The blocker measured in v1.2.2/W1: driving the recogniser needs its async
# methods, and Windows PowerShell 5.1 has no reachable AsTask overload to await
# an IAsyncOperation with. The types load and the recogniser constructs; it
# cannot be RUN from here. A compiled helper is what the item names for this
# case, and until one ships the honest answer is that dictation is unavailable.
$asTask = 0
try { $asTask = @([System.WindowsRuntimeSystemExtensions].GetMethods() | Where-Object { $_.Name -eq 'AsTask' }).Count } catch { $asTask = 0 }

if ($asTask -eq 0) {
    $result.available = $false
    $result.reason = 'the Windows recogniser is installed but cannot be driven from Windows PowerShell 5.1 (no awaitable AsTask); a compiled helper is needed'
} else {
    $result.available = $true
    $result.reason = if ($result.offline) { 'the Windows recogniser is available offline on this host' } else { 'the Windows recogniser is available through the online path' }
}

$result | ConvertTo-Json -Compress
exit 0
