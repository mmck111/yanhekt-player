// 下载进度跟踪：为网页端「下载管理」面板提供 /dl 与 /y2dl 的实时进度
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type dlTrackItem struct {
	ID         string `json:"id"`
	Platform   string `json:"platform"` // "old" | "y2"
	Title      string `json:"title"`
	ReqPath    string `json:"reqPath,omitempty"`
	SavePath   string `json:"savePath,omitempty"`
	DoneSegs   int    `json:"doneSegs"`
	TotalSegs  int    `json:"totalSegs"`
	Bytes      int64  `json:"bytes"`
	TotalBytes int64  `json:"totalBytes"`
	Status     string `json:"status"` // "running" | "done" | "interrupted" | "cancelled" | "error"
	Error      string `json:"error,omitempty"`
	StartedAt  int64  `json:"startedAt"`
	UpdatedAt  int64  `json:"updatedAt"`

	cancel    func()
	cancelled bool
	lastSave  int64
}

var (
	dlTrackMu           sync.Mutex
	dlTrackItems        = []*dlTrackItem{}
	dlTrackSeq          uint64
	dlTrackInitOnce     sync.Once
	dlTrackPathOverride string
)

func dlTrackFilePaths() []string {
	if dlTrackPathOverride != "" {
		return []string{dlTrackPathOverride}
	}
	var dirs []string
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		dirs = append(dirs, filepath.Join(dir, "yanhekt"))
	}
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		dirs = append(dirs, filepath.Join(dir, "yanhekt"))
	}
	if t := os.TempDir(); t != "" {
		dirs = append(dirs, filepath.Join(t, "yanhekt"))
	}
	if exe, err := os.Executable(); err == nil && exe != "" {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), ".yanhekt-cache"))
	}
	out := make([]string, 0, len(dirs))
	seen := map[string]bool{}
	for _, d := range dirs {
		p := filepath.Join(d, "downloads.json")
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func dlTrackInit() {
	dlTrackInitOnce.Do(dlTrackLoadNow)
}

func dlTrackLoadNow() {
	var bestPath string
	var bestTime time.Time
	for _, p := range dlTrackFilePaths() {
		if fi, err := os.Stat(p); err == nil {
			if bestPath == "" || fi.ModTime().After(bestTime) {
				bestPath, bestTime = p, fi.ModTime()
			}
		}
	}
	if bestPath == "" {
		return
	}
	b, err := os.ReadFile(bestPath)
	if err != nil || len(b) == 0 {
		return
	}
	var loaded []*dlTrackItem
	if json.Unmarshal(b, &loaded) != nil {
		return
	}
	changed := false
	cleaned := make([]*dlTrackItem, 0, len(loaded))
	for _, it := range loaded {
		if it == nil || it.ID == "" {
			continue
		}
		if it.Status == "running" {
			it.Status = "interrupted"
			it.Error = "上次窗口关闭时未完成"
			changed = true
		}
		if it.Status == "interrupted" && it.SavePath != "" {
			if fi, err := os.Stat(it.SavePath); err == nil && fi.Size() > it.Bytes {
				it.Bytes = fi.Size()
				changed = true
			}
		}
		cleaned = append(cleaned, it)
	}
	dlTrackMu.Lock()
	dlTrackItems = cleaned
	dlTrackMu.Unlock()
	if changed {
		dlTrackSaveLocked(false)
	}
}

func dlTrackSaveLocked(throttle bool) {
	now := time.Now().UnixMilli()
	dlTrackMu.Lock()
	if throttle && len(dlTrackItems) > 0 {
		last := dlTrackItems[0].lastSave
		if now-last < 1500 {
			dlTrackMu.Unlock()
			return
		}
		dlTrackItems[0].lastSave = now
	} else if len(dlTrackItems) > 0 {
		dlTrackItems[0].lastSave = now
	}
	snap := make([]dlTrackItem, 0, len(dlTrackItems))
	for _, it := range dlTrackItems {
		snap = append(snap, *it)
	}
	dlTrackMu.Unlock()

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return
	}
	for _, p := range dlTrackFilePaths() {
		if os.MkdirAll(filepath.Dir(p), 0755) != nil {
			continue
		}
		tmp := p + ".tmp"
		if os.WriteFile(tmp, data, 0600) == nil {
			if os.Rename(tmp, p) == nil {
				return
			}
			_ = os.Remove(tmp)
		}
		if os.WriteFile(p, data, 0600) == nil {
			return
		}
	}
}

