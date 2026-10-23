package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 没登录也能用本地课程库搜（手机端第一次打开就有东西搜），而且必须是本地匹配、毫秒级
func TestY2SearchLocalWhenNotLoggedIn(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/y2/search?q=%E6%95%B0%E5%80%BC%E5%88%86%E6%9E%90", nil) // 数值分析
	rec := httptest.NewRecorder()
	t0 := time.Now()
	handleY2Search(rec, req)
	elapsed := time.Since(t0)

	if rec.Code != 200 {
		t.Fatalf("code=%d body=%.200s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"source":"local"`) {
		t.Fatalf("应当走本地课程库：%.300s", body)
	}
	if !strings.Contains(body, "数值分析") {
		t.Fatalf("课程库里应该有「数值分析」：%.300s", body)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("本地课程库检索不该超过 2s，实际 %v", elapsed)
	}
	t.Logf("本地检索耗时 %v", elapsed)
}
