param(
    [Parameter(Mandatory=$true)][string]$SourceRel,
    [Parameter(Mandatory=$true)][string]$OutputRel,
    [string]$Package = '.',
    [string]$GoOs = 'linux',
    [string]$GoArch = 'arm64',
    [string]$GoExePath = ''
)

$ErrorActionPreference = 'Stop'
$RepoRoot = Split-Path -Parent $PSScriptRoot
$SourceDir = Join-Path $RepoRoot $SourceRel
$Output = Join-Path $RepoRoot $OutputRel
$GoExe = if ($GoExePath) { $GoExePath } else { (Get-Command go.exe -ErrorAction Stop).Source }
if (-not (Test-Path $GoExe)) { throw "Go executable not found: $GoExe" }

if (-not (Test-Path $SourceDir)) { throw "Source directory not found: $SourceDir" }
$OutputParent = Split-Path -Parent $Output
if ($OutputParent -and -not (Test-Path $OutputParent)) { New-Item -ItemType Directory -Force -Path $OutputParent | Out-Null }

$env:GOOS = $GoOs
$env:GOARCH = $GoArch
$env:CGO_ENABLED = '0'
$env:GOTOOLCHAIN = 'local'

Remove-Item -Force -ErrorAction SilentlyContinue $Output,($Output + '.exe')
$proc = Start-Process -FilePath $GoExe -ArgumentList @('build','-trimpath','-o',$Output,$Package) -WorkingDirectory $SourceDir -NoNewWindow -Wait -PassThru
if ($proc.ExitCode -ne 0) { throw "go build failed with exit code $($proc.ExitCode)" }
if (-not (Test-Path $Output)) { throw "Build succeeded but output is missing: $Output" }
Get-Item $Output | Select-Object FullName,Length,LastWriteTime
