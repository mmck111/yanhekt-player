//go:build !windows

// 非 Windows 平台：控制台本来就是 UTF-8，剪贴板没有统一实现，直接跳过
package main

func setUtf8Console() {}

func copyToClipboard(text string) bool { return false }
