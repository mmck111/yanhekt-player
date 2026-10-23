// 课程详情：/v1/course + /v1/course/session/list + /v1/video
//
// 对应 server.js 的 buildCourse / getCourse / rawSessions / probeOldCourses
package main

import (
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"
)

// ---------------------------------------------------------------- 数据结构

type courseInfo struct {
	ID         any      `json:"id"`
	Name       any      `json:"name"`
	Code       any      `json:"code"`
	College    any      `json:"college"`
	Year       any      `json:"year"`
	Semester   any      `json:"semester"`
	Professors []string `json:"professors"`
}

type sessionRow struct {
	ID        any    `json:"id"`
	Title     string `json:"title"`
	Week      any    `json:"week"`
	Date      string `json:"date"`
	StartedAt any    `json:"started_at"`
	Location  string `json:"location"`
	Duration  any    `json:"duration"`
	Main      any    `json:"main"`
	VGA       any    `json:"vga"`
}

type statsInfo struct {
	Total      int `json:"total"`
	Playable   int `json:"playable"`
	Unresolved int `json:"unresolved"`
}

type courseOK struct {
	Ok       bool         `json:"ok"`
	Course   *courseInfo  `json:"course"`
	Sessions []sessionRow `json:"sessions"`
	Stats    statsInfo    `json:"stats"`
}

type courseFail struct {
	Ok    bool   `json:"ok"`
	Error string `json:"error"`
	Code  any    `json:"code,omitempty"`
}

// courseData 是内部结果；序列化时按成功/失败换成不同的结构（字段与 Node 版一致）
type courseData struct {
	ok       bool
	fail     bool // 上游请求失败（Node 版这里会抛错 → 500），不要当"课程不存在"缓存起来
	errMsg   string
	code     any
	course   *courseInfo
	sessions []sessionRow
	stats    statsInfo
}

func (d *courseData) toJSON() any {
	if d == nil {
		return courseFail{Ok: false, Error: "课程不存在或无权访问", Code: nil}
	}
	if !d.ok {
		return courseFail{Ok: false, Error: d.errMsg, Code: d.code}
	}
	sessions := d.sessions
	if sessions == nil {
		sessions = []sessionRow{}
	}
	return courseOK{Ok: true, Course: d.course, Sessions: sessions, Stats: d.stats}
}

// mainURL / vgaURL 取一讲的播放地址（可能为 nil）
func (s sessionRow) mainURL() string { return jstr(s.Main) }
func (s sessionRow) vgaURL() string  { return jstr(s.VGA) }

// ---------------------------------------------------------------- 讲次原始列表

type rawSessionEntry struct {
	at   time.Time
	list []map[string]any
}

var (
	rawSessMu    sync.Mutex
	rawSessCache = map[string]rawSessionEntry{}
)

// rawSessions 只取某门课的节次原始列表（1~2 个请求），用于搜索时快速判断有没有录像
func rawSessions(courseID, token string) ([]map[string]any, error) {
	last10 := token
	if len(last10) > 10 {
		last10 = last10[len(last10)-10:]
	}
	key := "s:" + last10 + ":" + courseID

	rawSessMu.Lock()
	hit, ok := rawSessCache[key]
	rawSessMu.Unlock()
	if ok && time.Since(hit.at) < 5*time.Minute {
		return hit.list, nil
	}

	raw := []map[string]any{}
	for page := 1; page <= 20; page++ {
		j, err := apiCall(fmt.Sprintf("/v1/course/session/list?course_id=%s&order_type=asc&page=%d&page_size=100", courseID, page), token)
		if err != nil {
			return nil, err
		}
		list := sessionListOf(j)
		if len(list) == 0 {
			break
		}
		raw = append(raw, list...)
		if len(list) < 100 {
			break
		}
	}

	rawSessMu.Lock()
	if len(rawSessCache) > 500 {
		rawSessCache = map[string]rawSessionEntry{}
	}
	rawSessCache[key] = rawSessionEntry{at: time.Now(), list: raw}
	rawSessMu.Unlock()
	return raw, nil
}

