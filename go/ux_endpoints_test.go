package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDownloadTrackerEndpoint(t *testing.T) {
	dlTrackMu.Lock()
	prev := dlTrackItems
	dlTrackItems = nil
	dlTrackMu.Unlock()
	defer func() {
		dlTrackMu.Lock()
		dlTrackItems = prev
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
