package main

import (
	"testing"
)

// 判定结果要能跨进程存活：手机端重开 App 后，同一批课不该再重新探一遍
func TestOldVerdictCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", dir)
	t.Setenv("LOCALAPPDATA", dir)
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)

	reset := func() {
		oldVerMu.Lock()
		oldVerCache = nil
		oldVerDirty = false
		oldVerMu.Unlock()
	}
	defer reset()
	reset()

	n := 3
	oldVerdictSet("12345", "tokentokentoken", oldVerdict{Videos: &n, Verified: true, Room: "综教A305"})
	oldVerdictFlush(true)

	// 模拟 App 重开：内存清空，只留磁盘
	reset()
	got, ok := oldVerdictGet("12345", "tokentokentoken")
	if !ok {
		t.Fatal("落盘后读不回来")
	}
	if !got.Verified || got.Videos == nil || *got.Videos != 3 || got.Room != "综教A305" {
		t.Fatalf("读回来的判定不对：%+v", got)
	}

	// 换个账号（token 不同）不该命中别人的判定
	if _, ok := oldVerdictGet("12345", "anothertoken00"); ok {
		t.Fatal("不同账号不该共用判定缓存")
	}
}
