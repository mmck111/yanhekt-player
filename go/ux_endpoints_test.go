package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestDownloadTrackerEndpoint(t *testing.T) {
	dlTrackMu.Lock()
	prev := dlTrackItems
	prevPath := dlTrackPathOverride
	dlTrackItems = nil
	dlTrackPathOverride = filepath.Join(t.TempDir(), "downloads.json")
	dlTrackInitOnce = sync.Once{}
	dlTrackMu.Unlock()
	defer func() {
		dlTrackMu.Lock()
		dlTrackItems = prev
		dlTrackPathOverride = prevPath
		dlTrackInitOnce = sync.Once{}
		dlTrackMu.Unlock()
	}()

	it := dlTrackStart("y2", "数值分析_第1讲.ts", 10, 1024)
	it.update(3, 300)
	it.markDone()

	req := httptest.NewRequest("GET", "/api/downloads", nil)
	rec := httptest.NewRecorder()
	handleDownloads(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}
	var res struct {
		Ok    bool          `json:"ok"`
		Items []dlTrackItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if !res.Ok || len(res.Items) != 1 {
		t.Fatalf("unexpected resp: %+v", res)
	}
	if res.Items[0].Status != "done" || res.Items[0].DoneSegs != 10 || res.Items[0].Bytes != 300 {
		t.Fatalf("unexpected item: %+v", res.Items[0])
	}

	run := dlTrackStart("old", "高数_第1讲.ts", 20, 0)
	called := false
	run.setCancel(func() { called = true })
	creq := httptest.NewRequest("POST", "/api/downloads/cancel?id="+run.ID, nil)
	crec := httptest.NewRecorder()
	handleDownloadsCancel(crec, creq)
	if crec.Code != 200 || !called || !run.isCancelled() || run.Status != "cancelled" {
		t.Fatalf("cancel failed code=%d called=%v status=%q", crec.Code, called, run.Status)
	}
}

func TestY2LogoutClearsCookie(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/y2/logout", nil)
	rec := httptest.NewRecorder()
	handleY2Logout(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}
	ck := rec.Header().Get("Set-Cookie")
	if !strings.Contains(ck, "y2tok=") || !strings.Contains(ck, "Max-Age=0") {
		t.Fatalf("Set-Cookie did not clear y2tok: %q", ck)
	}
}

func TestY2SearchLimitParam(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/y2/search?q=%E6%95%B0%E5%80%BC%E5%88%86%E6%9E%90&limit=3", nil)
	rec := httptest.NewRecorder()
	handleY2Search(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}
	var res struct {
		Ok      bool  `json:"ok"`
		Courses []any `json:"courses"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if !res.Ok || len(res.Courses) > 3 {
		t.Fatalf("expected <=3 courses, got %d", len(res.Courses))
	}
}

func TestDownloadTrackerPersistAndRecoverInterrupted(t *testing.T) {
	dlTrackMu.Lock()
	prev := dlTrackItems
	prevPath := dlTrackPathOverride
	dlTrackItems = nil
	dlTrackMu.Unlock()

	tmpFile := filepath.Join(t.TempDir(), "downloads.json")
	dlTrackPathOverride = tmpFile
	dlTrackInitOnce = sync.Once{}
	defer func() {
		dlTrackMu.Lock()
		dlTrackItems = prev
		dlTrackPathOverride = prevPath
		dlTrackInitOnce = sync.Once{}
		dlTrackMu.Unlock()
	}()

	partFile := filepath.Join(t.TempDir(), "partial.ts")
	if err := os.WriteFile(partFile, make([]byte, 4096), 0644); err != nil {
		t.Fatal(err)
	}

	running := dlTrackStart("y2", "高等数学.ts", 100, 0)
	running.update(42, 4096)
	dlTrackAttachFile(running.ID, "/y2dl?c=1&s=2&k=camera", partFile)

	done := dlTrackStart("old", "线性代数.ts", 10, 2048)
	done.markDone()

	// 模拟应用重新启动，从磁盘加载记录
	dlTrackMu.Lock()
	dlTrackItems = nil
	dlTrackMu.Unlock()
	dlTrackLoadNow()

	got, ok := dlTrackGet(running.ID)
	if !ok {
		t.Fatalf("expected persisted running item to survive reload")
	}
	if got.Status != "interrupted" || got.DoneSegs != 42 || got.SavePath != partFile {
		t.Fatalf("unexpected recovered item: %+v", got)
	}

	// clear 应当只清理已完成，保留未完成中断任务
	creq := httptest.NewRequest("POST", "/api/downloads/clear", nil)
	crec := httptest.NewRecorder()
	handleDownloadsClear(crec, creq)
	if crec.Code != 200 {
		t.Fatalf("clear status=%d", crec.Code)
	}
	if _, ok := dlTrackGet(done.ID); ok {
		t.Fatalf("expected done item to be cleared")
	}
	if _, ok := dlTrackGet(running.ID); !ok {
		t.Fatalf("expected interrupted item to remain after clear")
	}

	// remove?deleteFile=1 应当清理记录并删除未完成残留文件
	rreq := httptest.NewRequest("POST", "/api/downloads/remove?id="+running.ID+"&deleteFile=1", nil)
	rrec := httptest.NewRecorder()
	handleDownloadsRemove(rrec, rreq)
	if rrec.Code != 200 {
		t.Fatalf("remove status=%d", rrec.Code)
	}
	if _, err := os.Stat(partFile); !os.IsNotExist(err) {
		t.Fatalf("expected partial file to be removed")
	}
}

func TestOldSearchScoreAndTermSortKey(t *testing.T) {
	if got := oldTermSortKey("2025-2026 第二学期"); got != 20252 {
		t.Fatalf("want 20252, got %d", got)
	}
	if got := oldTermSortKey("2026-2027 第一学期"); got != 20261 {
		t.Fatalf("want 20261, got %d", got)
	}
	sExact := searchScore("数值分析", "孙新", "", "数值分析 孙新")
	sPartial := searchScore("工程数值分析导论", "孙新", "", "数值分析 孙新")
	if sExact <= sPartial || sPartial <= 0 {
		t.Fatalf("expected exact > partial > 0, got exact=%d partial=%d", sExact, sPartial)
	}
}

func TestY2ParseSubInfoMultipleAudiosAndLiveRowFallback(t *testing.T) {
	body := map[string]any{
		"code": float64(0),
		"data": map[string]any{
			"sub_status":   "6",
			"type":         "record",
			"course_title": "信号与系统",
			"video_list": []any{
				map[string]any{"type": "3", "preview_url": "https://x/cam.mp4"},
				map[string]any{"type": "2", "preview_url": "https://x/screen.mp4"},
			},
			"audio_list": []any{
				map[string]any{"name": "教师领夹麦", "url": "https://x/mic1.aac"},
				map[string]any{"title": "教室环境音", "url": "https://x/room.mp3"},
			},
		},
	}
	info, err := y2ParseSubInfo(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Audios) != 2 || info.Audios[0].Label != "教师领夹麦" || info.Audios[1].Label != "教室环境音" {
		t.Fatalf("unexpected audios: %+v", info.Audios)
	}
	if info.Audio != "https://x/mic1.aac" {
		t.Fatalf("expected info.Audio=%q, got %q", "https://x/mic1.aac", info.Audio)
	}

	row := liveRow(map[string]any{
		"id":      "991",
		"title":   "直播测试",
		"target":  "https://cdn/main.m3u8",
		"session": map[string]any{"target_vga": "https://cdn/vga.m3u8"},
	})
	if row.VGA != "https://cdn/vga.m3u8" {
		t.Fatalf("expected live VGA fallback from session, got %q", row.VGA)
	}
}

func TestDownloadQueueTransitions(t *testing.T) {
	dlTrackMu.Lock()
	prev := dlTrackItems
	prevPath := dlTrackPathOverride
	dlTrackItems = nil
	dlTrackPathOverride = filepath.Join(t.TempDir(), "downloads.json")
	dlTrackInitOnce = sync.Once{}
	dlTrackMu.Unlock()
	defer func() {
		dlTrackMu.Lock()
		dlTrackItems = prev
		dlTrackPathOverride = prevPath
		dlTrackInitOnce = sync.Once{}
		dlTrackMu.Unlock()
	}()

	q1 := dlTrackEnqueue("y2", "第1讲.mp4", "/y2dl?c=1&s=10")
	q2 := dlTrackEnqueue("y2", "第2讲.mp4", "/y2dl?c=1&s=11")
	if dlTrackRunningCount() != 2 {
		t.Fatalf("want 2 queued items counted as active, got %d", dlTrackRunningCount())
	}
	bound := dlTrackBindOrStart(q1.ID, "y2", "数值分析_第1讲.mp4", 4, 4096)
	if bound.ID != q1.ID || bound.Status != "running" || bound.Title != "数值分析_第1讲.mp4" {
		t.Fatalf("unexpected bind result: %+v", bound)
	}
	dlTrackMarkAllInterrupted()
	g2, ok := dlTrackGet(q2.ID)
	if !ok || g2.Status != "interrupted" {
		t.Fatalf("expected queued item q2 interrupted on shutdown, got %+v", g2)
	}
}

func TestY2DownloadMP4ChunkedResumeAndRetry(t *testing.T) {
	dlTrackMu.Lock()
	prev := dlTrackItems
	prevPath := dlTrackPathOverride
	dlTrackItems = nil
	dlTrackPathOverride = filepath.Join(t.TempDir(), "downloads.json")
	dlTrackInitOnce = sync.Once{}
	dlTrackMu.Unlock()
	defer func() {
		dlTrackMu.Lock()
		dlTrackItems = prev
		dlTrackPathOverride = prevPath
		dlTrackInitOnce = sync.Once{}
		dlTrackMu.Unlock()
	}()

	// 9MB 伪 MP4：会分成 4MB + 4MB + 1MB 共 3 个分块；第 2 块首请求模拟 502 瞬断后自动重试恢复
	payload := make([]byte, 9<<20)
	for i := range payload {
		payload[i] = byte((i * 131 + 17) & 0xff)
	}
	var failChunk2 atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rg := r.Header.Get("Range")
		if rg == "" {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			_, _ = w.Write(payload)
			return
		}
		var s, e int
		if _, err := fmt.Sscanf(rg, "bytes=%d-%d", &s, &e); err != nil {
			http.Error(w, "bad range", 400)
			return
		}
		if s == 4<<20 && failChunk2.Add(1) == 1 {
			http.Error(w, "transient upstream reset", 502)
			return
		}
		if e >= len(payload) {
			e = len(payload) - 1
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", s, e, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(e-s+1))
		w.WriteHeader(206)
		_, _ = w.Write(payload[s : e+1])
	}))
	defer srv.Close()

	ident := &y2Identity{userId: 1, tenantId: 21, phone: "13800138000"}
	req := httptest.NewRequest("GET", "/y2dl?c=1&s=2&title=test", nil)
	rec := httptest.NewRecorder()
	err := y2DownloadMP4(rec, req, srv.URL+"/play/test.mp4", ident, y2ProxyOpts{
		filename: "test.mp4",
		ext:      ".mp4",
		reqPath:  "/y2dl?c=1&s=2&title=test",
	})
	if err != nil {
		t.Fatalf("y2DownloadMP4 err=%v", err)
	}
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), payload) {
		t.Fatalf("reconstructed mismatch: code=%d got=%d want=%d", rec.Code, rec.Body.Len(), len(payload))
	}
	dlID := rec.Header().Get("X-YHKT-DL-ID")
	it, ok := dlTrackGet(dlID)
	if !ok || it.Status != "done" || it.Bytes != int64(len(payload)) {
		t.Fatalf("unexpected tracker item: ok=%v %+v", ok, it)
	}
}
