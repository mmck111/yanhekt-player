param(
  [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\Yanhekt Player')
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$srcApp = Join-Path $root 'dist\yanhekt.exe'

if (-not (Test-Path $srcApp)) {
  throw "Missing $srcApp; run go\build.ps1 first."
}

Get-Process yanhekt* -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 200

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

$dst = Join-Path $InstallDir 'yanhekt.exe'
# Copy raw bytes to create a fresh local file without inheriting the workspace Low Mandatory Level SACL.
$bytes = [System.IO.File]::ReadAllBytes($srcApp)
[System.IO.File]::WriteAllBytes($dst, $bytes)
Unblock-File -Path $dst -ErrorAction SilentlyContinue
& icacls $dst /setintegritylevel Medium *> $null

$desktop = [Environment]::GetFolderPath('Desktop')
$startMenu = Join-Path ([Environment]::GetFolderPath('Programs')) 'Yanhekt Player'
New-Item -ItemType Directory -Force -Path $startMenu | Out-Null

$wsh = New-Object -ComObject WScript.Shell
$shortcuts = @(
  @{ Link = (Join-Path $desktop 'Yanhekt Player.lnk'); Target = $dst },
  @{ Link = (Join-Path $startMenu 'Yanhekt Player.lnk'); Target = $dst }
)

foreach ($item in $shortcuts) {
  $sc = $wsh.CreateShortcut($item.Link)
  $sc.TargetPath = $item.Target
  $sc.WorkingDirectory = $InstallDir
  $sc.Save()
}

Write-Host "Installed to: $InstallDir"
Write-Host "Desktop shortcut created: $(Join-Path $desktop 'Yanhekt Player.lnk')"