func cleanDLReqPath(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
	q := u.Query()
	q.Del("resumeId")
	q.Del("startSeg")
	q.Del("startBytes")
	q.Del("probe")
	out := u.Path
	if enc := q.Encode(); enc != "" {
		out += "?" + enc
	}
	return out
}

func dlTrackStart(platform, title string, totalSegs int, totalBytes int64) *dlTrackItem {
	dlTrackInit()
	id := fmt.Sprintf("dl-%d-%d", time.Now().UnixMilli(), atomic.AddUint64(&dlTrackSeq, 1))
	now := time.Now().UnixMilli()
	it := &dlTrackItem{
		ID:         id,
		Platform:   platform,
		Title:      title,
		TotalSegs:  totalSegs,
		TotalBytes: totalBytes,
		Status:     "running",
		StartedAt:  now,
		UpdatedAt:  now,
	}
	dlTrackMu.Lock()
	dlTrackItems = append([]*dlTrackItem{it}, dlTrackItems...)
	if len(dlTrackItems) > 30 {
		dlTrackItems = dlTrackItems[:30]
	}
	dlTrackMu.Unlock()
	dlTrackSaveLocked(false)
	return it
}

func (it *dlTrackItem) setReqPath(reqPath string) {
	if it == nil || reqPath == "" {
		return
	}
	dlTrackMu.Lock()
	it.ReqPath = cleanDLReqPath(reqPath)
	dlTrackMu.Unlock()
	dlTrackSaveLocked(false)
}

func (it *dlTrackItem) setResumeInit(doneSegs int, bytes int64) {
	if it == nil {
		return
	}
	dlTrackMu.Lock()
	if doneSegs > 0 {
		it.DoneSegs = doneSegs
	}
	if bytes > 0 {
		it.Bytes = bytes
	}
	it.UpdatedAt = time.Now().UnixMilli()
	dlTrackMu.Unlock()
}

func dlTrackAttachFile(id, reqPath, savePath string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	dlTrackMu.Lock()
	for _, it := range dlTrackItems {
		if it.ID == id {
			if reqPath != "" {
				it.ReqPath = cleanDLReqPath(reqPath)
			}
			if savePath != "" {
				it.SavePath = savePath
			}
			it.UpdatedAt = time.Now().UnixMilli()
			break
		}
	}
	dlTrackMu.Unlock()
	dlTrackSaveLocked(false)
}

func dlTrackGet(id string) (dlTrackItem, bool) {
	dlTrackInit()
	id = strings.TrimSpace(id)
	dlTrackMu.Lock()
	defer dlTrackMu.Unlock()
	for _, it := range dlTrackItems {
		if it.ID == id {
			return *it, true
		}
	}
	return dlTrackItem{}, false
}

func dlTrackDeleteEntry(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	removed := false
	dlTrackMu.Lock()
	out := make([]*dlTrackItem, 0, len(dlTrackItems))
	for _, it := range dlTrackItems {
		if it.ID == id {
			removed = true
			continue
		}
		out = append(out, it)
	}
	dlTrackItems = out
	dlTrackMu.Unlock()
	if removed {
		dlTrackSaveLocked(false)
	}
	return removed
}

func dlTrackRunningCount() int {
	dlTrackInit()
	dlTrackMu.Lock()
	defer dlTrackMu.Unlock()
	n := 0
	for _, it := range dlTrackItems {
		if it.Status == "running" {
			n++
		}
	}
	return n
}

func dlTrackMarkAllInterrupted() {
	dlTrackMu.Lock()
	changed := false
	var cancels []func()
	now := time.Now().UnixMilli()
	for _, it := range dlTrackItems {
		if it.Status == "running" {
			it.Status = "interrupted"
			it.Error = "窗口已关闭，下载暂停"
			it.UpdatedAt = now
			if it.cancel != nil {
				cancels = append(cancels, it.cancel)
			}
			changed = true
		}
	}
	dlTrackMu.Unlock()
	if changed {
		dlTrackSaveLocked(false)
	}
	for _, fn := range cancels {
		fn()
	}
}

func dlTrackSetStatus(id, status, errMsg string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	dlTrackMu.Lock()
	for _, it := range dlTrackItems {
		if it.ID == id {
			it.Status = status
			it.Error = errMsg
			it.UpdatedAt = time.Now().UnixMilli()
			break
		}
	}
	dlTrackMu.Unlock()
	dlTrackSaveLocked(false)
}

func (it *dlTrackItem) update(doneSegs int, bytes int64) {
	if it == nil {
		return
	}
	dlTrackMu.Lock()
	it.DoneSegs = doneSegs
	it.Bytes = bytes
	it.UpdatedAt = time.Now().UnixMilli()
	dlTrackMu.Unlock()
	dlTrackSaveLocked(true)
}

