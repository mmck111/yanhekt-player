@echo off
chcp 65001 >nul
cd /d "%~dp0"
echo.
echo   正在检查 Windows 对「延河课堂播放器.exe」的阻止记录……
echo.

powershell -NoProfile -ExecutionPolicy Bypass -Command ^
  "$f = Join-Path '%~dp0' '延河课堂播放器.exe';" ^
  "if (-not (Test-Path -LiteralPath $f)) { Write-Host '  没找到 延河课堂播放器.exe —— 请把这个 bat 和 exe 放在同一个文件夹里。' -ForegroundColor Yellow; exit }" ^
  "$before = $null;" ^
  "try { $before = Get-Content -LiteralPath $f -Stream Zone.Identifier -ErrorAction Stop } catch {}" ^
  "Unblock-File -LiteralPath $f -ErrorAction SilentlyContinue;" ^
  "try { Remove-Item -LiteralPath $f -Stream Zone.Identifier -Force -ErrorAction Stop } catch {}" ^
  "$after = $null; try { $after = Get-Content -LiteralPath $f -Stream Zone.Identifier -ErrorAction Stop } catch {}" ^
  "if ($before -and -not $after) {" ^
  "  Write-Host '  ✅ 已清除「来自 Internet」的标记。再双击 exe 就不会弹了。' -ForegroundColor Green" ^
  "} elseif (-not $before) {" ^
  "  Write-Host '  这个 exe 上没有「来自 Internet」的标记 —— 说明弹窗不是它引起的。' -ForegroundColor Yellow" ^
  "  Write-Host '' ;" ^
  "  Write-Host '  那你看到的多半是 Windows 的「未知发布者」提示（因为这个 exe 没买代码签名证书）。'" ^
  "  Write-Host '  这种情况 Windows 每次都会问一次，两种处理：'" ^
  "  Write-Host '    · 每次点「更多信息 → 仍要运行」；'" ^
  "  Write-Host '    · 右键 exe → 属性 → 底部勾选「解除锁定」→ 确定（只在文件是从网上下的才有这一项）。'" ^
  "} else {" ^
  "  Write-Host '  清除失败，可能 exe 还在运行。先关掉播放器的黑窗口，再运行一次本文件。' -ForegroundColor Yellow" ^
  "}" ^
  "Write-Host '';" ^
  "Write-Host ('  文件：' + $f);" ^
  "Write-Host ('  大小：' + [math]::Round((Get-Item -LiteralPath $f).Length / 1MB, 1) + ' MB');" ^
  "Write-Host ('  修改时间：' + (Get-Item -LiteralPath $f).LastWriteTime)"

echo.
pause
