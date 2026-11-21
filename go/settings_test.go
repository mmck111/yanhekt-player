package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestSettingsRoundTripAndValidation(t *testing.T) {
	tmp := t.TempDir()
	prevOverride := settingsPathOverride
	prevCheck := desktopCheckWritableDir
	prevDef := desktopDefaultDownloadDir
	settingsPathOverride = filepath.Join(tmp, "settings.json")
	desktopCheckWritableDir = func(dir string) bool { return dir != `D:\Blocked` }
	desktopDefaultDownloadDir = func() string { return filepath.Join(tmp, "downloads") }
	defer func() {
		settingsPathOverride = prevOverride
		desktopCheckWritableDir = prevCheck
		desktopDefaultDownloadDir = prevDef
	}()

	req := httptest.NewRequest("GET", "/api/settings", nil)
	rec := httptest.NewRecorder()
	handleSettings(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET status=%d", rec.Code)
	}
	var got struct {
		Ok                   bool        `json:"ok"`
		Settings             appSettings `json:"settings"`
		EffectiveDownloadDir string      `json:"effectiveDownloadDir"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || !got.Ok {
		t.Fatalf("unexpected GET body: %s err=%v", rec.Body.String(), err)
	}
	if got.Settings.DefaultView != "camera" || !got.Settings.RememberPos {
		t.Fatalf("unexpected defaults: %+v", got.Settings)
	}

	// Unwritable directory must be rejected cleanly with HTTP 400.
	badBody, _ := json.Marshal(appSettings{DownloadDir: `D:\Blocked`, DefaultView: "dual", DefaultRate: 1.5})
	breq := httptest.NewRequest("POST", "/api/settings", bytes.NewReader(badBody))
	brec := httptest.NewRecorder()
	handleSettings(brec, breq)
	if brec.Code != 400 {
		t.Fatalf("expected 400 for unwritable dir, got %d (%s)", brec.Code, brec.Body.String())
	}

	// Valid settings persist and normalize cleanly.
	goodDir := filepath.Join(tmp, "myvideos")
	goodBody, _ := json.Marshal(appSettings{
		DownloadDir: goodDir,
		AskSavePath: true,
		DefaultView: "dual",
		DualPip:     true,
		DefaultRate: 1.5,
		AutoNext:    true,
		RememberPos: false,
	})
	preq := httptest.NewRequest("POST", "/api/settings", bytes.NewReader(goodBody))
	prec := httptest.NewRecorder()
	handleSettings(prec, preq)
	if prec.Code != 200 {
		t.Fatalf("POST status=%d body=%s", prec.Code, prec.Body.String())
	}
	loaded := loadAppSettings()
	if loaded.DownloadDir != goodDir || !loaded.AskSavePath || loaded.DefaultView != "dual" ||
		!loaded.DualPip || loaded.DefaultRate != 1.5 || !loaded.AutoNext || loaded.RememberPos {
		t.Fatalf("persisted mismatch: %+v", loaded)
	}
}
