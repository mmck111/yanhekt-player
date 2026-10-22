// 新版平台的服务端编排层
//
// 对应 server.js 的 y2Who / y2Videos / y2Remember / y2Verify / windowsFor /
// y2ScanWindows / probeStream / pickPlayable / pool / y2Match / y2Rescue /
// y2MatchByTeacher，以及各类缓存的 TTL 和容量策略。
package main

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// y2Course 一条课程（用 map 而不是 struct：Node 里是普通对象，
// 不同来源的字段集合不一样，y2Verify 还会往上贴 videoCount / verified / source）
type y2Course = map[string]any

// cloneRow 浅拷贝一行（对应 Node 的 {...c} 展开）
func cloneRow(c y2Course) y2Course {
	out := make(y2Course, len(c)+4)
	for k, v := range c {
		out[k] = v
	}
	return out
}

// y2JwtTail 缓存键用的 JWT 尾巴（Node 的 jwt.slice(-12)）
func y2JwtTail(jwt string) string {
	if len(jwt) > y2Tail {
		return jwt[len(jwt)-y2Tail:]
	}
	return jwt
}

// ---------------------------------------------------------------- 身份

// JWT -> 身份（含签名用的 phone），只放内存
var (
	y2IdMu   sync.Mutex
	y2Idents = map[string]*y2Identity{}
	y2Probed = map[string]bool{}
)

func y2RememberIdentity(jwt string, id *y2Identity) {
	y2IdMu.Lock()
	if len(y2Idents) > 50 {
		y2Idents = map[string]*y2Identity{}
	}
	y2Idents[jwt] = id
	y2IdMu.Unlock()
}

// y2Who JWT → 身份；拿不到就 nil
func y2Who(jwt string) *y2Identity {
	if jwt == "" {
		return nil
	}
	y2IdMu.Lock()
	hit, ok := y2Idents[jwt]
	y2IdMu.Unlock()
	if ok {
		return hit
	}
	id, err := y2Profile(jwt)
	if err == nil {
		y2RememberIdentity(jwt, id)
		return id
	}
	// 可能是网关的鉴权写法变了（比如只认 cookie）：探测一次，成功就把这种方式记住
	y2IdMu.Lock()
	already := y2Probed[jwt]
	if !already {
		y2Probed[jwt] = true
	}
	y2IdMu.Unlock()
	if already {
		return nil
	}
	got, _, err2 := y2Login(jwt, "")
	if err2 != nil {
		return nil
	}
	y2RememberIdentity(jwt, got)
	return got
}

// ---------------------------------------------------------------- 缓存

const y2VideoTTL = 5 * time.Minute

// 讲次缓存：jwt+courseId -> data；data.sessions 只含「有录像」的讲次
var (
	y2VideoMu    sync.Mutex
	y2VideoCache = map[string]y2VideoEntry{}
)

type y2VideoEntry struct {
	at   time.Time
	data *y2VideosData
}

// 我的课表缓存：jwt -> Map(courseId -> 讲次[])
var (
	y2SchedMu    sync.Mutex
	y2SchedCache = map[string]y2SchedEntry{}
)

type y2SchedEntry struct {
	at  time.Time
	mp  map[string][]y2ScheduleRow
	err error
}

// 关键词扫描结果缓存：jwt+关键词+学期窗口 -> Map(courseId -> 讲次[])
var (
	y2ScanMu    sync.Mutex
	y2ScanCache = map[string]y2ScanEntry{}
)

type y2ScanEntry struct {
	at time.Time
	mp map[string][]y2Session
}

// 单讲"到底有没有流"的探测缓存
var (
	y2ProbeMu    sync.Mutex
	y2ProbeCache = map[string]y2ProbeResult{}
)

// y2VideosData 一门课的录像判定结果
type y2VideosData struct {
	source        string
	verified      bool
	note          string
	sessions      []y2Session
	room          string
	videoCount    int
	totalSessions int
	termName      string
}

func y2Remember(key string, data *y2VideosData) *y2VideosData {
	y2VideoMu.Lock()
	if len(y2VideoCache) > 200 {
		y2VideoCache = map[string]y2VideoEntry{}
	}
	y2VideoCache[key] = y2VideoEntry{at: time.Now(), data: data}
	y2VideoMu.Unlock()
	return data
}

