package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Live probe: requires YHKT_LIVE=1 and a _y2cookie.txt (never committed).
func TestLiveCourseTiming(t *testing.T) {
	if os.Getenv("YHKT_LIVE") != "1" {
		t.Skip("set YHKT_LIVE=1 to run")
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "_y2cookie.txt"))
	if err != nil {
		t.Skip("no _y2cookie.txt")
	}
	jwt := y2ExtractJwt(string(raw))
	if jwt == "" {
		t.Fatal("cookie has no JWT")
	}
	id := y2Who(jwt)
	if id == nil {
		t.Fatal("JWT invalid")
	}
	for _, cid := range strings.Split(os.Getenv("YHKT_IDS"), ",") {
		cid = strings.TrimSpace(cid)
		if cid == "" {
			continue
		}
		t0 := time.Now()
		d, derr := y2CourseDetail(jwt, cid, id.account)
		if derr != nil {
			t.Logf("%s detail failed: %v (%dms)", cid, derr, time.Since(t0).Milliseconds())
			continue
		}
		t.Logf("%s detail %dms sessions=%d title=%s", cid, time.Since(t0).Milliseconds(), len(d.sessions), d.title)

		t1 := time.Now()
		playable, failed, n := y2VerifySessions(jwt, cid, d.sessions, 3, 6)
		t.Logf("  probe %dms: probed=%d playable=%d failed=%d", time.Since(t1).Milliseconds(), n, len(playable), failed)

		t2 := time.Now()
		v, verr := y2Videos(jwt, id, cid, false, "", "", true)
		if verr != nil {
			t.Logf("  y2Videos failed: %v (%dms)", verr, time.Since(t2).Milliseconds())
			continue
		}
		t.Logf("  y2Videos %dms: source=%s videos=%d sessions=%d", time.Since(t2).Milliseconds(), v.source, v.videoCount, v.totalSessions)
	}
}
