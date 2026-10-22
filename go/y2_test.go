// 新版平台纯函数的交叉验证
//
// 这里的每一个期望值都是 **Node 版 yanhe2.js 真跑出来的**，不是手算的：
//
//	node tools\parity-y2-pure.js
//
// 那份输出的 JSON 直接抄成了本文件的常量。改 yanhe2.js 的纯函数之后，
// 重跑上面那条命令、更新常量即可。
//
// 注意：tsOf / dateOf 处理"YYYY-MM-DD HH:mm:ss"时两边都按**本地时区**解析
// （Node 是 Date.parse，Go 是 time.ParseInLocation(..., time.Local)），
// 所以这两个函数的期望值里带时间的那几条写死的是本机时区 Asia/Shanghai 的结果。
package main

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestY2ExtractJwt 覆盖：裸 JWT / 整段 cookie / URL 编码 / PHP 序列化 / 带引号 / 空串
func TestY2ExtractJwt(t *testing.T) {
	const jwt = "eyJhbGciOiJIUzI1NiJ9.eyJpZCI6MTIzNDV9.k3Jh-dEf_9xQ"
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"裸 JWT", jwt, jwt},
		{"整段 cookie", "_token=" + jwt + "; locale=zh_cn", jwt},
		{"URL 编码的 cookie",
			"_token%3DeyJhbGciOiJIUzI1NiJ9.eyJpZCI6MTIzNDV9.k3Jh-dEf_9xQ%3B%20locale%3Dzh_cn", jwt},
		{"PHP 序列化",
			`a:2:{i:0;s:6:"_token";i:1;s:50:"eyJhbGciOiJIUzI1NiJ9.eyJpZCI6MTIzNDV9.k3Jh-dEf_9xQ";}`, jwt},
		{"URL 编码的 PHP 序列化",
			"a%3A2%3A%7Bi%3A0%3Bs%3A6%3A%22_token%22%3Bi%3A1%3Bs%3A50%3A%22eyJhbGciOiJIUzI1NiJ9.eyJpZCI6MTIzNDV9.k3Jh-dEf_9xQ%22%3B%7D", jwt},
		{"带双引号", `"` + jwt + `"`, jwt},
		{"带单引号+空格", "  '" + jwt + "'  ", jwt},
		{"空串", "", ""},
		{"空白串", "   ", ""},
		{"只有 _token= 没有 JWT 形状", "_token=abc.def.ghi", "abc.def.ghi"},
		{"认不出的整段", "hello world", "hello world"},
		{"eyJ 开头但不是完整 JWT", "eyJhbGciOiJIUzI1NiJ9", "eyJhbGciOiJIUzI1NiJ9"},
	}
	for _, c := range cases {
		if got := y2ExtractJwt(c.in); got != c.want {
			t.Errorf("extractJwt(%s) = %q，期望 %q", c.name, got, c.want)
		}
	}
}

// TestY2SignPath pathname/userId/tenantId/phone/ts 给定后逐字符一致
func TestY2SignPath(t *testing.T) {
	ids := []struct {
		name string
		id   *y2Identity
	}{
		{"常规", &y2Identity{userId: 12345, tenantId: 21, phone: "13800138000"}},
		{"没手机号", &y2Identity{userId: 7, tenantId: 21, phone: ""}},
		{"零散字段", &y2Identity{userId: 1, tenantId: 0, phone: "1"}},
	}
	// 和 tools/parity-y2-pure.js 的输出一一对应
	want := map[string]map[string]string{
		"常规": {
			"/play/abc.mp4|1700000000":   "12345-1700000000-d622cbf80e0ad907acc19084f5466afa",
			"/play/x/y/z.mp4|1700000123": "12345-1700000123-1213a985a8bbba7089529bd43c9ecb8f",
			"/live/a.m3u8|1":             "12345-1-902bc2b7521fc1695624cd2f9d59dfe7",
		},
		"没手机号": {
			"/play/abc.mp4|1700000000":   "7-1700000000-ec9e166e48d3a9934d32dddf8d605c2f",
			"/play/x/y/z.mp4|1700000123": "7-1700000123-35d20d616e7788849deb1e2e05e70d61",
			"/live/a.m3u8|1":             "7-1-cad6a7f8af4bb2a8730edb3c437bbaf9",
		},
		"零散字段": {
			"/play/abc.mp4|1700000000":   "1-1700000000-81eaf7ccea11e8a1c7a62ce13622a877",
			"/play/x/y/z.mp4|1700000123": "1-1700000123-161deb371362a922464bceb18719899e",
			"/live/a.m3u8|1":             "1-1-fcf39a339fcb26292286e79453ce365e",
		},
	}
	paths := []struct {
		path string
		ts   int64
	}{
		{"/play/abc.mp4", 1700000000},
		{"/play/x/y/z.mp4", 1700000123},
		{"/live/a.m3u8", 1},
	}
	for _, c := range ids {
		for _, p := range paths {
			key := p.path + "|" + strconv.FormatInt(p.ts, 10)
			got := y2SignPath(p.path, c.id, p.ts)
			if got != want[c.name][key] {
				t.Errorf("signPath(%s, %s) = %q，期望 %q", c.name, key, got, want[c.name][key])
			}
		}
	}
}

