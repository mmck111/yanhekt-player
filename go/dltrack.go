// 下载进度跟踪：为网页端「下载管理」面板提供 /dl 与 /y2dl 的实时进度
package main

import (
	"fmt"
	"net/http"
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

func handleDownloads(w http.ResponseWriter, r *http.Request) {
	dlTrackMu.Lock()
	out := make([]dlTrackItem, 0, len(dlTrackItems))
	for _, it := range dlTrackItems {
		out = append(out, *it)
	}
	dlTrackMu.Unlock()
	writeJSON(w, 200, map[string]any{"ok": true, "items": out})
}
