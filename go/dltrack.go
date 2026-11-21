// 下载进度跟踪：为网页端「下载管理」面板提供 /dl 与 /y2dl 的实时进度
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type dlTrackItem struct {
	ID         string `json:"id"`
	Platform   string `json:"platform"` // "old" | "y2"
	Title      string `json:"title"`
	DoneSegs   int    `json:"doneSegs"`
	TotalSegs  int    `json:"totalSegs"`
	Bytes      int64  `json:"bytes"`
	TotalBytes int64  `json:"totalBytes"`
	Status     string `json:"status"` // "running" | "done" | "cancelled" | "error"
	Error      string `json:"error,omitempty"`
	StartedAt  int64  `json:"startedAt"`
	UpdatedAt  int64  `json:"updatedAt"`

	cancel    func()
	cancelled bool
}

var (
	dlTrackMu    sync.Mutex
	dlTrackItems = []*dlTrackItem{}
	dlTrackSeq   uint64
)

func dlTrackStart(platform, title string, totalSegs int, totalBytes int64) *dlTrackItem {
	id := fmt.Sprintf("dl-%d", atomic.AddUint64(&dlTrackSeq, 1))
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
	if len(dlTrackItems) > 20 {
		dlTrackItems = dlTrackItems[:20]
	}
	dlTrackMu.Unlock()
	return it
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
}

func (it *dlTrackItem) addBytes(n int64) {
	if it == nil || n <= 0 {
		return
	}
	dlTrackMu.Lock()
	it.Bytes += n
	it.UpdatedAt = time.Now().UnixMilli()
	dlTrackMu.Unlock()
}

func (it *dlTrackItem) finish(status, errMsg string) {
	if it == nil {
		return
	}
	dlTrackMu.Lock()
	it.Status = status
	it.Error = errMsg
	it.UpdatedAt = time.Now().UnixMilli()
	dlTrackMu.Unlock()
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
	dlTrackMu.Lock()
	out := make([]dlTrackItem, 0, len(dlTrackItems))
	for _, it := range dlTrackItems {
		out = append(out, *it)
	}
	dlTrackMu.Unlock()
	writeJSON(w, 200, map[string]any{"ok": true, "items": out})
}

func handleDownloadsCancel(w http.ResponseWriter, r *http.Request) {
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
