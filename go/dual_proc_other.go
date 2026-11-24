//go:build !windows

package main

import "os/exec"

func hideCmdWindow(cmd *exec.Cmd) {}