func (it *dlTrackItem) addBytes(n int64) {
	if it == nil || n <= 0 {
		return
	}
	dlTrackMu.Lock()
	it.Bytes += n
	it.UpdatedAt = time.Now().UnixMilli()
	dlTrackMu.Unlock()
	dlTrackSaveLocked(true)
}

func (it *dlTrackItem) finish(status, errMsg string) {
	if it == nil {
		return
	}
	dlTrackMu.Lock()
	if it.Status == "interrupted" && status == "cancelled" && !it.cancelled {
		status = "interrupted"
	}
	it.Status = status
	it.Error = errMsg
	it.UpdatedAt = time.Now().UnixMilli()
	dlTrackMu.Unlock()
	dlTrackSaveLocked(false)
}

func (it *dlTrackItem) markDone() {
	if it == nil {
		return
	}
	dlTrackMu.Lock()
	it.DoneSegs = it.TotalSegs
	it.Status = "done"
	it.Error = ""
	it.UpdatedAt = time.Now().UnixMilli()
	dlTrackMu.Unlock()
	dlTrackSaveLocked(false)
}

func (it *dlTrackItem) setCancel(fn func()) {
	if it == nil {
		return
	}
	dlTrackMu.Lock()
	already := it.cancelled
	it.cancel = fn
	dlTrackMu.Unlock()
	if already && fn != nil {
		fn()
	}
}

func (it *dlTrackItem) isCancelled() bool {
	if it == nil {
		return false
	}
	dlTrackMu.Lock()
	c := it.cancelled
	dlTrackMu.Unlock()
	return c
}

func dlTrackCancel(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	var fn func()
	found := false
	dlTrackMu.Lock()
	for _, it := range dlTrackItems {
		if it.ID == id {
			found = true
			if it.Status == "running" {
				it.cancelled = true
				it.Status = "cancelled"
				it.Error = "用户已取消"
				it.UpdatedAt = time.Now().UnixMilli()
				fn = it.cancel
			}
			break
		}
	}
	dlTrackMu.Unlock()
	if fn != nil {
		fn()
	}
	if found {
		dlTrackSaveLocked(false)
	}
	return found
}

func dlTrackIsCancelled(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	dlTrackMu.Lock()
	defer dlTrackMu.Unlock()
	for _, it := range dlTrackItems {
		if it.ID == id {
			return it.cancelled || it.Status == "cancelled"
		}
	}
	return false
}

func handleDownloads(w http.ResponseWriter, r *http.Request) {
	dlTrackInit()
	dlTrackMu.Lock()
	out := make([]dlTrackItem, 0, len(dlTrackItems))
	for _, it := range dlTrackItems {
		out = append(out, *it)
	}
	dlTrackMu.Unlock()
	writeJSON(w, 200, map[string]any{"ok": true, "items": out})
}

func handleDownloadsCancel(w http.ResponseWriter, r *http.Request) {
	dlTrackInit()
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" && r.Body != nil {
		var body struct {
			ID string `json:"id"`
		}
		b, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		if len(b) > 0 && json.Unmarshal(b, &body) == nil {
			id = strings.TrimSpace(body.ID)
		}
	}
	ok := dlTrackCancel(id)
	writeJSON(w, 200, map[string]any{"ok": ok})
}

func handleDownloadsRemove(w http.ResponseWriter, r *http.Request) {
	dlTrackInit()
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	delFile := r.URL.Query().Get("deleteFile") == "1"
	it, ok := dlTrackGet(id)
	if !ok {
		writeJSON(w, 200, map[string]any{"ok": false})
		return
	}
	if it.Status == "running" {
		dlTrackCancel(id)
	}
	if delFile && it.Status != "done" && it.SavePath != "" {
		_ = os.Remove(it.SavePath)
	}
	removed := dlTrackDeleteEntry(id)
	writeJSON(w, 200, map[string]any{"ok": removed})
}

func handleDownloadsClear(w http.ResponseWriter, r *http.Request) {
	dlTrackInit()
	dlTrackMu.Lock()
	kept := make([]*dlTrackItem, 0, len(dlTrackItems))
	removed := 0
	for _, it := range dlTrackItems {
		if it.Status == "done" || it.Status == "cancelled" || it.Status == "error" {
			removed++
			continue
		}
		kept = append(kept, it)
	}
	dlTrackItems = kept
	dlTrackMu.Unlock()
	if removed > 0 {
		dlTrackSaveLocked(false)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "removed": removed})
}
