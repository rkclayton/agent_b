[CmdletBinding()]
param(
	[Parameter(Mandatory = $true)]
	[string]$Commit
)

$ErrorActionPreference = 'Stop'
$alphaRoot = 'C:\alpha'
$sourceRoot = 'C:\alpha\src'
$applicationRoot = 'C:\alpha\Program Files\Agent_b'
$dataRoot = 'C:\alpha\LocalAppData\Agent_b'
$workspaceRoot = 'C:\alpha\ProgramData\Agent_b\workspace'
$startMenuRoot = Join-Path ([Environment]::GetFolderPath('StartMenu')) 'Programs\Agent_b Alpha'
$uninstallKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\Agent_b-Alpha'
$alphaURL = 'http://127.0.0.1:7337/'
$repositoryRoot = Split-Path -Parent $PSScriptRoot

function Quote-ProcessArgument {
	param([string]$Value)
	return '"' + $Value.Replace('"', '\"') + '"'
}

function Test-IsAdministrator {
	$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
	$principal = [Security.Principal.WindowsPrincipal]::new($identity)
	return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Invoke-Git {
	param([string[]]$Arguments)
	& git.exe @Arguments
	if ($LASTEXITCODE -ne 0) { throw "git $($Arguments -join ' ') failed with exit code $LASTEXITCODE." }
}

function Get-AlphaProcesses {
	if (-not (Test-Path -LiteralPath $applicationRoot -PathType Container)) { return @() }
	$executable = [IO.Path]::GetFullPath((Join-Path $applicationRoot 'Agent_b.exe'))
	return @(Get-CimInstance Win32_Process -Filter "Name='Agent_b.exe'" -ErrorAction SilentlyContinue | Where-Object {
		$_.ExecutablePath -and [IO.Path]::GetFullPath($_.ExecutablePath).Equals($executable, [StringComparison]::OrdinalIgnoreCase)
	})
}

if (-not [IO.Path]::GetFullPath($alphaRoot).Equals('C:\alpha', [StringComparison]::OrdinalIgnoreCase)) {
	throw "Refusing unexpected alpha root: $alphaRoot"
}
$resolved = (& git.exe -C $repositoryRoot rev-parse "$Commit^{commit}" 2>$null)
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($resolved)) { throw "Commit is not available locally: $Commit" }
$resolved = $resolved.Trim()

$null = New-Item -ItemType Directory -Path $alphaRoot -Force
if (Test-Path -LiteralPath $sourceRoot -PathType Container) {
	if (-not (Test-Path -LiteralPath (Join-Path $sourceRoot '.git'))) { throw "$sourceRoot exists but is not a git worktree." }
	if (@(& git.exe -C $sourceRoot status --porcelain --untracked-files=normal).Count -gt 0) { throw "$sourceRoot has local changes; refusing to move it." }
	Invoke-Git @('-C', $sourceRoot, 'switch', '--detach', $resolved)
} else {
	Invoke-Git @('-C', $repositoryRoot, 'worktree', 'add', '--detach', $sourceRoot, $resolved)
}
if ((& git.exe -C $sourceRoot rev-parse HEAD).Trim() -ne $resolved) { throw 'Alpha worktree did not move to the requested commit.' }
if (@(& git.exe -C $sourceRoot status --porcelain --untracked-files=normal).Count -gt 0) { throw 'Alpha worktree is not clean before build.' }

$go = Join-Path $repositoryRoot '.tools\go\bin\go.exe'
if (-not (Test-Path -LiteralPath $go -PathType Leaf)) {
	$go = (Get-Command go.exe -ErrorAction SilentlyContinue).Source
}
if (-not $go) { throw 'Go 1.24 or newer was not found.' }
$output = Join-Path $sourceRoot 'Agent_b.exe'
$ldflags = "-X harness/internal/buildinfo.Commit=$resolved -X harness/internal/buildinfo.Dirty=false"
Push-Location $sourceRoot
try {
	& $go build -ldflags $ldflags -o $output ./cmd/harness
	if ($LASTEXITCODE -ne 0) { throw "Alpha build failed with exit code $LASTEXITCODE." }
} finally { Pop-Location }

$alphaProcesses = Get-AlphaProcesses
foreach ($process in $alphaProcesses) { Stop-Process -Id $process.ProcessId -Force }
if ($alphaProcesses.Count) {
	$deadline = [DateTime]::UtcNow.AddSeconds(15)
	do {
		Start-Sleep -Milliseconds 200
		$remaining = Get-AlphaProcesses
	} while ($remaining.Count -and [DateTime]::UtcNow -lt $deadline)
	if ($remaining.Count) { throw 'Alpha did not stop; install was not attempted.' }
}