// TestY2APIURL 带数组参数、空值过滤、特殊字符的转义
func TestY2APIURL(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		params y2Params
		want   string
	}{
		{"无参数", "/vlabpassportapi/v1/course/term-list", nil,
			"https://aita.yanhekt.cn/vlabpassportapi/v1/course/term-list"},
		{"空参数对象", "/courseapi/v3/multi-search/get-course-list", y2Params{},
			"https://aita.yanhekt.cn/courseapi/v3/multi-search/get-course-list"},
		{"数组参数 semesters[]", "/v2/course/list",
			y2Params{y2P("semesters", []any{1, 2}), y2P("page", 1)},
			"https://aita.yanhekt.cn/v2/course/list?semesters%5B%5D=1&semesters%5B%5D=2&page=1"},
		{"空数组", "/courseapi/v3/multi-search/get-course-list",
			y2Params{y2P("title", "数值分析"), y2P("struc_ids", []any{})},
			"https://aita.yanhekt.cn/courseapi/v3/multi-search/get-course-list?title=%E6%95%B0%E5%80%BC%E5%88%86%E6%9E%90"},
		{"空值过滤", "/courseapi/v3/multi-search/get-course-list",
			y2Params{
				y2P("term", ""), y2P("page", 1), y2P("per_page", 20), y2P("tenant", 21),
				y2P("title", "数值 分析"), y2P("order", ""), y2P("publish", ""), y2P("nil", nil),
			},
			"https://aita.yanhekt.cn/courseapi/v3/multi-search/get-course-list?page=1&per_page=20&tenant=21&title=%E6%95%B0%E5%80%BC+%E5%88%86%E6%9E%90"},
		{"数组里有空串", "/v2/course/list",
			y2Params{y2P("semesters", []any{1, "", nil, 2})},
			"https://aita.yanhekt.cn/v2/course/list?semesters%5B%5D=1&semesters%5B%5D=2"},
		{"中文与符号", "/x",
			y2Params{y2P("k", "a~!*()'b"), y2P("t", "2024-2025 第一学期")},
			"https://aita.yanhekt.cn/x?k=a%7E%21*%28%29%27b&t=2024-2025+%E7%AC%AC%E4%B8%80%E5%AD%A6%E6%9C%9F"},
	}
	for _, c := range cases {
		if got := y2APIURL(c.path, c.params); got != c.want {
			t.Errorf("apiUrl(%s) = %q\n  期望 %q", c.name, got, c.want)
		}
	}
}

// TestY2TermWindow 学期名 → 时间窗
func TestY2TermWindow(t *testing.T) {
	type win struct{ from, to string }
	cases := []struct {
		name string
		in   string
		want *win
	}{
		{"第一学期", "2024-2025 第一学期", &win{"2024-09-01", "2025-01-31"}},
		{"第二学期", "2024-2025学年第二学期", &win{"2025-02-15", "2025-07-15"}},
		{"短横线-1", "2024-2025-1", &win{"2024-09-01", "2025-01-31"}},
		{"短横线-2", "2024-2025-2", &win{"2025-02-15", "2025-07-15"}},
		{"第三学期", "2024-2025 第三学期", &win{"2024-06-15", "2024-09-15"}},
		{"学期不明", "2023-2024", &win{"2023-09-01", "2024-07-15"}},
		{"空串", "", nil},
		{"认不出", "abc", nil},
		{"破折号+秋季", "2024—2025秋季学期", &win{"2024-09-01", "2025-01-31"}},
		{"至+春季", "2024至2025 春季", &win{"2025-02-15", "2025-07-15"}},
		{"en dash 不认", "2024–2025 第一学期", nil},
		{"波浪线+夏季", "2024~2025学年夏季学期", &win{"2024-06-15", "2024-09-15"}},
		{"下一学年", "2025-2026学年第一学期", &win{"2025-09-01", "2026-01-31"}},
	}
	for _, c := range cases {
		got := y2TermWindow(c.in)
		if c.want == nil {
			if got != nil {
				t.Errorf("termWindow(%s) = %+v，期望 null", c.name, *got)
			}
			continue
		}
		if got == nil || got.from != c.want.from || got.to != c.want.to {
			t.Errorf("termWindow(%s) = %+v，期望 %+v", c.name, got, *c.want)
		}
	}
}

