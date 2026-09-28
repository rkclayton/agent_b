# Item 2ng (a): THE ELEVATED CHILD'S STREAMS ARE CAPTURED.
#
# The launcher used to elevate the provisioning script directly, through
# `Start-Process -Verb RunAs -WindowStyle Hidden`, which redirects nothing. Anything the
# child said went to a console nobody could see. The script's own result file covered
# every failure INSIDE its body — but a failure before the body, a parameter that will not
# bind, an execution-policy refusal, a crash on load, happens above the code that writes
# that file. The operator pressed Repair on 2026-09-27 at 19:44 and got a 25-byte log
# holding one marker line and a message that could only say the setup "did not complete".
#
# This wrapper is what gets elevated now. It runs the real script with every stream
# appended to the per-run log the message already names, so a pre-body failure lands in
# the same file as everything else and the console can quote its first line.
#
# It is deliberately thin. It makes no decisions, writes no result of its own and knows
# nothing about accounts: the script it runs keeps all of that, and the marker discipline
# items 2kk and 2lb established is untouched.
[CmdletBinding()]
param(
    # The per-run log the launcher already tells the operator about.
    [Parameter(Mandatory)][string]$Log,
    # The provisioning script to run, and its arguments verbatim.
    [Parameter(Mandatory)][string]$Script,
    [Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments = @()
)

# Not Stop: a non-terminating warning from the child must not end the wrapper before it
# has propagated the child's exit code, which is what the launcher judges.
$ErrorActionPreference = 'Continue'
# Item 2kk (b): progress and verbose records serialize as CLIXML on stderr in PS 5.1.
# Nothing here should produce them, and the child is told the same.
$ProgressPreference = 'SilentlyContinue'
$VerbosePreference = 'SilentlyContinue'
$InformationPreference = 'SilentlyContinue'

$powershell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
if (-not (Test-Path -LiteralPath $powershell)) { $powershell = 'powershell.exe' }

# ONE ENCODING, DELIBERATELY. `*>> $Log` was the obvious way to write this and it is
# wrong: Windows PowerShell 5.1 redirects to UTF-16, so the file came out as a mix of
# encodings that the Go side could not read as text at all -- the first version of this
# produced a log the harness saw as binary. Every line goes through Add-Content with a
# named encoding instead, streamed rather than buffered so a killed wrapper still leaves
# what the child had said.
function Write-CapturedLine {
    param([string]$Line)
    Add-Content -LiteralPath $Log -Value $Line -Encoding UTF8
}

Write-CapturedLine "AGENTB_ELEVATED_WRAPPER_STARTED $(Get-Date -Format o)"

# Every stream of the child, line by line. A binding error appears here because it is
# the INNER process that fails to bind, and 2>&1 brings its stderr into the pipeline
# with the rest.
& $powershell -NoLogo -NoProfile -NonInteractive -File $Script @Arguments 2>&1 |
    Out-String -Stream |
    ForEach-Object { Write-CapturedLine $_ }
$childExit = $LASTEXITCODE

Write-CapturedLine "AGENTB_ELEVATED_WRAPPER_EXIT $childExit"
exit $childExit
