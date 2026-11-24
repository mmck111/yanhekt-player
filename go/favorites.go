package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxFavoriteCourses = 100

// favItem 收藏课程条目（统一覆盖老平台 old 与新版平台 y2）
type favItem struct {
	Key     string         `json:"key"`
	Plat    string         `json:"plat"`
	ID      string         `json:"id"`
	Title   string         `json:"title"`
	Teacher string         `json:"teacher,omitempty"`
	Term    string         `json:"term,omitempty"`
	Code    string         `json:"code,omitempty"`
	Room    string         `json:"room,omitempty"`
	College string         `json:"college,omitempty"`
	TS      int64          `json:"ts"`
	Raw     map[string]any `json:"raw,omitempty"`
}

var (
	favMu                 sync.Mutex
	favoritesPathOverride string
)

func favoritesFilePaths() []string {
	return yanhektCachePaths("favorites.json", favoritesPathOverride)
}

func normalizeFavItem(in favItem) (favItem, bool) {
	in.Plat = strings.TrimSpace(in.Plat)
	in.ID = strings.TrimSpace(in.ID)
	if (in.Plat != "old" && in.Plat != "y2") || in.ID == "" {
		return favItem{}, false
	}
	in.Key = in.Plat + ":" + in.ID
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		in.Title = "课程 " + in.ID
	}
	in.Teacher = strings.TrimSpace(in.Teacher)
	in.Term = strings.TrimSpace(in.Term)
	in.Code = strings.TrimSpace(in.Code)
	in.Room = strings.TrimSpace(in.Room)
	in.College = strings.TrimSpace(in.College)
	if in.TS <= 0 {
		in.TS = time.Now().UnixMilli()
	}
	return in, true
}

func sanitizeFavList(list []favItem) []favItem {
	out := make([]favItem, 0, len(list))
	seen := make(map[string]bool, len(list))
	for _, item := range list {
		norm, ok := normalizeFavItem(item)
		if !ok || seen[norm.Key] {
			continue
		}
		seen[norm.Key] = true
		out = append(out, norm)
		if len(out) >= maxFavoriteCourses {
			break
		}
	}
	return out
}

func loadFavoritesUnlocked() []favItem {
	for _, p := range favoritesFilePaths() {
		b, err := os.ReadFile(p)
		if err != nil || len(b) == 0 {
			continue
		}
		var items []favItem
		if json.Unmarshal(b, &items) == nil {
			return sanitizeFavList(items)
		}
	}
	return []favItem{}
}

func saveFavoritesUnlocked(items []favItem) error {
	items = sanitizeFavList(items)
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	var lastErr error
	for _, p := range favoritesFilePaths() {
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

func handleFavorites(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		favMu.Lock()
		items := loadFavoritesUnlocked()
		favMu.Unlock()
		writeJSON(w, 200, map[string]any{"ok": true, "items": items})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 512<<10))
	if err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "读取收藏数据失败"})
		return
	}
	var req struct {
		Op    string    `json:"op"`
		Key   string    `json:"key"`
		Item  favItem   `json:"item"`
		Items []favItem `json:"items"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "收藏数据格式错误：" + err.Error()})
		return
	}

	favMu.Lock()
	defer favMu.Unlock()
	cur := loadFavoritesUnlocked()
	added := false
	op := strings.TrimSpace(req.Op)
	if op == "" {
		op = "replace"
	}

	switch op {
	case "toggle":
		item, ok := normalizeFavItem(req.Item)
		if !ok {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "缺少有效的课程信息"})
			return
		}
		next := make([]favItem, 0, len(cur)+1)
		removed := false
		for _, it := range cur {
			if it.Key == item.Key {
				removed = true
				continue
			}
			next = append(next, it)
		}
		if !removed {
			added = true
			next = append([]favItem{item}, next...)
		}
		cur = sanitizeFavList(next)
		if err := saveFavoritesUnlocked(cur); err != nil {
			writeJSON(w, 500, map[string]any{"ok": false, "error": "保存收藏失败：" + err.Error()})
			return
		}
	case "remove":
		k := strings.TrimSpace(req.Key)
		if k == "" && req.Item.Plat != "" && req.Item.ID != "" {
			k = strings.TrimSpace(req.Item.Plat) + ":" + strings.TrimSpace(req.Item.ID)
		}
		if k != "" {
			next := make([]favItem, 0, len(cur))
			changed := false
			for _, it := range cur {
				if it.Key == k {
					changed = true
					continue
				}
				next = append(next, it)
			}
			if changed {
				cur = next
				if err := saveFavoritesUnlocked(cur); err != nil {
					writeJSON(w, 500, map[string]any{"ok": false, "error": "保存收藏失败：" + err.Error()})
					return
				}
			}
		}
	case "clear":
		cur = []favItem{}
		if err := saveFavoritesUnlocked(cur); err != nil {
			writeJSON(w, 500, map[string]any{"ok": false, "error": "清空收藏失败：" + err.Error()})
			return
		}
	case "merge":
		merged := sanitizeFavList(append(append([]favItem{}, cur...), req.Items...))
		if len(merged) != len(cur) {
			cur = merged
			if err := saveFavoritesUnlocked(cur); err != nil {
				writeJSON(w, 500, map[string]any{"ok": false, "error": "同步收藏失败：" + err.Error()})
				return
			}
		}
	case "replace":
		cur = sanitizeFavList(req.Items)
		if err := saveFavoritesUnlocked(cur); err != nil {
			writeJSON(w, 500, map[string]any{"ok": false, "error": "保存收藏失败：" + err.Error()})
			return
		}
	default:
		writeJSON(w, 400, map[string]any{"ok": false, "error": "不支持的操作类型：" + op})
		return
	}

	writeJSON(w, 200, map[string]any{"ok": true, "items": cur, "added": added})
}
