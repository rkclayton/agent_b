[CmdletBinding()]
param(
    [string]$GoRoot = '',
    [string]$Repository = ''
)

# Rebuilds the two ignored tool folders a checkout needs for the suites:
# node_modules (Playwright, from package-lock.json via `npm ci`) and .tools\go
# (a copy of an installed Go toolchain at or above go.mod's version). It never
# removes anything: a non-empty .tools\go that does not run is reported, not
# replaced, and a junction in either place is refused. v0.64.0, item 2en.

$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($Repository)) { $Repository = Split-Path -Parent $PSScriptRoot }

function Assert-NotReparsePoint([string]$Path) {
    if ((Test-Path -LiteralPath $Path) -and ((Get-Item -LiteralPath $Path -Force).Attributes -band [IO.FileAttributes]::ReparsePoint)) {
        throw "Refusing to rebuild through a junction or link: $Path"
    }
}

$repositoryRoot = [IO.Path]::GetFullPath($Repository).TrimEnd('\')
if (-not (Test-Path -LiteralPath (Join-Path $repositoryRoot 'go.mod') -PathType Leaf)) { throw "Not the Agent_b repository: $repositoryRoot" }

# --- node_modules
$nodeModules = Join-Path $repositoryRoot 'node_modules'
Assert-NotReparsePoint $nodeModules
$npm = (Get-Command npm.cmd -ErrorAction Stop).Source
Push-Location $repositoryRoot
try { & $npm ci --no-audit --no-fund } finally { Pop-Location }
if ($LASTEXITCODE -ne 0) { throw "npm ci exited $LASTEXITCODE" }
& (Get-Command node.exe -ErrorAction Stop).Source -e "import('@playwright/test').then(() => console.log('node_modules: @playwright/test resolves'))"
if ($LASTEXITCODE -ne 0) { throw '@playwright/test does not resolve after npm ci' }

# --- .tools\go
$required = [regex]::Match((Get-Content -Raw -LiteralPath (Join-Path $repositoryRoot 'go.mod')), '(?m)^go\s+(\d+\.\d+)').Groups[1].Value
$toolsGo = Join-Path $repositoryRoot '.tools\go'
Assert-NotReparsePoint (Join-Path $repositoryRoot '.tools')
Assert-NotReparsePoint $toolsGo
$localGo = Join-Path $toolsGo 'bin\go.exe'
if (Test-Path -LiteralPath $localGo -PathType Leaf) {
    $version = & $localGo version
    if ($LASTEXITCODE -eq 0) { Write-Host ".tools\go: already present ($version)"; return }
}
if ((Test-Path -LiteralPath $toolsGo) -and (Get-ChildItem -LiteralPath $toolsGo -Force | Select-Object -First 1)) {
    throw ".tools\go is not empty but its go.exe does not run; inspect it by hand, this script removes nothing: $toolsGo"
}
if ([string]::IsNullOrWhiteSpace($GoRoot)) {
    $onPath = Get-Command go.exe -ErrorAction SilentlyContinue
    $GoRoot = if ($onPath) { (& $onPath.Source env GOROOT).Trim() } elseif (Test-Path -LiteralPath 'C:\Go\bin\go.exe') { 'C:\Go' } else { '' }
}
if ([string]::IsNullOrWhiteSpace($GoRoot) -or -not (Test-Path -LiteralPath (Join-Path $GoRoot 'bin\go.exe') -PathType Leaf)) {
    throw "No Go toolchain found; install Go $required or later, or pass -GoRoot."
}
$sourceGo = Join-Path $GoRoot 'bin\go.exe'
$sourceVersion = [regex]::Match((& $sourceGo version), 'go(\d+)\.(\d+)')
$need = $required.Split('.')
if (-not $sourceVersion.Success -or [int]$sourceVersion.Groups[1].Value -lt [int]$need[0] -or ([int]$sourceVersion.Groups[1].Value -eq [int]$need[0] -and [int]$sourceVersion.Groups[2].Value -lt [int]$need[1])) {
    throw "Go at $GoRoot is older than go.mod's go $required"
}
$null = New-Item -ItemType Directory -Path $toolsGo -Force
# /E copies the tree; no /MIR and no /PURGE, so nothing at the destination is deleted.
& (Join-Path $env:SystemRoot 'System32\robocopy.exe') $GoRoot $toolsGo /E /NFL /NDL /NJH /NJS /NP /R:1 /W:1 | Out-Null
if ($LASTEXITCODE -ge 8) { throw "robocopy exited $LASTEXITCODE copying $GoRoot" }
$version = & $localGo version
if ($LASTEXITCODE -ne 0) { throw ".tools\go\bin\go.exe does not run after the copy" }
Write-Host ".tools\go: rebuilt from $GoRoot ($version)"