// y2Schedule 自己课表里近 150 天的讲次（讲次直读失败时的救命通道，一次覆盖所有课）
func y2Schedule(jwt string, id *y2Identity) (map[string][]y2ScheduleRow, error) {
	key := y2JwtTail(jwt)
	y2SchedMu.Lock()
	hit, ok := y2SchedCache[key]
	y2SchedMu.Unlock()
	if ok && time.Since(hit.at) < y2VideoTTL {
		return hit.mp, hit.err
	}
	mp, err := y2ScheduleMap(jwt, id.userId, 150)
	if err != nil {
		return nil, err
	}
	y2SchedMu.Lock()
	if len(y2SchedCache) > 50 {
		y2SchedCache = map[string]y2SchedEntry{}
	}
	y2SchedCache[key] = y2SchedEntry{at: time.Now(), mp: mp}
	y2SchedMu.Unlock()
	return mp, nil
}

// verifiedFromSchedule 课表行里能确定有录像的（只认 status=6，别的宁可待确认）
func verifiedFromSchedule(list []y2ScheduleRow) []y2Session {
	out := []y2Session{}
	for _, s := range list {
		if s.Status == "6" {
			out = append(out, y2ScheduleToSession(s))
		}
	}
	return out
}

func y2ScheduleToSession(s y2ScheduleRow) y2Session {
	return y2Session{
		SubID: s.SubID, Title: s.Title, StartAt: s.StartAt, Duration: s.Duration,
		Status: s.Status, Room: s.Room, Teacher: s.Teacher, Date: s.Date,
		TermID: s.TermID, TermName: s.TermName,
	}
}

// ---------------------------------------------------------------- 学期窗口

// y2WindowFull 一个要扫的学期窗口（带学期名，用于诊断输出）
type y2WindowFull struct {
	key   string
	from  string
	to    string
	terms []string
}

// y2WindowsFor 从一批课程里提炼出要扫的学期窗口（去重，最多 max 个）
func y2WindowsFor(courses []y2Course, termNames map[string]string, max int) []y2WindowFull {
	out := []y2WindowFull{}
	index := map[string]int{}
	for _, c := range courses {
		// 课程条目上没写学期名时，用学期表按 termId 反查
		name := jstr(c["termName"])
		if name == "" {
			name = termNames[jstr(c["termId"])]
		}
		w := y2TermWindow(name)
		if w == nil {
			continue
		}
		key := w.from + "~" + w.to
		i, ok := index[key]
		if !ok {
			out = append(out, y2WindowFull{key: key, from: w.from, to: w.to})
			i = len(out) - 1
			index[key] = i
		}
		dup := false
		for _, t := range out[i].terms {
			if t == name {
				dup = true
				break
			}
		}
		if !dup {
			out[i].terms = append(out[i].terms, name)
		}
	}
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// y2ScanWindows 按关键词扫这些学期窗口，合并成 Map(courseId -> 讲次[])。
// 一次搜索只扫一遍，结果是"关键词 → 这些学期里所有课程的讲次"。
func y2ScanWindows(jwt, q string, windows []y2WindowFull) (map[string][]y2Session, error) {
	if len(windows) == 0 {
		return map[string][]y2Session{}, nil
	}
	keys := make([]string, 0, len(windows))
	for _, w := range windows {
		keys = append(keys, w.key)
	}
	cacheKey := y2JwtTail(jwt) + "|" + q + "|" + strings.Join(keys, ",")
	y2ScanMu.Lock()
	hit, ok := y2ScanCache[cacheKey]
	y2ScanMu.Unlock()
	if ok && time.Since(hit.at) < y2VideoTTL {
		return hit.mp, nil
	}

	out := map[string][]y2Session{}
	for _, w := range windows {
		dates := y2DatesBetween(w.from, w.to, 170)
		if len(dates) == 0 {
			continue
		}
		part, err := y2ScanDates(jwt, q, dates, 10, 30)
		if err != nil {
			return nil, err
		}
		for k, rows := range part {
			cur := out[k]
			for _, r := range rows {
				dup := false
				for _, x := range cur {
					if x.SubID == r.SubID {
						dup = true
						break
					}
				}
				if !dup {
					cur = append(cur, r)
				}
			}
			sort.SliceStable(cur, func(i, j int) bool { return cur[i].StartAt > cur[j].StartAt })
			out[k] = cur
		}
	}
	y2ScanMu.Lock()
	if len(y2ScanCache) > 30 {
		y2ScanCache = map[string]y2ScanEntry{}
	}
	y2ScanCache[cacheKey] = y2ScanEntry{at: time.Now(), mp: out}
	y2ScanMu.Unlock()
	return out, nil
}

// y2ProbeResult 单讲取流的探测结果：ok=有流，err=这次请求失败（不是"没流"）
type y2ProbeResult struct {
	ok  bool
	err bool
}

// y2SessionHasStream 这一讲真的能取到视频流吗？
//
// 只认 get-sub-info 返回的 video_list —— 平台给的 sub_status 靠不住：
// 一门课 5 讲全是 6、实际只有 1 讲有流的情况很常见，其余点进去是黑屏。
// 返回 (ok, failed)：failed=true 表示请求失败（网络/权限），不是"没有流"。
func y2SessionHasStream(jwt, courseID, subID string) (ok, failed bool) {
	if subID == "" {
		return false, false
	}
	key := y2JwtTail(jwt) + ":" + subID
	y2ProbeMu.Lock()
	v, hit := y2ProbeCache[key]
	y2ProbeMu.Unlock()
	if hit {
		return v.ok, v.err
	}
	if info, err := y2SubInfoOf(jwt, courseID, subID); err == nil && info != nil {
		ok = len(info.Streams) > 0
	} else {
		failed = true
	}
	if !failed {
		y2ProbeMu.Lock()
		if len(y2ProbeCache) > 5000 {
			y2ProbeCache = map[string]y2ProbeResult{}
		}
		y2ProbeCache[key] = y2ProbeResult{ok: ok}
		y2ProbeMu.Unlock()
	}
	return ok, failed
}

// y2VerifySessions 逐讲验流：只有真取到视频流的讲次才算"有录像"。
// limit>0 时只验前 limit 讲；返回能播的讲次（时间倒序）、请求失败数、一共验了几讲。
func y2VerifySessions(jwt, courseID string, sessions []y2Session, limit, conc int) (playable []y2Session, failed, probed int) {
	list := sessions
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	probed = len(list)
	playable = []y2Session{}
	if probed == 0 {
		return playable, 0, 0
	}
	if conc <= 0 {
		conc = 6
	}
	var mu sync.Mutex
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for _, s := range list {
		wg.Add(1)
		go func(s y2Session) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ok, e := y2SessionHasStream(jwt, courseID, s.SubID)
			mu.Lock()
			if e {
				failed++
			} else if ok {
				playable = append(playable, s)
			}
			mu.Unlock()
		}(s)
	}
	wg.Wait()
	sort.SliceStable(playable, func(i, j int) bool { return playable[i].StartAt > playable[j].StartAt })
	return playable, failed, probed
}

