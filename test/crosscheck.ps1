# Cross-check: run Node version and Go version side by side,
# compare API shape (tools/parity.js) and run the smoke suite against both.
#
#   powershell -ExecutionPolicy Bypass -File test\crosscheck.ps1
#
# Ports: Node 8841, Go 8842. Both stopped at the end.

$ErrorActionPreference = 'Continue'
$root = Split-Path -Parent $PSScriptRoot
# 用 PATH 里的 node；没有就在常见位置找
$node = (Get-Command node -ErrorAction SilentlyContinue).Source
if (-not $node) {
  foreach ($p in @("$env:ProgramFiles\nodejs\node.exe", "${env:ProgramFiles(x86)}\nodejs\node.exe")) {
    if (Test-Path $p) { $node = $p; break }
  }
}
if (-not $node) { Write-Host '[!] 没找到 node，请先装 Node 20+ 或把 node.exe 放进 PATH'; return }
$goExe = Join-Path $root 'dist\yanhekt.exe'
$tmp = Join-Path $root '.tmp-probe'
New-Item -ItemType Directory -Force -Path $tmp | Out-Null

function Wait-Up($base, $seconds = 20) {
  $end = (Get-Date).AddSeconds($seconds)
  while ((Get-Date) -lt $end) {
    try {
      $r = Invoke-WebRequest "$base/api/status" -UseBasicParsing -TimeoutSec 2
      if ($r.StatusCode -eq 200) { return $true }
    } catch { Start-Sleep -Milliseconds 300 }
  }
  return $false
}

$env:FORCE_NEW = '1'

$env:PORT = '8841'
$pNode = Start-Process -FilePath $node -ArgumentList 'server.js' -WorkingDirectory $root -WindowStyle Hidden -PassThru `
  -RedirectStandardOutput (Join-Path $tmp 'node.log') -RedirectStandardError (Join-Path $tmp 'node.err')

$env:PORT = '8842'
$pGo = $null
if (Test-Path $goExe) {
  $pGo = Start-Process -FilePath $goExe -WorkingDirectory $root -WindowStyle Hidden -PassThru `
    -RedirectStandardOutput (Join-Path $tmp 'go.log') -RedirectStandardError (Join-Path $tmp 'go.err')
} else {
  Write-Host "[!] dist\yanhekt.exe not found, build it first: powershell -File go\build.ps1"
}

try {
  $nodeUp = Wait-Up 'http://127.0.0.1:8841'
  $goUp = $pGo -and (Wait-Up 'http://127.0.0.1:8842')
  Write-Host "node up: $nodeUp   go up: $goUp`n"
  if (-not $goUp) { Write-Host 'Go server did not start; see .tmp-probe\go.err'; return }

  Write-Host '=== parity (API shape) ==='
  & $node (Join-Path $root 'tools\parity.js') http://127.0.0.1:8841 http://127.0.0.1:8842

  Write-Host "`n=== smoke against Node ==="
  $env:SMOKE_URL = 'http://127.0.0.1:8841'
  & $node (Join-Path $root 'test\smoke.js') | Select-Object -Last 3

  Write-Host "`n=== smoke against Go ==="
  $env:SMOKE_URL = 'http://127.0.0.1:8842'
  & $node (Join-Path $root 'test\smoke.js') | Select-Object -Last 3
} finally {
  Remove-Item Env:SMOKE_URL -ErrorAction SilentlyContinue
  foreach ($p in @($pNode, $pGo)) { if ($p) { Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue } }
}
