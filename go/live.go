// 直播：列表（/api/live）与流代理（/lp）
//
// 对应 server.js 的 liveRow / liveListOld / proxyLive。
// 实测 /v1/live/hot/list 与 /v2/live/list 匿名就能读，直播流是公开的 m3u8，
// 所以这里不排队、不缓存，用独立的 4 并发连接池。
package main

import (
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type liveItem struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Teacher   string `json:"teacher"`
	Course    string `json:"course"`
	Room      string `json:"room"`
	StartedAt string `json:"startedAt"`
	EndedAt   string `json:"endedAt"`
	Phase     string `json:"phase"`
	Main      string `json:"main"`
	VGA       string `json:"vga"`
	RoomCam   string `json:"roomCam"`
	Views     int    `json:"views"`
}

const liveTTL = 30 * time.Second

var (
	liveMu    sync.Mutex
	liveAt    time.Time
	liveItems []liveItem
	liveValid bool
)

// 上游这次合并出来多少条（过滤前）—— 只给 /api/live 的排查字段用，
// 一眼分清"接口没给数据"还是"被我们的时间窗口过滤掉了"
var liveRawCount int

// 上游两个接口一共返回了多少个直播对象（连没有取流地址的都算）
var liveSrcCount int

var reBackslashSlash = strings.NewReplacer(`\/`, `/`)

// unescapeUrl JSON 里的地址有时带 \/ 转义
func unescapeUrl(s string) string { return reBackslashSlash.Replace(s) }

// parseLocalTime 解析平台给的北京时间串 2026-10-22 18:30:00
// （固定 +08:00；不能用 time.Local —— 手机上找不到 tzdata 会退化成 UTC，整体偏 8 小时）
func parseLocalTime(s string) int64 {
	t := strings.ReplaceAll(s, "-", "/")
	for _, layout := range []string{"2006/01/02 15:04:05", "2006/01/02 15:04", "2006/01/02"} {
		if v, err := time.ParseInLocation(layout, t, bj); err == nil {
			return v.UnixNano() / int64(time.Millisecond)
		}
	}
	return 0
}

// liveRow 上游直播对象 → 统一条目
func liveRow(l map[string]any) liveItem {
	c := jobj(l["course"])
	if c == nil {
		c = map[string]any{}
	}
	profs := []string{}
	for _, p := range jarr(c["professors"]) {
		if o := jobj(p); o != nil {
			if name := jstr(o["name"]); name != "" {
				profs = append(profs, name)
			}
		}
	}
	if len(profs) > 3 {
		profs = profs[:3]
	}
	sess := jobj(l["session"])
	teacher := strings.Join(profs, "、")
	if teacher == "" && sess != nil {
		if prof := jobj(sess["professor"]); prof != nil {
			teacher = jstr(prof["name"])
		}
	}

	startedAt := orStr(jstr(l["schedule_started_at"]), orStr(jstr(l["started_at"]), jstr(sess["started_at"])))
	endedAt := orStr(jstr(l["schedule_ended_at"]), orStr(jstr(l["ended_at"]), jstr(sess["ended_at"])))

	// 真实开播时间（不是课表时间）—— 有的课提前开播，只看课表会误判"未开始"
	aStart := parseLocalTime(orStr(jstr(l["started_at"]), jstr(sess["started_at"])))
	hasStream := unescapeUrl(jstr(l["target"])) != "" || unescapeUrl(jstr(l["target_vga"])) != ""
	s := parseLocalTime(startedAt)
	e := parseLocalTime(endedAt)
	now := nowMillis()
	phase := "live"
	switch {
	case e != 0 && now > e:
		phase = "ended"
	case aStart != 0 && now >= aStart:
		phase = "live" // 已经真的开播了
	case s != 0 && now < s:
		// 有流、且离上课不到 15 分钟，就按"可进"算，别显示成未开始
		if hasStream && now >= s-int64((15*time.Minute)/time.Millisecond) {
			phase = "live"
		} else {
			phase = "upcoming"
		}
	}

	id := orStr(jstr(l["id"]), jstr(sess["id"]))
	views := 0
	if v := l["participant_count"]; truthy(v) {
		views = jint(v)
	} else if v := l["views"]; truthy(v) {
		views = jint(v)
	}
	courseName := jstr(c["name_zh"])
	room := orStr(jstr(l["subtitle"]), jstr(sess["section_group_title"]))

	return liveItem{
		ID:        id,
		Title:     orStr(jstr(l["title"]), courseName),
		Teacher:   teacher,
		Course:    courseName,
		Room:      room,
		StartedAt: startedAt,
		EndedAt:   endedAt,
		Phase:     phase,
		Main:      unescapeUrl(jstr(l["target"])),
		VGA:       unescapeUrl(jstr(l["target_vga"])),
		RoomCam:   unescapeUrl(jstr(l["target_room"])),
		Views:     views,
	}
}