// probeStream 单讲"到底有没有流"
func probeStream(jwt, courseID, subID string) bool {
	ok, _ := y2SessionHasStream(jwt, courseID, subID)
	return ok
}

// pickPlayable 从课表行里挑出真正能播的讲次。
//
// sub_status=6 **不能**直接当"有录像"用：平台上一门课 5 讲全是 6、实际只有 1 讲
// 有流的情况很常见，所以这里逐讲真取一次视频流，取到才算。全部请求都失败
// （网络/权限）时退回状态判断，别把故障当成"没录像"。
func pickPlayable(jwt, courseID string, rows []y2Session) []y2Session {
	if len(rows) == 0 {
		return []y2Session{}
	}
	six := []y2Session{}
	for _, r := range rows {
		if y2IsRecordingStatus(r.Status) || y2MaybeRecording(r.Status) {
			six = append(six, r)
		}
	}
	cand := six
	if len(cand) == 0 {
		cand = rows
	}
	playable, failed, probed := y2VerifySessions(jwt, courseID, cand, 0, 6)
	if len(playable) > 0 || failed < probed {
		return playable
	}
	return six
}

// ---------------------------------------------------------------- 一门课的录像

// y2Videos 一门课的可看录像（带缓存）。三级来源**依次真试**，任何一级拿到就返回：
//  1. get-course-detail（最快，但很多账号拿不到）
//  2. 自己的课表 get-week-schedules（一次覆盖所有课，自带教室）
//  3. allowScan=true 时，按天扫课表
//
// 关键：某一级"成功但 0 条"时**不能就此结束**，要继续试下一级。
func y2Videos(jwt string, id *y2Identity, courseID string, allowScan bool, q, termName string) (*y2VideosData, error) {
	key := y2JwtTail(jwt) + ":" + courseID
	y2VideoMu.Lock()
	hit, ok := y2VideoCache[key]
	y2VideoMu.Unlock()
	if ok && time.Since(hit.at) < y2VideoTTL {
		return hit.data, nil
	}

	notes := []string{}
	var detail *y2CourseVideosResult

	// 1. 讲次直读
	d, err := y2CourseVideos(jwt, courseID, id.account)
	if err == nil {
		detail = d
		if d.videoCount > 0 {
			merged := y2MergeKnownSessions(jwt, id, courseID, d.sessions, q, termName)
			sessions := d.sessions
			if len(merged) > 0 {
				sessions = merged
			}
			src := "detail"
			if len(sessions) > len(d.sessions) {
				src = "detail+schedule"
			}
			room := firstRoom(sessions)
			if room == "" {
				room = d.room
			}
			return y2Remember(key, &y2VideosData{
				source: src, verified: d.verified, note: "",
				sessions: sessions, room: room,
				videoCount: len(sessions), totalSessions: maxInt(d.totalSessions, len(sessions)), termName: d.termName,
			}), nil
		}
		notes = append(notes, "讲次直读没给到可播放讲次")
	} else {
		if y2IsAuthRejected(err.Error()) {
			return nil, err
		}
		notes = append(notes, "讲次直读："+err.Error())
	}

	// 2. 自己的课表
	if mp, serr := y2Schedule(jwt, id); serr == nil {
		list := mp[courseID]
		rows := make([]y2Session, 0, len(list))
		for _, s := range list {
			rows = append(rows, y2ScheduleToSession(s))
		}
		playable := pickPlayable(jwt, courseID, rows)
		if len(playable) > 0 {
			return y2Remember(key, &y2VideosData{
				source: "schedule-table", verified: true, note: strings.Join(notes, "；"),
				sessions: playable, room: playable[0].Room,
				videoCount: len(playable), totalSessions: len(list), termName: "",
			}), nil
		}
		if len(list) > 0 {
			notes = append(notes, "自己的课表里有课，但没有一讲是\"可播放\"状态")
		}
	}

	// 3. 按天扫课表（只在用户真的点开这门课时）
	if allowScan {
		// 3a. 搜索时扫过的学期窗口 / 这个学期自己扫一遍
		if q != "" {
			if w := y2TermWindow(termName); w != nil {
				windows := []y2WindowFull{{key: w.from + "~" + w.to, from: w.from, to: w.to}}
				if mp, err := y2ScanWindows(jwt, q, windows); err == nil {
					if rows := mp[courseID]; len(rows) > 0 {
						list := pickPlayable(jwt, courseID, rows)
						return y2Remember(key, &y2VideosData{
							source: "schedule-keyword", verified: len(list) > 0, note: strings.Join(notes, "；"),
							sessions: list, room: firstRoom(list),
							videoCount: len(list), totalSessions: len(rows), termName: termName,
						}), nil
					}
				}
			} else if mp, err := y2ScanByKeyword(jwt, q, 100, 8, 21); err == nil {
				if rows := mp[courseID]; len(rows) > 0 {
					list := pickPlayable(jwt, courseID, rows)
					return y2Remember(key, &y2VideosData{
						source: "schedule-keyword", verified: len(list) > 0, note: strings.Join(notes, "；"),
						sessions: list, room: firstRoom(list),
						videoCount: len(list), totalSessions: len(rows), termName: termName,
					}), nil
				}
			}
		}
		// 3b. 单独扫这门课
		raw, err := y2ScanSessions(jwt, courseID, "", 60, 30)
		if err == nil {
			list := pickPlayable(jwt, courseID, y2FilterRecordings(raw))
			if len(list) > 0 {
				return y2Remember(key, &y2VideosData{
					source: "schedule", verified: true, note: strings.Join(notes, "；"),
					sessions: list, room: firstRoom(list),
					videoCount: len(list), totalSessions: len(raw), termName: "",
				}), nil
			}
			notes = append(notes, "按天扫课表也没扫到讲次")
		} else {
			notes = append(notes, "按天扫课表："+err.Error())
		}
	}

	total := 0
	if detail != nil {
		total = detail.totalSessions
	}
	return y2Remember(key, &y2VideosData{
		source: "none", verified: false, note: strings.Join(notes, "；"),
		sessions: []y2Session{}, room: "",
		videoCount: 0, totalSessions: total, termName: "",
	}), nil
}