// TestY2DatesBetween 日期区间
func TestY2DatesBetween(t *testing.T) {
	cases := []struct {
		from, to string
		cap      int
		want     []string
	}{
		{"2024-09-01", "2024-09-05", 240, []string{"2024-09-01", "2024-09-02", "2024-09-03", "2024-09-04", "2024-09-05"}},
		{"2024-02-27", "2024-03-02", 240, []string{"2024-02-27", "2024-02-28", "2024-02-29", "2024-03-01", "2024-03-02"}},
		{"2024-09-05", "2024-09-01", 240, []string{}},
		{"2024-01-01", "2024-12-31", 10, []string{"2024-01-01", "2024-01-02", "2024-01-03", "2024-01-04", "2024-01-05",
			"2024-01-06", "2024-01-07", "2024-01-08", "2024-01-09", "2024-01-10"}},
		{"", "", 240, []string{}},
		{"2023-12-30", "2024-01-02", 240, []string{"2023-12-30", "2023-12-31", "2024-01-01", "2024-01-02"}},
	}
	for _, c := range cases {
		got := y2DatesBetween(c.from, c.to, c.cap)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("datesBetween(%s,%s,%d) = %v，期望 %v", c.from, c.to, c.cap, got, c.want)
		}
	}
}

// TestY2TsOf epoch 秒 / 毫秒 / 日期字符串
func TestY2TsOf(t *testing.T) {
	// 带时间的字符串两边都按**本地时区**解析（Node 的 Date.parse / Go 的 time.Local），
	// 下面三条期望值是在 Asia/Shanghai(+08:00) 上跑 Node 得到的，换时区就没意义了
	_, off := time.Now().Zone()
	localOK := off == 8*3600
	if !localOK {
		t.Logf("本机时区偏移 %ds 不是 +8 小时，跳过 3 条依赖本地时区的断言", off)
	}
	cases := []struct {
		in        any
		want      int64
		localOnly bool
	}{
		{float64(1700000000), 1700000000, false},
		{float64(1700000000000), 1700000000, false},
		{"1700000000", 1700000000, false},
		{"2024-09-01 14:30:00", 1725172200, true},
		{"2024-09-01", 1725120000, true},
		{"2024-09-01T14:30:00", 1725172200, true},
		{"", 0, false},
		{"abc", 0, false},
		{float64(-5), 0, false},
		{float64(0), 0, false},
		{nil, 0, false},
	}
	for _, c := range cases {
		if c.localOnly && !localOK {
			continue
		}
		if got := y2TsOf(c.in); got != c.want {
			t.Errorf("tsOf(%v) = %d，期望 %d", c.in, got, c.want)
		}
	}
}

