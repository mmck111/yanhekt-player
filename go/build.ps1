# 构建 Go 版：同步页面资源 → 编译 Windows 单文件
# 用法：powershell -ExecutionPolicy Bypass -File go\build.ps1
#   $env:YHKT_BUILD_DIR 可指定构建缓存目录（默认 %LOCALAPPDATA%\yanhekt-build）
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot

# Go 编译器：优先 PATH，其次常见安装位置
$go = (Get-Command go -ErrorAction SilentlyContinue).Source
if (-not $go) {
  foreach ($p in @("$env:ProgramFiles\Go\bin\go.exe", "$env:LOCALAPPDATA\Programs\Go\bin\go.exe", 'C:\Go\bin\go.exe')) {
    if (Test-Path $p) { $go = $p; break }
  }
}
if (-not $go) { throw '找不到 go.exe：请先安装 Go 1.21+，或把 go.exe 放进 PATH' }

# 有些机器的默认 TEMP 不可写，构建缓存放到用户目录
$tools = $env:YHKT_BUILD_DIR
if (-not $tools) { $tools = Join-Path $env:LOCALAPPDATA 'yanhekt-build' }
New-Item -ItemType Directory -Force -Path "$tools\tmp", "$tools\gocache", "$tools\gopath" | Out-Null
$env:TMP = "$tools\tmp"; $env:TEMP = "$tools\tmp"
$env:GOCACHE = "$tools\gocache"; $env:GOPATH = "$tools\gopath"

# 1. 页面资源同步进 go/web（go:embed 不能引用上级目录）
$web = Join-Path $PSScriptRoot 'web'
if (Test-Path $web) { Remove-Item $web -Recurse -Force }
New-Item -ItemType Directory -Force -Path $web | Out-Null
foreach ($f in 'index.html', 'y2.html', 'icon.png', 'hls.min.js', 'catalog.json.gz', 'y2-catalog.json.gz') {
  Copy-Item (Join-Path $root "public\$f") (Join-Path $web $f)
}

# 2. 编译
#   -tags nethttpomithttp2：上游全走 HTTP/1.1（ForceAttemptHTTP2=false），
#   去掉 Go 内置的 HTTP/2 客户端代码，省约 0.6 MB。
Push-Location $PSScriptRoot
try {
  $env:CGO_ENABLED = '0'
  $ldflags = '-s -w'
  $tags = 'nethttpomithttp2'

  $env:GOOS = 'windows'; $env:GOARCH = 'amd64'
  & $go build -trimpath -tags $tags -ldflags $ldflags -o (Join-Path $root 'dist\yanhekt.exe') .
  if ($LASTEXITCODE -ne 0) { throw 'windows build failed' }
} finally {
  Pop-Location
  Remove-Item Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
}

Get-ChildItem (Join-Path $root 'dist\yanhekt*') | Select-Object Name, Length |
  ForEach-Object { '{0,-28} {1,8:N2} MB' -f $_.Name, ($_.Length / 1MB) }