// y2ScanCached 只从"关键词扫描"缓存里取结果；没扫过就返回 nil（不现场扫）
func y2ScanCached(jwt, q, termName string) map[string][]y2Session {
	if q == "" {
		return nil
	}
	tail := y2JwtTail(jwt) + "|" + q + "|"
	want := ""
	if w := y2TermWindow(termName); w != nil {
		want = tail + w.from + "~" + w.to
	}
	y2ScanMu.Lock()
	defer y2ScanMu.Unlock()
	for k, v := range y2ScanCache {
		if len(k) < len(tail) || k[:len(tail)] != tail {
			continue
		}
		if time.Since(v.at) >= y2VideoTTL {
			continue
		}
		if want != "" && !strings.Contains(k, want) {
			continue
		}
		return v.mp
	}
	return nil
}

// y2MergeKnownSessions 把"讲次直读"的结果和课表里已知的讲次合并，再逐讲验流。
//
// 讲次直读经常只返回一部分 —— 实测一门 5 讲的课只有 1 讲是 sub_status=6，
// 其余 4 讲是 5 或空，于是只剩 1 讲；课表里 5 讲都在，合并后两边一致。
func y2MergeKnownSessions(jwt string, id *y2Identity, courseID string, sessions []y2Session, q, termName string) []y2Session {
	base := sessions
	if base == nil {
		base = []y2Session{}
	}
	seen := map[string]bool{}
	for _, s := range base {
		seen[s.SubID] = true
	}
	extra := []y2Session{}
	add := func(rows []y2Session) {
		for _, r := range rows {
			if r.SubID == "" || seen[r.SubID] {
				continue
			}
			seen[r.SubID] = true
			extra = append(extra, r)
		}
	}
	addRows := func(rows []y2ScheduleRow) {
		for _, s := range rows {
			add([]y2Session{y2ScheduleToSession(s)})
		}
	}
	if mp, err := y2Schedule(jwt, id); err == nil {
		addRows(mp[courseID])
	}
	// 搜索时已经扫好的课表缓存，有就用
	if len(extra) == 0 && q != "" {
		if mp := y2ScanCached(jwt, q, termName); mp != nil {
			add(mp[courseID])
		}
	}
	// 直读只给了一两讲（典型：5 讲里只有 1 讲是可播放状态）→ 单独把这门课的课表扫一遍兜底
	if len(extra) == 0 && len(base) < 2 {
		if raw, err := y2ScanSessions(jwt, courseID, "", 90, 20); err == nil {
			add(y2FilterRecordings(raw))
		}
	}
	if len(extra) == 0 {
		return base
	}
	playable, _, _ := y2VerifySessions(jwt, courseID, extra, 0, 6)
	if len(playable) == 0 {
		return base
	}
	out := append(append([]y2Session{}, base...), playable...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartAt > out[j].StartAt })
	return out
}