// TestY2DateOf 时间戳 → 北京时间日期
func TestY2DateOf(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{float64(1700000000), "2023-11-15"},
		{"2024-09-01 23:30:00", "2024-09-01"},
		{"2024-09-01", "2024-09-01"},
		{float64(0), ""},
		{"abc", ""},
	}
	for _, c := range cases {
		if got := y2DateOf(c.in); got != c.want {
			t.Errorf("dateOf(%v) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestY2KindOfSub 直播类 / 录播类
func TestY2KindOfSub(t *testing.T) {
	for _, x := range []string{"course_live", "ILive", "olive", "live", "large_class", "live_video"} {
		if got := y2KindOfSub(x); got != "live" {
			t.Errorf("kindOfSub(%q) = %q，期望 live", x, got)
		}
	}
	for _, x := range []any{"record", "", nil, "video"} {
		if got := y2KindOfSub(x); got != "record" {
			t.Errorf("kindOfSub(%v) = %q，期望 record", x, got)
		}
	}
}

// TestY2StatusFlags 讲次状态判定
func TestY2StatusFlags(t *testing.T) {
	rec := map[string]bool{"6": true}
	maybe := map[string]bool{"5": true, "": true}
	live := map[string]bool{"1": true, "2": true, "3": true}
	for _, s := range []string{"0", "1", "2", "3", "5", "6", "", "9"} {
		if got := y2IsRecordingStatus(s); got != rec[s] {
			t.Errorf("isRecordingStatus(%q) = %v", s, got)
		}
		if got := y2MaybeRecording(s); got != maybe[s] {
			t.Errorf("maybeRecording(%q) = %v", s, got)
		}
		if got := y2IsLiveStatus(s); got != live[s] {
			t.Errorf("isLiveStatus(%q) = %v", s, got)
		}
	}
}

// TestY2FilterRecordings 过滤 + 按时间倒序（Node 的结果是 d,b,a）
func TestY2FilterRecordings(t *testing.T) {
	raw := []y2Session{
		{SubID: "a", StartAt: 100, Status: "6"},
		{SubID: "b", StartAt: 300, Status: "5"},
		{SubID: "c", StartAt: 200, Status: "1"},
		{SubID: "d", StartAt: 400, Status: ""},
		{SubID: "e", StartAt: 50, Status: "3"},
		{SubID: "f", StartAt: 500, Status: "2"},
		{SubID: "g", StartAt: 600, Status: "9"},
	}
	got := []string{}
	for _, s := range y2FilterRecordings(raw) {
		got = append(got, s.SubID)
	}
	if strings.Join(got, ",") != "d,b,a" {
		t.Errorf("filterRecordings = %v，期望 [d b a]", got)
	}
}

// TestY2SignedURL /play/ 地址补 t（把时间钉死，和 Node 逐字符比）
func TestY2SignedURL(t *testing.T) {
	id := &y2Identity{userId: 12345, tenantId: 21, phone: "13800138000"}
	cases := []struct{ in, want string }{
		{"https://aita.yanhekt.cn/play/abc.mp4",
			"https://aita.yanhekt.cn/play/abc.mp4?t=12345-1700000000-d622cbf80e0ad907acc19084f5466afa"},
		{"https://aita.yanhekt.cn/play/abc.mp4?t=old&x=1",
			"https://aita.yanhekt.cn/play/abc.mp4?x=1&t=12345-1700000000-d622cbf80e0ad907acc19084f5466afa"},
		{"https://aita.yanhekt.cn/play/a%2Fb.mp4?name=%E4%B8%AD%E6%96%87&t=old",
			"https://aita.yanhekt.cn/play/a%2Fb.mp4?name=%E4%B8%AD%E6%96%87&t=12345-1700000000-f414031943526a09b85f94ea501eb9b0"},
		{"https://aita.yanhekt.cn/live/a.m3u8?sign=abc%2Bdef%3D&width=1280",
			"https://aita.yanhekt.cn/live/a.m3u8?sign=abc%2Bdef%3D&width=1280&t=12345-1700000000-7bfc28288723bbe71765c22ae23f9008"},
		{"https://aita.yanhekt.cn/play/x.mp4?z=1&a=2&t=zzz&m=3",
			"https://aita.yanhekt.cn/play/x.mp4?z=1&a=2&m=3&t=12345-1700000000-295db79f216101a11c4d92ec22d32e49"},
	}
	for _, c := range cases {
		got, err := y2SignedURLAt(c.in, id, 1700000000)
		if err != nil {
			t.Errorf("signedUrl(%s) 出错：%v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("signedUrl(%s)\n  = %q\n  期望 %q", c.in, got, c.want)
		}
	}
}

// TestY2JwtExp 解析 JWT 里的 exp（这份 payload 的 base64 里带 '+'，正好压一下解码）
func TestY2JwtExp(t *testing.T) {
	const jwt = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
		"eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IueOi-S6lCIsInRlbmFudCI6MjEsImV4cCI6MTc2MDAwMDAwMH0." +
		"LXEWQrcmsEQBYnyp-6wy9chTD7GQPMTbAiWHF5IaSIE"
	if got := y2JwtExp(jwt); got != 1760000000 {
		t.Errorf("jwtExp = %d，期望 1760000000", got)
	}
	if got := y2JwtExp("not-a-jwt"); got != 0 {
		t.Errorf("jwtExp(坏 token) = %d，期望 0", got)
	}
	if got := y2JwtExp(""); got != 0 {
		t.Errorf("jwtExp(空) = %d，期望 0", got)
	}
}

// TestY2FormEncode URLSearchParams 那套转义（空格是 +，只有 [A-Za-z0-9*-._] 原样）
func TestY2FormEncode(t *testing.T) {
	cases := map[string]string{
		"":                  "",
		"abc":               "abc",
		"a b":               "a+b",
		"数值分析":              "%E6%95%B0%E5%80%BC%E5%88%86%E6%9E%90",
		"semesters[]":       "semesters%5B%5D",
		"~!*'()":            "%7E%21*%27%28%29",
		"2024-2025":         "2024-2025",
		"a=b&c":             "a%3Db%26c",
		"/play/x.mp4?t=1":   "%2Fplay%2Fx.mp4%3Ft%3D1",
		"¥":                 "%C2%A5",
		"中文 english 混排 + 号": "%E4%B8%AD%E6%96%87+english+%E6%B7%B7%E6%8E%92+%2B+%E5%8F%B7",
	}
	for in, want := range cases {
		if got := y2FormEncode(in); got != want {
			t.Errorf("formEncode(%q) = %q，期望 %q", in, got, want)
		}
	}
}
