# 构建 Android APK（不用 Gradle：aapt2 + javac + d8 + zipalign + apksigner）
#
#   powershell -ExecutionPolicy Bypass -File android\build.ps1
#
# 依赖：JDK 17、Android SDK（build-tools 34 + platforms/android-34）、Go 1.21+
# 可用环境变量覆盖：JAVA_HOME / ANDROID_SDK_ROOT / YHKT_ANDROID_OUT
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot          # 仓库根
$apkDir = $PSScriptRoot
$out = if ($env:YHKT_ANDROID_OUT) { $env:YHKT_ANDROID_OUT } else { Join-Path $apkDir 'build' }
New-Item -ItemType Directory -Force -Path $out | Out-Null

# ---- 工具路径 ----
$ws = Split-Path -Parent $root                    # 工作区
$jdk = if ($env:JAVA_HOME) { $env:JAVA_HOME } else { Join-Path $ws '_android\jdk' }
$sdk = if ($env:ANDROID_SDK_ROOT) { $env:ANDROID_SDK_ROOT } else { Join-Path $ws '_android\sdk' }
$androidJar = Join-Path $sdk 'platforms\android-34\android.jar'
$bt = Join-Path $sdk 'build-tools\34.0.0'
foreach ($p in @((Join-Path $jdk 'bin\javac.exe'), $androidJar, (Join-Path $bt 'aapt2.exe'), (Join-Path $bt 'zipalign.exe'))) {
  if (-not (Test-Path $p)) { throw "缺少 $p —— 先跑 _android_setup.ps1 装工具链" }
}
$env:JAVA_HOME = $jdk
$env:PATH = (Join-Path $jdk 'bin') + ';' + $bt + ';' + $env:PATH

# ---- Go 引擎（android/arm64）----
$go = (Get-Command go -ErrorAction SilentlyContinue).Source
if (-not $go) { $go = Join-Path $ws 'tools\go\bin\go.exe' }
if (-not (Test-Path $go)) { throw '找不到 go.exe' }
$web = Join-Path $root 'go\web'
# 构建缓存放到工作区（有些机器默认 TEMP 不可写）
$buildDir = $env:YHKT_BUILD_DIR
if (-not $buildDir) { $buildDir = Join-Path $ws '_gobuild2' }
New-Item -ItemType Directory -Force -Path "$buildDir\tmp", "$buildDir\gocache", "$buildDir\gopath" | Out-Null
$env:TMP = "$buildDir\tmp"; $env:TEMP = "$buildDir\tmp"
$env:GOCACHE = "$buildDir\gocache"; $env:GOPATH = "$buildDir\gopath"
if (Test-Path $web) { Remove-Item $web -Recurse -Force }
New-Item -ItemType Directory -Force -Path $web | Out-Null
foreach ($f in 'index.html', 'y2.html', 'icon.png', 'hls.min.js', 'catalog.json.gz', 'y2-catalog.json.gz') {
  $src = Join-Path $root "public\$f"
  if (Test-Path $src) { Copy-Item $src (Join-Path $web $f) }
}
$libDir = Join-Path $out 'lib\arm64-v8a'
New-Item -ItemType Directory -Force -Path $libDir | Out-Null
$so = Join-Path $libDir 'libyhkt.so'
Push-Location (Join-Path $root 'go')
try {
  $env:CGO_ENABLED = '0'; $env:GOOS = 'android'; $env:GOARCH = 'arm64'
  & $go build -trimpath -tags nethttpomithttp2 -ldflags '-s -w' -o $so .
  if ($LASTEXITCODE -ne 0) { throw 'Go 引擎编译失败' }
} finally { Pop-Location; Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue }
Write-Host ('引擎 ' + [math]::Round((Get-Item $so).Length / 1MB, 2) + ' MB (android/arm64)')

# ---- 资源（图标）----
# aapt2 要先把 res/ 编译成 flat 文件，再跟清单一起 link
$resDir = Join-Path $apkDir 'res'
$resZip = Join-Path $out 'res.zip'
$linkArgs = @()
if (Test-Path $resDir) {
  if (Test-Path $resZip) { Remove-Item $resZip -Force }
  & (Join-Path $bt 'aapt2.exe') compile --dir $resDir -o $resZip
  if ($LASTEXITCODE -ne 0) { throw 'aapt2 compile 失败' }
  $linkArgs = @($resZip)
}