func firstRoom(list []y2Session) string {
	if len(list) > 0 {
		return list[0].Room
	}
	return ""
}

// ---------------------------------------------------------------- 搜索结果的录像校验

// y2Verify 搜索结果的"有没有录像"校验 + 带教室名。
//
// 证据优先级（哪条先给出结论就用哪条）：
//  1. **按关键词扫一遍课表**（一次覆盖列表里所有课程，sub_status、room_name 都在里面）
//     - 有 status=6 的讲次 → 有录像，教室直接有了
//     - 有讲次但一条都不是可播状态 → 抽最新一条**真的要一次视频流**：要不到就判定没录像
//  2. 课表里扫不到的课（往届课程等）→ 走 detail / 自己的课表；都不行就标「待确认」
//
// 总保险：如果一轮下来全部被藏掉，说明判定出了系统性偏差，这时一门都不藏。
// y2LongestToken 多词查询里最长的一段（通常是课程名），用来扫课表
func y2LongestToken(s string) string {
	best := ""
	for _, t := range strings.Fields(s) {
		if len([]rune(t)) > len([]rune(best)) {
			best = t
		}
	}
	return best
}

func y2Verify(jwt string, id *y2Identity, courses []y2Course, q string) (kept []y2Course, hidden, unverified int, fallback bool, err error) {
	// 1. 按结果里出现的学期，各自扫一遍课表（一次搜索最多 2 个学期）
	termNames := map[string]string{}
	if terms, e := y2TermList(jwt); e == nil {
		for _, t := range terms {
			termNames[t.ID] = t.Name
		}
	}
	windows := y2WindowsFor(courses, termNames, 2)
	scanQ := y2LongestToken(q)
	if scanQ == "" {
		scanQ = q
	}
	var scan map[string][]y2Session
	if scanQ != "" && len(windows) > 0 {
		scan, err = y2ScanWindows(jwt, scanQ, windows)
		if err != nil {
			if y2IsAuthRejected(err.Error()) {
				return nil, 0, 0, false, err
			}
			scan = nil
		}
	} else if scanQ != "" {
		// 学期名解析不出来 → 退回"近 100 天"
		scan, err = y2ScanByKeyword(jwt, scanQ, 100, 8, 21)
		if err != nil {
			if y2IsAuthRejected(err.Error()) {
				return nil, 0, 0, false, err
			}
			scan = nil
		}
	}

	kept = []y2Course{}
	type pendingItem struct {
		c    y2Course
		rows []y2Session
	}
	pending := []pendingItem{}

	for _, c := range courses {
		if n, ok := c["sessions"].(int); ok && n == 0 {
			hidden++ // 课程库明确说 0 讲
			continue
		}
		var rows []y2Session
		if scan != nil {
			rows = scan[jstr(c["courseId"])]
		}
		pending = append(pending, pendingItem{c: c, rows: rows})
	}

	// 2. 剩下的并行确认（最多 6 路）
	type judged struct {
		keep       bool
		row        y2Course
		hidden     bool
		unverified bool
	}
	results := make([]judged, len(pending))
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i := range pending {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			it := pending[i]
			c := it.c
			name := jstr(c["termName"])
			if name == "" {
				name = termNames[jstr(c["termId"])]
			}
			w := y2TermWindow(name)
			scannedThisTerm := false
			if scan != nil && w != nil {
				for _, x := range windows {
					if x.from == w.from && x.to == w.to {
						scannedThisTerm = true
						break
					}
				}
			}

			if len(it.rows) > 0 {
				// sub_status=6 不等于"真有流"：一门课 5 讲全是 6、实际只有 1 讲有流的情况
				// 很常见。所以逐讲真取一次流：讲次少的课全验（卡片上的数字就是准确讲数），
				// 讲次多的只抽最新 5 讲验，卡片上只标"有录像"不写数字。
				six := []y2Session{}
				for _, r := range it.rows {
					if y2IsRecordingStatus(r.Status) || y2MaybeRecording(r.Status) {
						six = append(six, r)
					}
				}
				cand := six
				if len(cand) == 0 {
					cand = it.rows
				}
				exact := len(cand) <= y2ProbeCap
				limit := 3
				if exact {
					limit = 0
				}
				playable, failed, n := y2VerifySessions(jwt, jstr(c["courseId"]), cand, limit, 6)
				if len(playable) > 0 {
					row := cloneRow(c)
					row["verified"] = true
					if exact {
						row["videoCount"] = len(playable)
					} else {
						row["videoCount"] = nil
					}
					row["totalSessions"] = len(it.rows)
					room := playable[0].Room
					if room == "" {
						room = cand[0].Room
					}
					row["room"] = room
					row["source"] = "scan"
					results[i] = judged{keep: true, row: row}
					return
				}
				if n > 0 && failed == n { // 请求全失败 → 不能当成没录像
					row := cloneRow(c)
					row["verified"] = false
					row["note"] = "取视频流超时，未能确认"
					results[i] = judged{keep: true, row: row, unverified: true}
					return
				}
				results[i] = judged{hidden: true} // 验过的讲次一条流都没有
				return
			}
			// 扫不到讲次不等于没录像：常见原因是这门课不在你的课表里（没选它）。
			// 继续往下走 detail / 自己的课表确认。
			_ = scannedThisTerm
			// 学期窗口没扫到（往届课程 / 学期名认不出来）→ 试 detail / 自己的课表
			if v, e := y2Videos(jwt, id, jstr(c["courseId"]), false, "", ""); e == nil && v != nil {
				if v.videoCount > 0 {
					row := cloneRow(c)
					row["verified"] = true
					row["videoCount"] = v.videoCount
					row["totalSessions"] = v.totalSessions
					row["room"] = v.room
					row["source"] = v.source
					results[i] = judged{keep: true, row: row}
					return
				}
				if v.totalSessions > 0 {
					results[i] = judged{hidden: true} // 有讲次，但一讲能播的都没有
					return
				}
			}
			row := cloneRow(c)
			row["verified"] = false
			row["note"] = "这个学期的时间窗没扫到讲次，接口也没给"
			results[i] = judged{keep: true, row: row, unverified: true}
		}(i)
	}
	wg.Wait()

	for i := range results {
		switch {
		case results[i].keep:
			kept = append(kept, results[i].row)
		case results[i].hidden:
			hidden++
		}
		if results[i].unverified {
			unverified++
		}
	}

	// 排序：先匹配度（课程名 / 教师名），再录像数，最后原始顺序
	orig := map[string]int{}
	for i, c := range courses {
		orig[jstr(c["courseId"])] = i
	}
	sort.SliceStable(kept, func(i, j int) bool {
		si := searchScore(jstr(kept[i]["title"]), jstr(kept[i]["teacher"]), "", q)
		sj := searchScore(jstr(kept[j]["title"]), jstr(kept[j]["teacher"]), "", q)
		if si != sj {
			return si > sj
		}
		vi, vj := y2CourseVideoCount(kept[i]), y2CourseVideoCount(kept[j])
		if vi != vj {
			return vi > vj
		}
		return orig[jstr(kept[i]["courseId"])] < orig[jstr(kept[j]["courseId"])]
	})

	return kept, hidden, unverified, false, nil
}

