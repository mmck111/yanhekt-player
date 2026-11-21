//go:build !windows || nogui

package main

func desktopSupported() bool { return false }

func focusRunningDesktopApp(p int) bool { return false }

func runDesktopApp(p int) bool { return false }

func attachParentConsoleIfNeeded() {}

func showDesktopError(msg string) {}

func resetDesktopLog() {}

func desktopLog(format string, args ...any) {}

func desktopFailTrace() string { return "" }