$installer = Join-Path $sourceRoot 'scripts\install-Agent_b.ps1'
$installerArguments = @(
	'-NoLogo', '-NoProfile', '-File', $installer,
	'-SourceDirectory', $sourceRoot,
	'-ApplicationDirectory', $applicationRoot,
	'-DataDirectory', $dataRoot,
	'-WorkspaceDirectory', $workspaceRoot,
	'-StartMenuDirectory', $startMenuRoot,
	'-UninstallRegistryPath', $uninstallKey,
	'-OperatorSid', [Security.Principal.WindowsIdentity]::GetCurrent().User.Value,
	'-OperatorLocalAppData', [Environment]::GetFolderPath('LocalApplicationData'),
	'-Alpha', '-SkipBuild'
)
$powershell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
$installLog = Join-Path ([IO.Path]::GetTempPath()) ("Agent_b-alpha-install-{0}.log" -f [Guid]::NewGuid().ToString('N'))
$invokeInstaller = '& { Import-Module Microsoft.PowerShell.Security -ErrorAction Stop; & ' +
	(Quote-ProcessArgument $installer) + ' ' +
	(($installerArguments | Select-Object -Skip 4 | ForEach-Object { Quote-ProcessArgument $_ }) -join ' ') +
	'} *> ' + (Quote-ProcessArgument $installLog) + '; if ($?) { exit 0 } else { exit 1 }'
$encodedInstaller = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($invokeInstaller))
$install = Start-Process -FilePath $powershell -ArgumentList "-NoLogo -NoProfile -EncodedCommand $encodedInstaller" -Verb RunAs -Wait -PassThru
if ($install.ExitCode -ne 0) {
	$detail = if (Test-Path -LiteralPath $installLog -PathType Leaf) { Get-Content -Raw -LiteralPath $installLog } else { 'no installer log was produced' }
	throw "Alpha install failed with exit code $($install.ExitCode). Log: $installLog`n$detail"
}
if (Test-Path -LiteralPath $installLog -PathType Leaf) { Remove-Item -LiteralPath $installLog -Force }

$launcher = Join-Path $applicationRoot 'scripts\launch-Agent_b.ps1'
if (Test-IsAdministrator) {
	# Agent_b correctly refuses an elevated token. A one-shot task obtains the
	# logged-on operator's limited token and is removed immediately after launch.
	$launchArguments = '-NoLogo -NoProfile -File ' + (Quote-ProcessArgument $launcher) +
		' -ApplicationDirectory ' + (Quote-ProcessArgument $applicationRoot) +
		' -DataDirectory ' + (Quote-ProcessArgument $dataRoot) + ' -Detached -NoBrowser -NoPause'
	$taskName = 'Agent_b Alpha Launch ' + [Guid]::NewGuid().ToString('N')
	$action = New-ScheduledTaskAction -Execute $powershell -Argument $launchArguments -WorkingDirectory $dataRoot
	$principal = New-ScheduledTaskPrincipal -UserId ([Security.Principal.WindowsIdentity]::GetCurrent().Name) -LogonType Interactive -RunLevel Limited
	$task = New-ScheduledTask -Action $action -Principal $principal
	try {
		Register-ScheduledTask -TaskName $taskName -InputObject $task -Force | Out-Null
		Start-ScheduledTask -TaskName $taskName
		Start-Sleep -Milliseconds 750
	} finally {
		Unregister-ScheduledTask -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
	}
} else {
	& $launcher -ApplicationDirectory $applicationRoot -DataDirectory $dataRoot -Detached -NoBrowser -NoPause
	if ($LASTEXITCODE -ne 0) { throw "Alpha launch failed with exit code $LASTEXITCODE." }
}

$deadline = [DateTime]::UtcNow.AddSeconds(30)
$state = $null
do {
	try { $state = Invoke-RestMethod -Uri ([Uri]::new([Uri]$alphaURL, 'api/state')) -TimeoutSec 2 } catch { $state = $null }
	if (-not $state) { Start-Sleep -Milliseconds 250 }
} while (-not $state -and [DateTime]::UtcNow -lt $deadline)
if (-not $state) { throw "Alpha did not answer $alphaURL within 30 seconds." }
if (-not $state.build.known -or $state.build.dirty -or $state.build.commit -ne $resolved) {
	throw "Alpha build assertion failed: deployed $resolved, running $($state.build.commit), dirty=$($state.build.dirty)."
}

Write-Host "ALPHA DEPLOYED: $resolved"
Write-Host "ASSERTED: $alphaURL reports build $($state.build.display)"
Write-Host "ROOTS: $applicationRoot | $dataRoot | $workspaceRoot"
