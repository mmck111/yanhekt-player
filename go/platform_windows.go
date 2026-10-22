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
	cmd := exec.Command("cmd", "/c", "chcp", "65001")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Run()
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