// liveInvalidate 丢掉直播列表缓存（页面点「直播」时强制刷新用）
func liveInvalidate() {
	liveMu.Lock()
	liveValid = false
	liveMu.Unlock()
}

// liveListOld 合并 hot/list 与 v2/live/list，只留"在播 / 即将 / 刚结束半小时内"
func liveListOld() ([]liveItem, int64, string) {
	liveMu.Lock()
	if liveValid && time.Since(liveAt) < liveTTL {
		items, at := liveItems, liveAt.UnixNano()/int64(time.Millisecond)
		liveMu.Unlock()
		return items, at, ""
	}
	liveMu.Unlock()

	var hot, all map[string]any
	var errHot, errAll error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); hot, errHot = apiCall("/v1/live/hot/list?page=1&page_size=60", "") }()
	go func() { defer wg.Done(); all, errAll = apiCall("/v2/live/list?page=1&page_size=200", "") }()
	wg.Wait()
	lerr := ""
	if errHot != nil && errAll != nil {
		lerr = "hot: " + errHot.Error() + " / list: " + errAll.Error()
	} else if errHot != nil {
		lerr = "hot: " + errHot.Error()
	} else if errAll != nil {
		lerr = "list: " + errAll.Error()
	}

	merged := map[string]liveItem{}
	order := []string{} // 记住第一次出现的顺序，Node 的 Map 也是插入序
	liveSrcCount = 0
	put := func(l map[string]any) {
		if l == nil {
			return
		}
		liveSrcCount++
		row := liveRow(l)
		if row.ID == "" || row.Main == "" {
			return
		}
		if prev, ok := merged[row.ID]; ok {
			row.Main = orStr(row.Main, prev.Main)
			row.VGA = orStr(row.VGA, prev.VGA)
			row.RoomCam = orStr(row.RoomCam, prev.RoomCam)
		} else {
			order = append(order, row.ID)
		}
		merged[row.ID] = row
	}

	if hot != nil {
		for _, it := range jarr(hot["data"]) {
			if liveObj := jobj(it); liveObj != nil {
				if inner := liveObj["live"]; inner != nil {
					put(jobj(inner))
					continue
				}
			}
			put(jobj(it))
		}
	}
	if all != nil {
		var allRows []any
		if d := jobj(all["data"]); d != nil {
			allRows = jarr(d["data"])
		}
		if allRows == nil {
			allRows = jarr(all["data"])
		}
		for _, it := range allRows {
			put(jobj(it))
		}
	}

	now := nowMillis()
	liveRawCount = len(merged)
	items := make([]liveItem, 0, len(merged))
	for _, id := range order {
		x := merged[id]
		e := parseLocalTime(x.EndedAt)
		s := parseLocalTime(x.StartedAt)
		// 只要"现在在播 / 今天马上开播 / 刚结束半小时内"，别把历史全端出来
		if e != 0 && now-e > 30*60*1000 {
			continue
		}
		if s != 0 && s-now > 6*3600*1000 {
			continue
		}
		items = append(items, x)
	}
	// 兜底：接口明明给了数据，却被时间窗口全滤掉了（设备时钟不准之类），
	// 那就原样摆出来 —— 总比一片空白强，至少用户能看到场次去点。
	if len(items) == 0 {
		for _, id := range order {
			items = append(items, merged[id])
		}
	}

	rank := func(x liveItem) int {
		switch x.Phase {
		case "live":
			return 0
		case "upcoming":
			return 1
		}
		return 2
	}
	stableSortLive(items, func(a, b liveItem) bool {
		if rank(a) != rank(b) {
			return rank(a) < rank(b)
		}
		return parseLocalTime(a.StartedAt) < parseLocalTime(b.StartedAt)
	})

	at := nowMillis()
	liveMu.Lock()
	liveItems, liveAt, liveValid = items, time.Now(), true
	liveMu.Unlock()
	return items, at, lerr
}