// sessionListOf 从接口返回里取节次数组（j.data 可能是数组，也可能是 {data:[...]}）
func sessionListOf(j map[string]any) []map[string]any {
	out := []map[string]any{}
	if j == nil {
		return out
	}
	if a := jarr(j["data"]); a != nil {
		for _, it := range a {
			if m := jobj(it); m != nil {
				out = append(out, m)
			}
		}
		return out
	}
	if d := jobj(j["data"]); d != nil {
		for _, it := range jarr(d["data"]) {
			if m := jobj(it); m != nil {
				out = append(out, m)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------- 课程详情

var (
	courseMu    sync.Mutex
	courseCache = map[string]courseEntry{}
)

type courseEntry struct {
	at   time.Time
	data *courseData
}

// buildCourse 拉一门课的全部讲次 + 每讲的播放地址
func buildCourse(id, token string) *courseData {
	cRes, err := apiCall("/v1/course?id="+encodeURIComponent(id), token)
	if err != nil {
		return &courseData{ok: false, fail: true, errMsg: err.Error()}
	}
	if cRes == nil || !codeIsZeroStrict(cRes) || jobj(cRes["data"]) == nil {
		msg := "课程不存在或无权访问"
		if cRes != nil {
			if m := jstr(cRes["message"]); m != "" {
				msg = m
			}
		}
		var code any
		if cRes != nil {
			code = cRes["code"]
		}
		return &courseData{ok: false, errMsg: msg, code: code}
	}
	c := jobj(cRes["data"])

	// 注意：v2 的 session/list 对多数账号返回空数组，v1 才带 videos
	raw := []map[string]any{}
	var pageErr error
	for page := 1; page <= 20; page++ {
		j, err := apiCall(fmt.Sprintf("/v1/course/session/list?course_id=%s&order_type=asc&page=%d&page_size=100", jstr(c["id"]), page), token)
		if err != nil {
			pageErr = err // Node 版直接抛错 → 500，且不写缓存；这里也不能把半份讲次表当完整结果
			break
		}
		arr := sessionListOf(j)
		if len(arr) == 0 {
			break
		}
		raw = append(raw, arr...)
		if len(arr) < 100 {
			break
		}
	}
	if pageErr != nil {
		return &courseData{ok: false, fail: true, errMsg: pageErr.Error()}
	}

	sessions := []sessionRow{}
	unresolved := 0
	for _, s := range raw {
		var v map[string]any
		if a := jarr(s["videos"]); len(a) > 0 {
			v = jobj(a[0])
		}
		ids := videoIDs(s)

		if v == nil || (!truthy(v["main"]) && !truthy(v["vga"])) {
			if len(ids) > 0 {
				// 一次课可能有多个视频记录（全景 / 课件 / 教室），一个不行就试下一个，
				// 以前只看 video_ids[0]，正好碰到没有 main 的那条就会误判成"没有录像"
				try := ids
				if len(try) > 3 {
					try = try[:3]
				}
				for _, vid := range try {
					r, err := apiCall(fmt.Sprintf("/v1/video?id=%s&course_id=%s", jstr(vid), jstr(c["id"])), token)
					if err != nil {
						continue
					}
					if !codeIsZeroStrict(r) {
						continue
					}
					cand := jobj(r["data"])
					if cand == nil {
						continue
					}
					if truthy(cand["main"]) || truthy(cand["vga"]) {
						v = cand
						break
					}
					if v == nil {
						v = cand
					}
				}
			}
		}

		if (v == nil || (!truthy(v["main"]) && !truthy(v["vga"]))) && len(ids) > 0 {
			unresolved++
		}

		var duration any
		if v != nil && truthy(v["duration"]) {
			duration = jnum(v["duration"])
		}
		var main, vga any
		if v != nil {
			if truthy(v["main"]) {
				main = jstr(v["main"])
			}
			if truthy(v["vga"]) {
				vga = jstr(v["vga"])
			}
		}
		startedAt := jstr(s["started_at"])
		var startedAny any
		if startedAt != "" {
			startedAny = startedAt
		}
		var week any
		if truthy(s["week_number"]) {
			week = s["week_number"]
		}

		date := ""
		if len(startedAt) >= 10 {
			date = startedAt[:10]
		}
		sessions = append(sessions, sessionRow{
			ID:        s["id"],
			Title:     orStr(jstr(s["section_group_title"]), jstr(s["title"])),
			Week:      week,
			Date:      date,
			StartedAt: startedAny,
			Location:  jstr(s["location"]),
			Duration:  duration,
			Main:      main,
			VGA:       vga,
		})
	}

	sort.SliceStable(sessions, func(i, j int) bool {
		return jstr(sessions[j].StartedAt) < jstr(sessions[i].StartedAt)
	})

	// 课程信息
	profs := []string{}
	for _, p := range jarr(c["professors"]) {
		if len(profs) >= 5 {
			break
		}
		if s, ok := p.(string); ok {
			profs = append(profs, s)
			continue
		}
		o := jobj(p)
		if o == nil {
			continue
		}
		name := orStr(jstr(o["name"]), jstr(o["realname"]))
		if name != "" {
			profs = append(profs, name)
		}
	}

	playable := 0
	for _, s := range sessions {
		if truthy(s.Main) || truthy(s.VGA) {
			playable++
		}
	}

	return &courseData{
		ok: true,
		course: &courseInfo{
			ID:         c["id"],
			Name:       c["name_zh"],
			Code:       c["code"],
			College:    c["college_name"],
			Year:       c["school_year"],
			Semester:   c["semester"],
			Professors: profs,
		},
		sessions: sessions,
		stats:    statsInfo{Total: len(sessions), Playable: playable, Unresolved: unresolved},
	}
}

// getCourse 带缓存（60 秒），force 时强制重拉
func getCourse(id, token string, force bool) *courseData {
	key := token + ":" + id
	courseMu.Lock()
	hit, ok := courseCache[key]
	courseMu.Unlock()
	if !force && ok && time.Since(hit.at) < time.Minute {
		return hit.data
	}
	data := buildCourse(id, token)
	courseMu.Lock()
	if len(courseCache) > 300 {
		courseCache = map[string]courseEntry{}
	}
	courseCache[key] = courseEntry{at: time.Now(), data: data}
	courseMu.Unlock()
	return data
}

// ---------------------------------------------------------------- 讲次探测

// videoIDs 对应 Node 的 `Array.isArray(s.video_ids) ? s.video_ids : (s.video_ids ? [s.video_ids] : [])`
func videoIDs(s map[string]any) []any {
	if a := jarr(s["video_ids"]); a != nil {
		return a
	}
	if v := s["video_ids"]; truthy(v) {
		return []any{v}
	}
	return nil
}

// videoObjForProbe 对应探测逻辑里的取法：数组取第一个，对象就当它本身
func videoObjForProbe(s map[string]any) map[string]any {
	if a := jarr(s["videos"]); a != nil {
		if len(a) > 0 {
			return jobj(a[0])
		}
		return nil
	}
	if m := jobj(s["videos"]); m != nil {
		return m
	}
	return nil
}

var reOnlineRoom = regexp.MustCompile(`线上|网络|直播|云课堂|腾讯|钉钉|zoom`)

// probedRow 探过"有没有录像"的课程行（字段沿用 Node 版的 name/term/teacher/room/videoCount/verified/id）
type probedRow struct {
	courseRow
	VideoCount    *int   `json:"videoCount"`              // null 或数字
	TotalSessions *int   `json:"totalSessions,omitempty"` // 只有探到节次才有
	Verified      bool   `json:"verified"`                // 是否确认有录像
	LatestDate    *string `json:"latestDate,omitempty"`   // 最新一讲的日期（Node 成功行一定有这个键，哪怕是空串）
	Tag           string `json:"tag,omitempty"`           // 未确认 / 取不到地址 / 本学期无录像
	Note          string `json:"note,omitempty"`          // 给人看的原因
}

// probeOldCourses 给课程库的搜索结果补上「有没有录像 / 教室」。
//
// 老平台的节次列表一条请求就够，所以敢对几十门课直接探（并发 8）。
func probeOldCourses(courses []courseRow, token string, concurrency int) ([]probedRow, int) {
	out := []probedRow{}
	hidden := 0
	if len(courses) == 0 {
		return out, hidden
	}

	queue := append([]courseRow{}, courses...)
	var qmu, omu sync.Mutex
	workers := concurrency
	if workers > len(courses) {
		workers = len(courses)
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				qmu.Lock()
				if len(queue) == 0 {
					qmu.Unlock()
					return
				}
				c := queue[0]
				queue = queue[1:]
				qmu.Unlock()

				rows, hide := probeOneCourse(c, token)
				omu.Lock()
				out = append(out, rows...)
				if hide {
					hidden++
				}
				omu.Unlock()
			}
		}()
	}
	wg.Wait()
	oldVerdictFlush(false)

	// 结果按原顺序排好（Node 版也是这么排的）
	order := map[string]int{}
	for i, c := range courses {
		order[c.ID] = i
	}
	sort.SliceStable(out, func(i, j int) bool { return order[out[i].ID] < order[out[j].ID] })
	return out, hidden
}

// probeOneCourse 探一门课：返回（要输出的行，是否算"藏掉"）
func probeOneCourse(c courseRow, token string) ([]probedRow, bool) {
	// 先看落盘缓存：手机端重开 App 后也能直接命中，不用把几十门课重新探一遍
	if v, ok := oldVerdictGet(c.ID, token); ok {
		return oldVerdictRow(c, v), v.Hide
	}
	rows, hide := probeOneCourseFresh(c, token)
	if v, ok := oldVerdictFrom(rows, hide); ok {
		oldVerdictSet(c.ID, token, v)
	}
	return rows, hide
}

// probeOneCourseFresh 真去上游探
func probeOneCourseFresh(c courseRow, token string) ([]probedRow, bool) {
	raw, err := rawSessions(c.ID, token)
	if err != nil {
		// 探测失败（没权限 / 网络）→ 保留，但不给结论
		return []probedRow{{courseRow: c, VideoCount: nil, Verified: false, Note: err.Error()}}, false
	}

	// 有能播的讲次就算有录像：videos 可能是数组也可能是对象，video_ids 也算
	playable := []map[string]any{}
	for _, s := range raw {
		v := videoObjForProbe(s)
		if (v != nil && (truthy(v["main"]) || truthy(v["vga"]))) || len(videoIDs(s)) > 0 {
			playable = append(playable, s)
		}
	}

	// 节次列表压根没给（没权限 / 还没排课）→ 不下结论，标"未确认"，别把课藏了
	if len(raw) == 0 {
		return []probedRow{{
			courseRow: c, VideoCount: nil, Verified: false, Tag: "未确认",
			Note: "节次列表为空，可能没有权限或还没排课",
		}}, false
	}

	if len(playable) == 0 {
		// 有节次但没有能用的播放地址。这有两种完全不同的情况：
		//  1) 有 video_ids → 是"取不到地址"（登录过期 / 该接口没给），重登就好
		//  2) 真的一个视频记录都没有 → 本学期的课仍然不下结论（录像可能还在传）
		hasVideoRecord := false
		for _, s := range raw {
			if len(videoIDs(s)) > 0 {
				hasVideoRecord = true
				break
			}
		}
		total := len(raw)
		if hasVideoRecord {
			return []probedRow{{
				courseRow: c, VideoCount: nil, TotalSessions: &total, Verified: false, Tag: "取不到地址",
				Note: "这门课有录像记录，但没拿到播放地址 —— 多半是登录过期了，重新登录再搜一次",
			}}, false
		}
		if termIsRecent(c.Term) {
			return []probedRow{{
				courseRow: c, VideoCount: nil, TotalSessions: &total, Verified: false, Tag: "本学期无录像",
				Note: "本学期的课，录像可能还在上传（过几天再看）",
			}}, false
		}
		return nil, true
	}

	sort.SliceStable(playable, func(i, j int) bool {
		return jstr(playable[j]["started_at"]) < jstr(playable[i]["started_at"])
	})

	// 教室：课程自带的 classrooms 最准；节次的 location 可能是编码，换成名字
	fromSessions := []string{}
	for _, s := range playable {
		r := roomName(jstr(s["location"]), &c)
		if r == "" || reOnlineRoom.MatchString(r) {
			continue
		}
		fromSessions = append(fromSessions, r)
	}
	room := c.Room
	if room == "" && len(fromSessions) > 0 {
		room = fromSessions[0]
	}

	count := len(playable)
	total := len(raw)
	latest := jstr(playable[0]["started_at"])
	if len(latest) > 10 {
		latest = latest[:10]
	}
	c.Room = room
	return []probedRow{{
		courseRow:     c,
		VideoCount:    &count,
		TotalSessions: &total,
		Verified:      true,
		LatestDate:    &latest,
	}}, false
}
