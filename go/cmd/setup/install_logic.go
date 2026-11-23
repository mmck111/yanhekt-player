package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const appVersion = "2.1.0"

func defaultInstallDir(localAppData, userHome string) string {
	if strings.TrimSpace(localAppData) != "" {
		return filepath.Join(localAppData, "Programs", "Yanhekt Player")
	}
	if strings.TrimSpace(userHome) != "" {
		return filepath.Join(userHome, "Yanhekt Player")
	}
	return `C:\Yanhekt Player`
}

func defaultDownloadDir(knownDownloads, knownVideos, userHome, installDir string) string {
	for _, d := range []string{knownDownloads, knownVideos} {
		if strings.TrimSpace(d) != "" {
			return strings.TrimSpace(d)
		}
	}
	if strings.TrimSpace(userHome) != "" {
		return filepath.Join(userHome, "Downloads")
	}
	return filepath.Join(installDir, "downloads")
}

func extractGzipPayload(gz []byte, dstPath string) error {
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return err
	}
	r, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return err
	}
	defer r.Close()

	tmp := dstPath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Remove(dstPath)
	return os.Rename(tmp, dstPath)
}

func applyInstallSettings(installDir, downloadDir, localAppData string) error {
	downloadDir = strings.TrimSpace(downloadDir)
	if downloadDir != "" {
		_ = os.MkdirAll(downloadDir, 0755)
	}

	defObj := map[string]any{
		"downloadDir": downloadDir,
		"askSavePath": false,
	}
	defBytes, err := json.MarshalIndent(defObj, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(installDir, "defaults.json"), defBytes, 0644); err != nil {
		return err
	}

	if strings.TrimSpace(localAppData) == "" {
		return nil
	}
	setPath := filepath.Join(localAppData, "yanhekt", "settings.json")
	if err := os.MkdirAll(filepath.Dir(setPath), 0755); err != nil {
		return nil
	}
	merged := map[string]any{
		"downloadDir": downloadDir,
		"askSavePath": false,
		"defaultView": "camera",
		"dualPip":     false,
		"defaultRate": 1,
		"autoNext":    false,
		"rememberPos": true,
	}
	if b, err := os.ReadFile(setPath); err == nil && len(b) > 0 {
		var existing map[string]any
		if json.Unmarshal(b, &existing) == nil && existing != nil {
			for k, v := range existing {
				merged[k] = v
			}
			merged["downloadDir"] = downloadDir
			merged["askSavePath"] = false
		}
	}
	if out, err := json.MarshalIndent(merged, "", "  "); err == nil {
		_ = os.WriteFile(setPath, out, 0644)
	}
	return nil
}