func stableSortLive(items []liveItem, less func(a, b liveItem) bool) {
	// 简单插入排序就够（一百多条），顺便保证稳定
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && less(items[j], items[j-1]); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

// ---------------------------------------------------------------- 直播流代理

// 直播用独立的连接池（4 并发），不和录播那条单连接混用
var liveClient = &http.Client{
	Transport: &http.Transport{
		TLSClientConfig:       insecureTLS,
		ForceAttemptHTTP2:     false,
		MaxConnsPerHost:       4,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		DisableCompression:    true,
	},
}

// proxyLive /lp：m3u8 里的分片改成本地地址，分片直接转发；不缓存（直播要实时）
func proxyLive(w http.ResponseWriter, r *http.Request, rawURL string) {
	req, err := http.NewRequestWithContext(r.Context(), "GET", rawURL, nil)
	if err != nil {
		w.Header().Set("content-type", "text/plain; charset=utf-8")
		w.WriteHeader(502)
		_, _ = w.Write([]byte("直播流取不到：" + err.Error()))
		return
	}
	setVideoHeaders(req)
	up, err := liveClient.Do(req)
	if err != nil {
		if r.Context().Err() != nil {
			return // 客户端自己走了，不算错
		}
		w.Header().Set("content-type", "text/plain; charset=utf-8")
		w.WriteHeader(502)
		_, _ = w.Write([]byte("直播流取不到：" + err.Error()))
		return
	}
	defer up.Body.Close()

	ct := up.Header.Get("content-type")
	isPlaylist := strings.Contains(strings.ToLower(ct), "mpegurl") ||
		strings.Contains(strings.ToLower(ct), "m3u8") ||
		strings.HasSuffix(strings.Split(rawURL, "?")[0], ".m3u8")

	if isPlaylist {
		body, err := io.ReadAll(up.Body)
		if err != nil {
			return
		}
		text := string(body)
		lines := strings.Split(text, "\n")
		var out strings.Builder
		out.Grow(len(text) + 256)
		for i, line := range lines {
			t := strings.TrimSpace(line)
			if t == "" || strings.HasPrefix(t, "#") {
				out.WriteString(line)
			} else {
				out.WriteString("/lp?u=" + enc(resolveRef(t, rawURL)))
			}
			if i < len(lines)-1 {
				out.WriteString("\n")
			}
		}
		w.Header().Set("content-type", "application/vnd.apple.mpegurl")
		w.Header().Set("cache-control", "no-store")
		w.Header().Set("access-control-allow-origin", "*")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(out.String()))
		return
	}

	if ct == "" {
		ct = "video/mp2t"
	}
	w.Header().Set("content-type", ct)
	w.Header().Set("cache-control", "no-store")
	w.Header().Set("access-control-allow-origin", "*")
	if cl := up.Header.Get("content-length"); cl != "" {
		w.Header().Set("content-length", cl)
	}
	code := up.StatusCode
	if code == 0 {
		code = 200
	}
	w.WriteHeader(code)
	_, _ = io.Copy(w, up.Body)
}

// resolveRef 把播放列表里的相对地址按原始 URL 解析成绝对地址
// （等价 Node 的 new URL(ref, base).href：处理 ../、./、//host/x）
func resolveRef(ref, base string) string {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}
