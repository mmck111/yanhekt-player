package main

import (
	"testing"
	"time"
)

// 平台给的是北京时间墙钟串，解析结果必须跟设备时区无关。
// 旧实现用 time.Local：手机上没有 tzdata 时退化成 UTC，整体偏 8 小时，
// 于是正在直播的课被判成"未开始"，几小时后开播的又被窗口过滤掉。
func TestParseLocalTimeIsBeijing(t *testing.T) {
	// 模拟 Android：把进程本地时区硬掰成 UTC（Windows 上 TZ 环境变量不生效，
	// 直接改 time.Local 才是真的复现）。旧实现这里会差 8 小时。
	old := time.Local
	time.Local = time.UTC
	defer func() { time.Local = old }()

	cases := map[string]time.Time{
		"2026-10-23 14:00:00": time.Date(2026, 10, 23, 14, 0, 0, 0, bj),
		"2026-10-23 14:00":    time.Date(2026, 10, 23, 14, 0, 0, 0, bj),
		"2026-10-23":          time.Date(2026, 10, 23, 0, 0, 0, 0, bj),
	}
	for in, want := range cases {
		got := parseLocalTime(in)
		if got != want.UnixMilli() {
			t.Fatalf("parseLocalTime(%q) = %d, want %d（差 %d 小时，时区没固定住）",
				in, got, want.UnixMilli(), (got-want.UnixMilli())/3600000)
		}
	}
	if parseLocalTime("") != 0 {
		t.Fatal("空串应当返回 0")
	}
}

// 北京时间 14:10 就是 UTC 06:10 —— 解析和比较都得落在这个绝对时刻上
func TestLivePhaseUsesBeijing(t *testing.T) {
	start := time.Date(2026, 10, 23, 14, 0, 0, 0, bj)
	now := start.Add(10 * time.Minute)
	if off := now.Sub(time.Date(2026, 10, 23, 6, 10, 0, 0, time.UTC)); off != 0 {
		t.Fatalf("北京时间 14:10 应该等于 UTC 06:10，差 %v", off)
	}
	if want := time.Date(2026, 10, 23, 6, 0, 0, 0, time.UTC); parseLocalTime("2026-10-23 14:00:00") != want.UnixMilli() {
		t.Fatalf("解析出的绝对时刻不对：%d", parseLocalTime("2026-10-23 14:00:00"))
	}
}
