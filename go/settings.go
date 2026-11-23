package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// appSettings 用户可配置的播放器与下载偏好（跨前后端持久化）
type appSettings struct {
	DownloadDir string  `json:"downloadDir"`
	AskSavePath bool    `json:"askSavePath"`
	DefaultView string  `json:"defaultView"` // "camera" | "dual" | "screen" | "room"
	DualPip     bool    `json:"dualPip"`
	DefaultRate float64 `json:"defaultRate"`
	AutoNext    bool    `json:"autoNext"`
	RememberPos bool    `json:"rememberPos"`
}

var (
	settingsMu sync.Mutex

	// 由桌面宿主在启动时注入（无头/Android 保持 nil）
	desktopCheckWritableDir   func(string) bool
	desktopDefaultDownloadDir func() string
	desktopLowIntegrityFn     func() bool
	settingsPathOverride      string
)

func defaultAppSettings() appSettings {
	s := appSettings{
		DefaultView: "camera",
		DefaultRate: 1,
		RememberPos: true,
	}
	if def := loadInstallDefaults(); def != "" {
		s.DownloadDir = def
	}
	return s
}

func installDefaultsPath() string {
	if settingsPathOverride != "" {
		return ""
	}
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), "defaults.json")
}

func loadInstallDefaults() string {
	p := installDefaultsPath()
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil || len(b) == 0 {
		return ""
	}
	var d struct {
		DownloadDir string `json:"downloadDir"`
	}
	if json.Unmarshal(b, &d) != nil {
		return ""
	}
	return strings.TrimSpace(d.DownloadDir)
}

func sanitizeAppSettings(in appSettings) appSettings {
	in.DownloadDir = strings.TrimSpace(in.DownloadDir)
	switch in.DefaultView {
	case "camera", "dual", "screen", "room":
	default:
		in.DefaultView = "camera"
	}
	validRates := []float64{0.75, 1, 1.25, 1.5, 1.75, 2}
	matched := false
	for _, r := range validRates {
		if in.DefaultRate == r {
			matched = true
			break
		}
	}
	if !matched {
		in.DefaultRate = 1
	}
	return in
}

func settingsFilePaths() []string {
	if settingsPathOverride != "" {
		return []string{settingsPathOverride}
	}
	var raw []string
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		raw = append(raw, filepath.Join(dir, "yanhekt", "settings.json"))
	}
	if t := os.TempDir(); t != "" {
		raw = append(raw, filepath.Join(t, "yanhekt", "settings.json"))
	}
	if exe, err := os.Executable(); err == nil && exe != "" {
		raw = append(raw, filepath.Join(filepath.Dir(exe), ".yanhekt-cache", "settings.json"))
	}
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, p := range raw {
		c := filepath.Clean(p)
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

func loadAppSettings() appSettings {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	for _, p := range settingsFilePaths() {
		b, err := os.ReadFile(p)
		if err != nil || len(b) == 0 {
			continue
		}
		var s appSettings
		// 先填默认值，兼容早期缺少新增字段（如 rememberPos）的 JSON
		s = defaultAppSettings()
		if json.Unmarshal(b, &s) == nil {
			return sanitizeAppSettings(s)
		}
	}
	return defaultAppSettings()
}

func saveAppSettings(s appSettings) error {
	s = sanitizeAppSettings(s)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	settingsMu.Lock()
	defer settingsMu.Unlock()
	var lastErr error
	for _, p := range settingsFilePaths() {
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			lastErr = err
			continue
		}
		if err := os.WriteFile(p, data, 0644); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}

func effectiveDownloadDirOf(s appSettings) string {
	if s.DownloadDir != "" {
		if desktopCheckWritableDir == nil || desktopCheckWritableDir(s.DownloadDir) {
			return s.DownloadDir
		}
	}
	if desktopDefaultDownloadDir != nil {
		return desktopDefaultDownloadDir()
	}
	return ""
}

func settingsPayload(s appSettings) map[string]any {
	lowIL := false
	if desktopLowIntegrityFn != nil {
		lowIL = desktopLowIntegrityFn()
	}
	return map[string]any{
		"ok":                   true,
		"settings":             s,
		"effectiveDownloadDir": effectiveDownloadDirOf(s),
		"desktop":              desktopSupported(),
		"lowIntegrity":         lowIL,
	}
}

func handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, 200, settingsPayload(loadAppSettings()))
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "读取设置失败"})
		return
	}
	cur := loadAppSettings()
	next := cur
	if err := json.Unmarshal(body, &next); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "设置格式错误：" + err.Error()})
		return
	}
	next = sanitizeAppSettings(next)
	if next.DownloadDir != "" && desktopCheckWritableDir != nil && !desktopCheckWritableDir(next.DownloadDir) {
		writeJSON(w, 400, map[string]any{
			"ok":    false,
			"error": "该目录没有写入权限（可能是系统保护目录如 D:\\Downloads），请改选其他可写目录",
		})
		return
	}
	if err := saveAppSettings(next); err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": "保存设置失败：" + err.Error()})
		return
	}
	writeJSON(w, 200, settingsPayload(next))
}
