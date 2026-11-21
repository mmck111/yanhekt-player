//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProbeWritableDirAndFallback(t *testing.T) {
	tmp := t.TempDir()
	okDir := filepath.Join(tmp, "ok", "webview2")
	if !probeWritableDir(okDir) {
		t.Fatalf("expected writable temp dir %q", okDir)
	}
	// When target points at an existing regular file, MkdirAll/CreateTemp must fail cleanly.
	fileAsDir := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(fileAsDir, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if probeWritableDir(fileAsDir) {
		t.Fatalf("expected non-dir path %q not to pass probeWritableDir", fileAsDir)
	}

	cands := desktopBaseDirCandidates()
	if len(cands) == 0 {
		t.Fatal("expected at least one desktop base dir candidate")
	}
}