# ---- 资源 / 清单 ----
$baseApk = Join-Path $out 'base.apk'
if (Test-Path $baseApk) { Remove-Item $baseApk -Force }
& (Join-Path $bt 'aapt2.exe') link -o $baseApk -I $androidJar `
  --manifest (Join-Path $apkDir 'AndroidManifest.xml') `
  --min-sdk-version 23 --target-sdk-version 34 --version-code 2 --version-name 2.0.0 @linkArgs
if ($LASTEXITCODE -ne 0) { throw 'aapt2 link 失败' }

# ---- Java ----
$classes = Join-Path $out 'classes'
if (Test-Path $classes) { Remove-Item $classes -Recurse -Force }
New-Item -ItemType Directory -Force -Path $classes | Out-Null
$srcs = @(Get-ChildItem (Join-Path $apkDir 'java') -Recurse -Filter *.java | ForEach-Object { $_.FullName })
$jArgs = @('-encoding', 'UTF-8', '-source', '8', '-target', '8', '-nowarn',
           '-bootclasspath', $androidJar, '-classpath', $androidJar, '-d', $classes) + $srcs
& (Join-Path $jdk 'bin\javac.exe') @jArgs
if ($LASTEXITCODE -ne 0) { throw 'javac 失败' }

# ---- dex ----
$dexDir = Join-Path $out 'dex'
if (Test-Path $dexDir) { Remove-Item $dexDir -Recurse -Force }
New-Item -ItemType Directory -Force -Path $dexDir | Out-Null
$classFiles = @(Get-ChildItem $classes -Recurse -Filter *.class | ForEach-Object { $_.FullName })
$d8Args = @('--release', '--min-api', '23', '--lib', $androidJar, '--output', $dexDir) + $classFiles
& (Join-Path $bt 'd8.bat') @d8Args
if ($LASTEXITCODE -ne 0) { throw 'd8 失败' }

# ---- 打包（classes.dex + 引擎 so 塞进 apk）----
Add-Type -AssemblyName System.IO.Compression.FileSystem
$tmpApk = Join-Path $out 'unsigned.apk'
Copy-Item $baseApk $tmpApk -Force
$zip = [System.IO.Compression.ZipFile]::Open($tmpApk, 'Update')
try {
  $dex = Join-Path $dexDir 'classes.dex'
  [System.IO.Compression.ZipFileExtensions]::CreateEntryFromFile($zip, $dex, 'classes.dex', [System.IO.Compression.CompressionLevel]::Optimal) | Out-Null
  # native lib 必须叫 lib*.so，且走 lib/<abi>/ 目录，Android 才会解到 nativeLibraryDir
  [System.IO.Compression.ZipFileExtensions]::CreateEntryFromFile($zip, $so, 'lib/arm64-v8a/libyhkt.so', [System.IO.Compression.CompressionLevel]::Optimal) | Out-Null
} finally { $zip.Dispose() }

# ---- 对齐 + 签名 ----
$aligned = Join-Path $out 'aligned.apk'
if (Test-Path $aligned) { Remove-Item $aligned -Force }
& (Join-Path $bt 'zipalign.exe') -f -p 4 $tmpApk $aligned
if ($LASTEXITCODE -ne 0) { throw 'zipalign 失败' }

$ks = Join-Path $out 'debug.keystore'
if (-not (Test-Path $ks)) {
  & (Join-Path $jdk 'bin\keytool.exe') -genkeypair -v -keystore $ks -storepass android -keypass android `
    -alias androiddebugkey -keyalg RSA -keysize 2048 -validity 10000 `
    -dname 'CN=Android Debug,O=Android,C=US' | Out-Null
}
$signed = Join-Path $out 'yanhekt.apk'
if (Test-Path $signed) { Remove-Item $signed -Force }
& (Join-Path $bt 'apksigner.bat') sign --ks $ks --ks-pass pass:android --key-pass pass:android `
  --min-sdk-version 23 --out $signed $aligned
if ($LASTEXITCODE -ne 0) { throw 'apksigner 失败' }
& (Join-Path $bt 'apksigner.bat') verify $signed | Out-Null
if ($LASTEXITCODE -ne 0) { throw '签名校验失败' }

Get-Item $signed | ForEach-Object { '{0}  {1:N2} MB' -f $_.FullName, ($_.Length / 1MB) }
