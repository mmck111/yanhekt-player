//go:build windows

// Windows 专属的小动作：控制台切 UTF-8、把地址塞进剪贴板
package main

import (
	"os/exec"
	"strings"
	"syscall"
)

// setUtf8Console Windows 控制台默认 GBK，先把代码页切成 UTF-8，中文才不会变乱码
func setUtf8Console() {
	k32 := syscall.NewLazyDLL("kernel32.dll")
	_, _, _ = k32.NewProc("SetConsoleOutputCP").Call(65001)
	_, _, _ = k32.NewProc("SetConsoleCP").Call(65001)
}

// copyToClipboard 把地址塞进剪贴板：省得用户手抄（不开浏览器，避免和已开的浏览器打架）
func copyToClipboard(text string) bool {
	cmd := exec.Command("clip")
	cmd.Stdin = strings.NewReader(text + "\r\n")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return false
	}
	go func() { _ = cmd.Wait() }()
	return true
}
