// 课表扫描：按天查课程行，以及学期时间窗 / 日期区间
//
// 对应 yanhe2.js 的 rowOf / dayRows / ymdShift / scanDays / termWindow /
// datesBetween / scanDates / scanByKeyword / findCourses / scanSessions。
//
// 这是"讲次直读"不可靠时最实在的一条路：一天一次请求，但**一次搜索只扫一遍**，
// 结果同时覆盖列表里所有课程，每行还自带 sub_status 和 room_name。
package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const y2DayList = "/courseapi/v3/course-live-role/search-role-course-list"

// y2Ymd 时间 → UTC 的 "YYYY-MM-DD"（对应 Node 的 toISOString().slice(0,10)）
func y2Ymd(t time.Time) string { return t.UTC().Format("2006-01-02") }

// y2YmdShift 日期字符串加减天数
func y2YmdShift(dateStr string, deltaDays int) string {
	d, err := time.ParseInLocation("2006-01-02", dateStr, time.UTC)
	if err != nil {
		return dateStr
	}
	return d.AddDate(0, 0, deltaDays).Format("2006-01-02")
}

// y2RowOf 课表里的一行 → y2Session
func y2RowOf(period, r map[string]any) y2Session {
	typ := y2PickStr(r, "type", "sub_type")

	// 教师：优先 teachers[0].realname / .name，再退回 lecturer_name
	teacher := ""
	if ts := y2Arr(r["teachers"]); len(ts) > 0 {
		if t0 := jobj(ts[0]); t0 != nil {
			teacher = y2PickStr(t0, "realname", "name")
		}
	}
	if teacher == "" {
		teacher = y2PickStr(r, "lecturer_name")
	}

	dur := y2Num(r["sub_duration"])
	if dur == 0 {
		dur = y2Num(r["duration"])
	}
	status := y2PickStr(r, "sub_status")
	if status == "" {
		status = y2PickStr(r, "status")
	}
	room := y2PickStr(r, "room_name")
	if room == "" {
		room = y2PickStr(r, "sub_room_name")
	}
	return y2Session{
		CourseID: y2PickStr(r, "course_id"),
		SubID:    y2PickStr(r, "sub_id"),
		Title:    y2PickStr(r, "title"),
		SubTitle: y2PickStr(r, "sub_title"),
		Status:   status,
		Duration: dur,
		StartAt:  y2TsOf(r["start_at"]),
		EndAt:    y2TsOf(r["end_at"]),
		Teacher:  teacher,
		Room:     room,
		Period:   y2PickStr(period, "name"),
		Type:     typ,
		Kind:     y2KindOfSub(typ),
		Drag:     y2PickStr(r, "is_allow_video_drag"),
	}
}

// y2DayRows 某一天（或某节课的时段）的课程行；keyword 走 like_title 过滤，响应会小很多
func y2DayRows(jwt, date, keyword string) ([]y2Session, error) {
	params := y2Params{
		y2P("search_time", date),
		y2P("tenant", y2Tenant),
		y2P("need_time_quantum", 1),
		y2P("unique_course", 1),
		y2P("with_sub_duration", 1),
		y2P("with_sub_data", 1),
		y2P("has_frame", 1),
		y2P("nteacher", 0),
	}
	if keyword != "" {
		params = append(params, y2P("like_title", keyword))
	}
	body, err := y2GetJSON(jwt, y2DayList, params)
	if err != nil {
		return nil, err
	}
	return y2ParseDayRows(body), nil
}

// y2ParseDayRows 只做解析
func y2ParseDayRows(body any) []y2Session {
	b := jobj(body)
	out := []y2Session{}
	for _, pv := range y2Arr(j2get(b, "list")) {
		p := jobj(pv)
		for _, rv := range y2Arr(j2get(p, "list")) {
			if r := jobj(rv); r != nil {
				out = append(out, y2RowOf(p, r))
			}
		}
	}
	return out
}

// y2DayRows 一天的结果
type y2DayScanned struct {
	date string
	rows []y2Session
}

