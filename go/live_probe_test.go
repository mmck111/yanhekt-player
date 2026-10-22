package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Live probe: needs YHKT_LIVE=1 and _y2tok.txt (never committed).
func TestLiveCourseTiming(t *testing.T) {
	if os.Getenv("YHKT_LIVE") != "1" {
		t.Skip("set YHKT_LIVE=1 to run")
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "_y2tok.txt"))
	if err != nil {
		t.Skip("no _y2tok.txt")
	}
	jwt := y2ExtractJwt(string(raw))
	if jwt == "" {
		t.Fatal("cookie has no JWT")
	}
	id := y2Who(jwt)
	if id == nil {
		t.Fatal("JWT invalid")
	}

	// 1) 自己的课表：一次拉 150 天，看第二次是否命中缓存
	for i := 0; i < 2; i++ {
		t0 := time.Now()
		mp, serr := y2Schedule(jwt, id)
		courses := len(mp)
		rows := 0
		for _, v := range mp {
			rows += len(v)
		}
		t.Logf("y2Schedule #%d: %dms courses=%d rows=%d err=%v", i+1, time.Since(t0).Milliseconds(), courses, rows, serr)
	}

	for _, cid := range strings.Split(os.Getenv("YHKT_IDS"), ",") {
		cid = strings.TrimSpace(cid)
		if cid == "" {
			continue
		}
		t0 := time.Now()
		d, derr := y2CourseDetail(jwt, cid, id.account)
		if derr != nil {
			t.Logf("%s detail failed: %v", cid, derr)
			continue
		}
		t.Logf("%s detail %dms sessions=%d title=%s", cid, time.Since(t0).Milliseconds(), len(d.sessions), d.title)

		t1 := time.Now()
		v, verr := y2Videos(jwt, id, cid, false, "", "", true)
		if verr != nil {
			t.Logf("  deep failed: %v", verr)
			continue
		}
		t.Logf("  deep %dms: source=%s videos=%d sessions=%d exact=%v",
			time.Since(t1).Milliseconds(), v.source, v.videoCount, v.totalSessions, v.countExact)
	}
}