// ---------------------------------------------------------------- 老平台 → 新版 的课程匹配

// 新版里找同名同学期的课（没有学期就只按名字）。
// 注：这条链路（y2Match / y2MatchByTeacher / y2Rescue）在当前 server.js 版本里
// 没有任何路由调用 —— 老平台搜索那一段已经把"问新版"的步骤去掉了。
// 这里照样移植，行为逐行对齐，将来要用直接接上。
var (
	y2MatchMu    sync.Mutex
	y2MatchCache = map[string]y2MatchEntry{}
)

type y2MatchEntry struct {
	at time.Time
	id *string
}

func y2NormName(s string) string {
	s = y2SpaceRun.ReplaceAllString(jstr(s), "")
	s = strings.ReplaceAll(s, "（", "(")
	s = strings.ReplaceAll(s, "）", ")")
	return strings.ToLower(s)
}

// y2Match 老平台的课名/学期 → 新版 courseId（找不到给 nil）
func y2Match(jwt, name, term string) *string {
	key := y2JwtTail(jwt) + "|" + y2NormName(name) + "|" + termKeyOf(term)
	y2MatchMu.Lock()
	hit, ok := y2MatchCache[key]
	y2MatchMu.Unlock()
	if ok && time.Since(hit.at) < 10*time.Minute {
		return hit.id
	}

	var result *string
	if r, err := y2SearchCourses(jwt, name, "", 1, 40); err == nil && r != nil {
		list := r.courses
		sameName := []y2Course{}
		for _, c := range list {
			if y2NormName(jstr(c["title"])) == y2NormName(name) {
				sameName = append(sameName, c)
			}
		}
		if len(sameName) == 0 {
			// 名字不完全一样时，只接受"一边包含另一边"且唯一的那个
			loose := []y2Course{}
			for _, c := range list {
				a, b := y2NormName(jstr(c["title"])), y2NormName(name)
				if a != "" && b != "" && (strings.Contains(a, b) || strings.Contains(b, a)) {
					loose = append(loose, c)
				}
			}
			if len(loose) == 1 {
				result = strPtr(jstr(loose[0]["courseId"]))
			}
		} else if term != "" {
			want := termKeyOf(term)
			var found *string
			for _, c := range sameName {
				tn := jstr(c["termName"])
				if tn != "" && termKeyOf(tn) == want {
					found = strPtr(jstr(c["courseId"]))
					break
				}
			}
			if found != nil {
				result = found
			} else if len(sameName) == 1 {
				result = strPtr(jstr(sameName[0]["courseId"]))
			}
		} else if len(sameName) == 1 {
			result = strPtr(jstr(sameName[0]["courseId"]))
		}
	}

	y2MatchMu.Lock()
	if len(y2MatchCache) > 400 {
		y2MatchCache = map[string]y2MatchEntry{}
	}
	y2MatchCache[key] = y2MatchEntry{at: time.Now(), id: result}
	y2MatchMu.Unlock()
	return result
}

