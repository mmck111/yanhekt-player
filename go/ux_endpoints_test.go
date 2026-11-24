package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
