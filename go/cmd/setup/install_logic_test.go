package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractGzipPayloadAndApplySettings(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	want := []byte("MZ-dummy-exe-payload")
	if _, err := zw.Write(want); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	tmp := t.TempDir()
	installDir := filepath.Join(tmp, "app")
	dstExe := filepath.Join(installDir, "yanhekt.exe")
	if err := extractGzipPayload(gz.Bytes(), dstExe); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dstExe)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("extracted mismatch got=%q err=%v", got, err)
	}

	localApp := filepath.Join(tmp, "localapp")
	// Pre-populate user view preference to verify merge preserves non-download settings.
	setDir := filepath.Join(localApp, "yanhekt")
	if err := os.MkdirAll(setDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(setDir, "settings.json"), []byte(`{"defaultView":"dual","defaultRate":1.5}`), 0644); err != nil {
		t.Fatal(err)
	}

	dlDir := filepath.Join(tmp, "myvideos")
	if err := applyInstallSettings(installDir, dlDir, localApp); err != nil {
		t.Fatal(err)
	}

	var def map[string]any
	b, err := os.ReadFile(filepath.Join(installDir, "defaults.json"))
	if err != nil || json.Unmarshal(b, &def) != nil || def["downloadDir"] != dlDir {
		t.Fatalf("defaults.json invalid: %s err=%v", b, err)
	}

	var merged map[string]any
	b2, err := os.ReadFile(filepath.Join(setDir, "settings.json"))
	if err != nil || json.Unmarshal(b2, &merged) != nil {
		t.Fatalf("settings.json invalid: %s err=%v", b2, err)
	}
	if merged["downloadDir"] != dlDir || merged["defaultView"] != "dual" || merged["defaultRate"] != 1.5 {
		t.Fatalf("merged mismatch: %+v", merged)
	}
}