// y2ScanDays 并发跑一组日期，返回每天的行。
// 任何一天遇到"会话被拒绝"就整体放弃并抛出（对应 Node 里 worker 里 throw 让 Promise.all 失败）。
func y2ScanDays(jwt string, dates []string, keyword string, concurrency int) ([]y2DayScanned, error) {
	out := []y2DayScanned{}
	var mu sync.Mutex // 保护 out / firstErr
	var wg sync.WaitGroup
	var firstErr error
	var aborted atomic.Bool // 有人失败后其余 worker 尽快收工
	var idx atomic.Int64

	n := concurrency
	if len(dates) < n {
		n = len(dates)
	}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if aborted.Load() {
					return
				}
				k := int(idx.Add(1)) - 1
				if k >= len(dates) {
					return
				}
				d := dates[k]

				rows, err := y2DayRows(jwt, d, keyword)
				mu.Lock()
				if err != nil {
					if y2IsAuthRejected(err.Error()) {
						if firstErr == nil {
							firstErr = err
						}
						aborted.Store(true)
						mu.Unlock()
						return
					}
					out = append(out, y2DayScanned{date: d, rows: []y2Session{}})
				} else {
					out = append(out, y2DayScanned{date: d, rows: rows})
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// ---------------------------------------------------------------- 学期时间窗

// y2Window 学期时间窗
type y2Window struct {
	from string
	to   string
}

var y2TermWindowRe = regexp.MustCompile(`(20\d{2})\s*[-—~至]\s*(20\d{2})`)

// y2TermWindow 从学期名推这段时间窗。北理的学期名长这样：
//
//	"2024-2025 第一学期" / "2024-2025学年第二学期" / "2024-2025-1"
//
// 窗口刻意贴紧教学周（开头结尾各留一点），这样"连续没课就提前收工"才不会被
// 寒暑假的长空白卡住。
func y2TermWindow(name string) *y2Window {
	s := y2SpaceRun.ReplaceAllString(y2Text(name), "")
	if s == "" {
		return nil
	}
	m := y2TermWindowRe.FindStringSubmatch(s)
	if m == nil {
		return nil
	}
	y1, err := strconv.Atoi(m[1])
	if err != nil {
		return nil
	}
	isThird := strings.Contains(s, "第三") || strings.Contains(s, "3学期") || strings.Contains(s, "夏")
	isSecond := strings.Contains(s, "第二") || strings.Contains(s, "2学期") ||
		strings.Contains(s, "Ⅱ") || strings.Contains(s, "春") || y2HasTermSuffix(s, "2")
	isFirst := strings.Contains(s, "第一") || strings.Contains(s, "1学期") ||
		strings.Contains(s, "Ⅰ") || strings.Contains(s, "秋") || y2HasTermSuffix(s, "1")

	switch {
	case isThird:
		return &y2Window{from: fmt.Sprintf("%d-06-15", y1), to: fmt.Sprintf("%d-09-15", y1)}
	case isSecond:
		return &y2Window{from: fmt.Sprintf("%d-02-15", y1+1), to: fmt.Sprintf("%d-07-15", y1+1)}
	case isFirst:
		return &y2Window{from: fmt.Sprintf("%d-09-01", y1), to: fmt.Sprintf("%d-01-31", y1+1)}
	}
	// 学期不明：整个学年
	return &y2Window{from: fmt.Sprintf("%d-09-01", y1), to: fmt.Sprintf("%d-07-15", y1+1)}
}

// y2HasTermSuffix 对应 Node 的 /[-_ ]2$/（空白已经被去掉了，只剩 [-_]2$）
func y2HasTermSuffix(s, last string) bool {
	if !strings.HasSuffix(s, last) {
		return false
	}
	if len(s) < 2 {
		return false
	}
	c := s[len(s)-2]
	return c == '-' || c == '_' || c == ' '
}

// y2DatesBetween [from, to] 之间的日期列表，**旧的在前**（从学期头开始扫）
func y2DatesBetween(from, to string, cap int) []string {
	out := []string{}
	start, err1 := time.ParseInLocation("2006-01-02", from, time.UTC)
	end, err2 := time.ParseInLocation("2006-01-02", to, time.UTC)
	if err1 != nil || err2 != nil || start.After(end) {
		return out
	}
	for d := start; !d.After(end) && len(out) < cap; d = d.AddDate(0, 0, 1) {
		out = append(out, d.Format("2006-01-02"))
	}
	return out
}

// ---------------------------------------------------------------- 扫描

// y2ScanDates 扫一段日期范围内的课表，按 courseId 分组。
// 这是"讲次直读"不可靠时最实在的一条路：一天一次请求，但**一次搜索只扫一遍**，
// 结果同时覆盖列表里所有课程，每行还自带 sub_status 和 room_name。
func y2ScanDates(jwt, keyword string, dates []string, concurrency, maxEmpty int) (map[string][]y2Session, error) {
	map_ := map[string][]y2Session{}
	emptyStreak := 0
	const batch = 7

	for base := 0; base < len(dates); base += batch {
		end := base + batch
		if end > len(dates) {
			end = len(dates)
		}
		scanned, err := y2ScanDays(jwt, dates[base:end], keyword, concurrency)
		if err != nil {
			return nil, err
		}
		hit := 0
		for _, sc := range scanned {
			for _, r := range sc.rows {
				if r.CourseID == "" {
					continue
				}
				if keyword != "" && r.Title != "" && !strings.Contains(r.Title, keyword) {
					continue
				}
				if y2IsLiveStatus(r.Status) {
					continue
				}
				list := map_[r.CourseID]
				dup := false
				for _, x := range list {
					if x.SubID == r.SubID {
						dup = true
						break
					}
				}
				if dup {
					continue
				}
				if r.StartAt > 0 {
					r.Date = y2DateOf(r.StartAt)
				} else {
					r.Date = sc.date
				}
				map_[r.CourseID] = append(list, r)
				hit++
			}
		}
		if hit > 0 {
			emptyStreak = 0
		} else {
			emptyStreak += batch
		}
		if emptyStreak >= maxEmpty {
			break
		}
	}
	for _, list := range map_ {
		sort.SliceStable(list, func(i, j int) bool { return list[i].StartAt > list[j].StartAt })
	}
	return map_, nil
}

// y2ScanByKeyword 按"最近 N 天"扫（不知道学期时间窗时的兜底）
func y2ScanByKeyword(jwt, keyword string, days, concurrency, maxEmpty int) (map[string][]y2Session, error) {
	today := y2Ymd(time.Now())
	dates := make([]string, 0, days)
	for i := 0; i < days; i++ {
		dates = append(dates, y2YmdShift(today, -i))
	}
	return y2ScanDates(jwt, keyword, dates, concurrency, maxEmpty)
}

// y2FindCourses 兜底：按关键词在近期课表里找课（只统计有录像的讲次）
func y2FindCourses(jwt, keyword string, days int) ([]y2Course, error) {
	if days <= 0 {
		days = 21
	}
	today := y2Ymd(time.Now())
	dates := make([]string, 0, days)
	for i := 0; i < days; i++ {
		dates = append(dates, y2YmdShift(today, -i))
	}
	scanned, err := y2ScanDays(jwt, dates, keyword, 4)
	if err != nil {
		return nil, err
	}

	rows := []y2Course{}
	index := map[string]int{}
	for _, sc := range scanned {
		for _, r := range sc.rows {
			if r.CourseID == "" {
				continue
			}
			if keyword != "" && !strings.Contains(r.Title, keyword) {
				continue
			}
			if !y2IsRecordingStatus(r.Status) && !y2MaybeRecording(r.Status) {
				continue
			}
			i, ok := index[r.CourseID]
			if !ok {
				kind := r.Kind
				if kind == "" {
					kind = "record"
				}
				rows = append(rows, y2Course{
					"courseId": r.CourseID,
					"title":    r.Title,
					"teacher":  r.Teacher,
					"kind":     kind,
					"termId":   "",
					"termName": "",
					"sessions": 0,
					"lastDate": sc.date,
					"cover":    "",
					"room":     r.Room,
				})
				i = len(rows) - 1
				index[r.CourseID] = i
			}
			cur := rows[i]
			cur["sessions"] = cur["sessions"].(int) + 1
			if cur["room"] == "" && r.Room != "" {
				cur["room"] = r.Room
			}
			if sc.date > jstr(cur["lastDate"]) {
				cur["lastDate"] = sc.date
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i]["sessions"].(int) > rows[j]["sessions"].(int) })
	return rows, nil
}

// y2ScanSessions 兜底：按天扫某门课的讲次
func y2ScanSessions(jwt, courseID, keyword string, days, maxEmpty int) ([]y2Session, error) {
	if days <= 0 {
		days = 130
	}
	today := y2Ymd(time.Now())
	found := []y2Session{}
	emptyStreak := 0
	const batch = 7

	for base := 0; base < days; base += batch {
		end := base + batch
		if end > days {
			end = days
		}
		dates := make([]string, 0, end-base)
		for i := base; i < end; i++ {
			dates = append(dates, y2YmdShift(today, -i))
		}
		scanned, err := y2ScanDays(jwt, dates, keyword, 4)
		if err != nil {
			return nil, err
		}
		hit := 0
		for _, sc := range scanned {
			for _, r := range sc.rows {
				if r.CourseID != courseID {
					continue
				}
				dup := false
				for _, x := range found {
					if x.SubID == r.SubID {
						dup = true
						break
					}
				}
				if dup {
					continue
				}
				if r.StartAt > 0 {
					r.Date = y2DateOf(r.StartAt)
				} else {
					r.Date = sc.date
				}
				found = append(found, r)
				hit++
			}
		}
		if hit > 0 {
			emptyStreak = 0
		} else {
			emptyStreak += batch
		}
		if emptyStreak >= maxEmpty {
			break
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].StartAt > found[j].StartAt })
	return found, nil
}
