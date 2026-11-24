param(
  [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\Yanhekt Player')
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$srcApp = Join-Path $root 'dist\yanhekt.exe'
$srcBrowser = Join-Path $root 'dist\yanhekt-browser.exe'

if (-not (Test-Path $srcApp)) {
  throw "Missing $srcApp; run go\build.ps1 first."
}

Get-Process yanhekt* -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 200

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

foreach ($src in @($srcApp, $srcBrowser)) {
  if (-not (Test-Path $src)) { continue }
  $dst = Join-Path $InstallDir (Split-Path -Leaf $src)
  # Copy raw bytes to create a fresh local file without inheriting the workspace Low Mandatory Level SACL.
  $bytes = [System.IO.File]::ReadAllBytes($src)
  [System.IO.File]::WriteAllBytes($dst, $bytes)
  Unblock-File -Path $dst -ErrorAction SilentlyContinue
  & icacls $dst /setintegritylevel Medium *> $null
}

$desktop = [Environment]::GetFolderPath('Desktop')
$startMenu = Join-Path ([Environment]::GetFolderPath('Programs')) 'Yanhekt Player'
New-Item -ItemType Directory -Force -Path $startMenu | Out-Null

$wsh = New-Object -ComObject WScript.Shell
$shortcuts = @(
  @{ Link = (Join-Path $desktop 'Yanhekt Player.lnk'); Target = (Join-Path $InstallDir 'yanhekt.exe') },
  @{ Link = (Join-Path $startMenu 'Yanhekt Player.lnk'); Target = (Join-Path $InstallDir 'yanhekt.exe') }
)
if (Test-Path (Join-Path $InstallDir 'yanhekt-browser.exe')) {
  $shortcuts += @{ Link = (Join-Path $startMenu 'Yanhekt Player (Browser).lnk'); Target = (Join-Path $InstallDir 'yanhekt-browser.exe') }
}

foreach ($item in $shortcuts) {
  $sc = $wsh.CreateShortcut($item.Link)
  $sc.TargetPath = $item.Target
  $sc.WorkingDirectory = $InstallDir
  $sc.Save()
}

Write-Host "Installed to: $InstallDir"
Write-Host "Desktop shortcut created: $(Join-Path $desktop 'Yanhekt Player.lnk')"
