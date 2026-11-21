//go:build windows && !nogui

package main

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"
)

func TestProbeWritableDirAndFallback(t *testing.T) {
	tmp := t.TempDir()
	okDir := filepath.Join(tmp, "ok", "webview2")
	if !probeWritableDir(okDir) {
		t.Fatalf("expected writable temp dir %q", okDir)
	}
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

func TestOpenFileNameStructSizeAndFilenameParse(t *testing.T) {
	if got := unsafe.Sizeof(winOpenFileNameW{}); got != 152 {
		t.Fatalf("winOpenFileNameW size=%d, want 152 on amd64", got)
	}
	cd := `attachment; filename="lecture.ts"; filename*=UTF-8''%E8%AF%BE%E7%A8%8B+102502_%E6%95%99%E5%AE%A4.ts`
	if got := parseDownloadFilename(cd, "/y2dl?c=1&title=x"); got != "课程 102502_教室.ts" {
		t.Fatalf("parseDownloadFilename=%q", got)
	}
	if got := parseDownloadFilename("", "/y2dl?c=1&title=A%3AB/C"); got != "A_B_C.mp4" {
		t.Fatalf("fallback title=%q", got)
	}
}