// y2FirstTeacher 取第一个教师名（Node: String(teacher||”).split(/[、,，\/]/)[0].trim()）
func y2FirstTeacher(s string) string {
	if i := strings.IndexAny(s, "、,，/"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// y2MatchByTeacher 名字对不上时的第二招：按教师搜，再从结果里挑"标题像这门课"且学期对的那个。
// 只接受唯一候选，宁可不合并，也不要把 A 课的录像挂到 B 课下面。
func y2MatchByTeacher(jwt, name, teacher, term string) *string {
	if jwt == "" || teacher == "" {
		return nil
	}
	r, err := y2SearchCourses(jwt, teacher, "", 1, 40)
	if err != nil || r == nil {
		return nil
	}
	list := r.courses
	if term != "" {
		want := termKeyOf(term)
		sameTerm := []y2Course{}
		for _, c := range list {
			if termKeyOf(jstr(c["termName"])) == want {
				sameTerm = append(sameTerm, c)
			}
		}
		if len(sameTerm) > 0 {
			list = sameTerm
		}
	}
	a := y2NormName(name)
	like := []y2Course{}
	for _, c := range list {
		b := y2NormName(jstr(c["title"]))
		if b != "" && a != "" && (b == a || strings.Contains(a, b) || strings.Contains(b, a)) {
			like = append(like, c)
		}
	}
	if len(like) == 1 {
		return strPtr(jstr(like[0]["courseId"]))
	}
	// 多个候选但只有一个带了这门课的字样 → 取最像的那个
	if len(like) > 1 {
		best := like[0]
		for _, c := range like[1:] {
			if len([]rune(y2NormName(jstr(c["title"])))) > len([]rune(y2NormName(jstr(best["title"])))) {
				best = c
			}
		}
		same := 0
		for _, c := range like {
			if y2NormName(jstr(c["title"])) == y2NormName(jstr(best["title"])) {
				same++
			}
		}
		if same == 1 {
			return strPtr(jstr(best["courseId"]))
		}
	}
	return nil
}

// y2RescueData 老平台一门课在新版上的情况
type y2RescueData struct {
	y2ID       string
	videoCount *int
	room       string
}

var (
	y2RescueMu    sync.Mutex
	y2RescueCache = map[string]y2RescueEntry{}
)

type y2RescueEntry struct {
	at   time.Time
	data *y2RescueData
}

// y2Rescue 这门课在新版平台上到底存不存在 / 有没有录像。
// 搜索结果里"老平台没录像"的课，必须先问过新版才能下"没有"的结论。
func y2Rescue(jwt string, c y2Course) *y2RescueData {
	if jwt == "" || c == nil {
		return nil
	}
	key := y2JwtTail(jwt) + "|" + jstr(c["id"]) + "|" + termKeyOf(jstr(c["term"]))
	y2RescueMu.Lock()
	hit, ok := y2RescueCache[key]
	y2RescueMu.Unlock()
	if ok && time.Since(hit.at) < 10*time.Minute {
		return hit.data
	}

	var data *y2RescueData
	if idn := y2Who(jwt); idn != nil {
		name := jstr(c["name"])
		if name == "" {
			name = jstr(c["title"])
		}
		var y2id *string
		if v, ok := c["y2CourseId"].(string); ok && v != "" {
			y2id = strPtr(v)
		} else {
			y2id = y2Match(jwt, name, jstr(c["term"]))
		}
		// 名字对不上时，只有"新近学期"才值得再按教师找一次（每次要多发一个请求）
		if y2id == nil && termIsRecent(jstr(c["term"])) {
			t := y2FirstTeacher(jstr(c["teacher"]))
			if t != "" {
				y2id = y2MatchByTeacher(jwt, name, t, jstr(c["term"]))
			}
		}
		if y2id != nil {
			// 只取讲次清单，不扫课表：搜索要快。点开课程时 /api/open 会再深挖
			var vc *int
			room := ""
			if v, err := y2Videos(jwt, idn, *y2id, false, "", ""); err == nil && v != nil && v.videoCount > 0 {
				n := v.videoCount
				vc = &n
				room = v.room
			}
			data = &y2RescueData{y2ID: *y2id, videoCount: vc, room: room}
		}
	}

	y2RescueMu.Lock()
	if len(y2RescueCache) > 400 {
		y2RescueCache = map[string]y2RescueEntry{}
	}
	y2RescueCache[key] = y2RescueEntry{at: time.Now(), data: data}
	y2RescueMu.Unlock()
	return data
}
