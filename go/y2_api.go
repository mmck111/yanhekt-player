// 新版平台的上游接口：学期、我的课程、课表、讲次、课程检索、课程详情
//
// 对应 yanhe2.js 的 probeVariants / termList / myCourses / weekSchedule /
// scheduleMap / subInfo / searchCourses / courseDetail / courseVideos 以及
// kindOfSub / isRecordingStatus / maybeRecording / isLiveStatus 这些状态判断。
package main

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// 讲次状态
var y2StatusName = map[string]string{"1": "live", "2": "upcoming", "3": "processing", "5": "ended", "6": "playable"}

// 画面的类型（上游给的是数字编码）
var y2RecordedType = map[string]string{"3": "camera", "2": "screen", "4": "room"}

// 直播类讲次（其余都算录播）
var y2LiveSubTypes = []string{"course_live", "ilive", "olive", "live", "large_class", "live_video"}

// y2KindOfSub 讲次类型：直播类 vs 录播类
func y2KindOfSub(t any) string {
	s := strings.ToLower(y2Text(t))
	if s == "" {
		return "record"
	}
	for _, x := range y2LiveSubTypes {
		if s == x {
			return "live"
		}
	}
	return "record"
}

// y2IsRecordingStatus 6 = 可播放录像，直接算有录像
func y2IsRecordingStatus(status string) bool { return status == "6" }

// y2MaybeRecording 5 = 已结束 / ” = 未知：有可能已有回放，需要实测一次视频流
func y2MaybeRecording(status string) bool { return status == "5" || status == "" }

// y2IsLiveStatus 1 = 直播中 2 = 未开始 3 = 处理中：不算录像（也不提供直播）
func y2IsLiveStatus(status string) bool {
	return status == "1" || status == "2" || status == "3"
}

// ---------------------------------------------------------------- 变体探测

// y2Try 一种"参数写法"（对应 yanhe2.js 里 tries 数组的元素）
type y2Try struct {
	name   string
	path   string
	params y2Params
	fail   string // 失败原因（对应 Node 里的 t.__fail，只用于诊断）
}

// y2Probe 一个接口一次试几种参数写法，成功一次就记住（对应 probeVariants）。
//
// 返回第一个"有内容"的结果；全都没内容就返回第一个"通了但没内容"的结果；
// 一个都没通就把最后一次错误抛给上层。
//
// 注：Node 版末尾还有一段 authOnly 判断（全部失败都是"会话被拒绝"时抛最后一次错误），
// 但两个分支抛的是同一个错误，行为等价，这里没再重复一遍。
func y2Probe[T any](jwt string, tries []y2Try, extract func(body any) (T, bool)) (T, *y2Try, error) {
	var zero T
	var empty T
	var emptyTry *y2Try
	hasEmpty := false
	var lastErr error

	for i := range tries {
		t := &tries[i]
		body, err := y2GetJSON(jwt, t.path, t.params)
		if err != nil {
			t.fail = err.Error()
			lastErr = err
			continue
		}
		got, has := extract(body)
		if has {
			return got, t, nil
		}
		if !hasEmpty {
			empty, emptyTry, hasEmpty = got, t, true
		}
	}
	if hasEmpty {
		return empty, emptyTry, nil
	}
	if lastErr == nil {
		lastErr = errors.New("接口不可用")
	}
	return zero, nil, lastErr
}

// ---------------------------------------------------------------- 学期列表

// y2Term 学期表里的一项
type y2Term struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

var (
	y2TermMu    sync.Mutex
	y2TermCache []y2Term
	y2TermAt    time.Time
)

var y2YearPrefixRe = regexp.MustCompile(`^\d{4}`)

// y2TermsFromList [{id,term_name,...}] → [{id,name}]
func y2TermsFromList(raw any) []y2Term {
	out := []y2Term{}
	for _, it := range y2Arr(raw) {
		t := jobj(it)
		if t == nil {
			continue
		}
		id := y2PickStr(t, "id", "term_id", "termId")
		name := y2PickStr(t, "term_name", "termName", "name", "title")
		if id != "" && name != "" {
			out = append(out, y2Term{ID: id, Name: name})
		}
	}
	return out
}

// y2FlatTermYears 学年分组结构 {schoolYear:[{id,term_name}]} 拍平
func y2FlatTermYears(data any) []y2Term {
	if a, ok := data.([]any); ok {
		return y2TermsFromList(a)
	}
	out := []y2Term{}
	m, ok := data.(map[string]any)
	if !ok {
		return out
	}
	for _, year := range y2Keys(m) {
		for _, it := range y2Arr(m[year]) {
			t := jobj(it)
			if t == nil {
				continue
			}
			id := y2PickStr(t, "id", "term_id")
			name := y2PickStr(t, "term_name", "termName", "name")
			if id == "" || name == "" {
				continue
			}
			// 名字里没写学年就补上（"第一学期" → "2024-2025 第一学期"）
			if !y2YearPrefixRe.MatchString(name) {
				name = strings.TrimSpace(year + " " + name)
			}
			out = append(out, y2Term{ID: id, Name: name})
		}
	}
	return out
}

// y2ParseTermList 只做解析（termList 的变体探测用它）
func y2ParseTermList(body any) []y2Term {
	b := jobj(body)
	p := jobj(j2get(b, "params"))
	if p == nil {
		p = map[string]any{}
	}
	a := y2TermsFromList(y2First(p["list"], p["data"], j2get(b, "data"), j2get(b, "list")))
	if len(a) > 0 {
		return a
	}
	// 另一种结构：{result:{data:{2024-2025:[...]}}} / {data:{data:{...}}}
	return y2FlatTermYears(y2First(jobj(j2get(b, "result"))["data"], jobj(j2get(b, "data"))["data"]))
}

// y2TermList 学期列表（缓存 1 小时）
func y2TermList(jwt string) ([]y2Term, error) {
	y2TermMu.Lock()
	if len(y2TermCache) > 0 && time.Since(y2TermAt) < time.Hour {
		list := y2TermCache
		y2TermMu.Unlock()
		return list, nil
	}
	y2TermMu.Unlock()

	tries := []y2Try{
		{name: "course/term-list", path: "/vlabpassportapi/v1/course/term-list"},
		{name: "get-term-years", path: "/courseapi/index.php/v2/course/get-term-years", params: y2Params{y2P("tenant", y2Tenant)}},
		{name: "schedule/search-term", path: "/courseapi/index.php/v2/schedule/search-term", params: y2Params{y2P("tenant", y2Tenant)}},
	}
	got, _, err := y2Probe(jwt, tries, func(body any) ([]y2Term, bool) {
		a := y2ParseTermList(body)
		return a, len(a) > 0
	})
	if err != nil {
		return nil, err
	}
	if len(got) > 0 {
		y2TermMu.Lock()
		y2TermCache = got
		y2TermAt = time.Now()
		y2TermMu.Unlock()
	}
	return got, nil
}

// ---------------------------------------------------------------- 我的课程

// y2MyCourses 我的课程列表
func y2MyCourses(jwt string) ([]y2Course, error) {
	body, err := y2GetJSON(jwt, "/vlabpassportapi/v1/account-profile/course", nil)
	if err != nil {
		return nil, err
	}
	return y2ParseMyCourses(body), nil
}

// y2ParseMyCourses 只做解析
func y2ParseMyCourses(body any) []y2Course {
	// 结构：{code:1000, params:{result:{data:[{Id,Title,Teacher,TermName,...}]}}}
	b := jobj(body)
	result := y2First(jobj(j2get(jobj(j2get(b, "params")), "result")), j2get(b, "result"))
	if result == nil {
		result = map[string]any{}
	}

	var list []any
	if rm := jobj(result); rm != nil {
		if a, ok := rm["data"].([]any); ok {
			list = a
		}
	}
	if list == nil {
		if a, ok := result.([]any); ok {
			list = a
		}
	}
	if list == nil {
		if a, ok := j2get(b, "data").([]any); ok {
			list = a
		}
	}

	out := []y2Course{}
	for _, it := range list {
		c := jobj(it)
		if c == nil {
			continue
		}
		id := y2PickStr(c, "Id", "course_id", "id")
		if id == "" {
			continue
		}
		out = append(out, y2Course{
			"id":      id,
			"name":    y2PickStr(c, "Title", "course_title", "name"),
			"teacher": y2PickStr(c, "Teacher", "lecturer_name"),
			"term":    y2PickStr(c, "TermName"),
			"cover":   y2PickStr(c, "Thumb"),
		})
	}
	return out
}

// ---------------------------------------------------------------- 课表

// y2WeekRow 课表里的一行（一次一周）
type y2WeekRow struct {
	SubID      string `json:"subId"`
	CourseID   string `json:"courseId"`
	Title      string `json:"title"`
	CourseName string `json:"courseName"`
	Date       string `json:"date"`
	StartAt    int64  `json:"startAt"`
	EndAt      int64  `json:"endAt"`
	Duration   int    `json:"duration"`
	Status     string `json:"status"`
	Room       string `json:"room"`
	Teacher    string `json:"teacher"`
}

// y2WeekSchedule 一周的课表：每行 course_id + sub_id + sub_status
func y2WeekSchedule(jwt string, userId int, startAt, endAt string) ([]y2WeekRow, error) {
	body, err := y2GetJSON(jwt, "/courseapi/v2/schedule/get-week-schedules", y2Params{
		y2P("user_id", userId),
		y2P("tenant_id", y2Tenant),
		y2P("start_at", startAt),
		y2P("end_at", endAt),
	})
	if err != nil {
		return nil, err
	}
	return y2ParseWeekSchedule(body), nil
}

// y2ParseWeekSchedule 只做解析
func y2ParseWeekSchedule(body any) []y2WeekRow {
	b := jobj(body)
	result := jobj(y2First(j2get(b, "result"), j2get(b, "data")))
	if result == nil {
		result = map[string]any{}
	}
	days := y2First(result["list"], result["schedule"])

	out := []y2WeekRow{}
	for _, d := range y2Arr(days) {
		day := jobj(d)
		if day == nil {
			continue
		}
		rows := y2First(day["course"], day["list"])
		for _, rv := range y2Arr(rows) {
			row := jobj(rv)
			if row == nil {
				continue
			}
			subID := y2PickStr(row, "id", "sub_id")
			courseID := y2PickStr(row, "course_id")
			if subID == "" || courseID == "" {
				continue
			}
			date := y2PickStr(day, "day")
			if date == "" {
				s := y2PickStr(row, "start_at")
				if len(s) > 10 {
					s = s[:10]
				}
				date = s
			}
			dur := y2Num(row["duration"])
			if dur == 0 {
				dur = y2Num(row["sub_duration"])
			}
			status := y2PickStr(row, "sub_status")
			if status == "" {
				status = y2PickStr(row, "status")
			}
			out = append(out, y2WeekRow{
				SubID:      subID,
				CourseID:   courseID,
				Title:      y2PickStr(row, "sub_title", "title"),
				CourseName: y2PickStr(row, "course_title", "course_name"),
				Date:       date,
				StartAt:    y2TsOf(row["start_at"]),
				EndAt:      y2TsOf(row["end_at"]),
				Duration:   dur,
				Status:     status,
				Room:       y2PickStr(row, "room_name"),
				Teacher:    y2PickStr(row, "lecturer_name"),
			})
		}
	}
	return out
}

// y2ScheduleRow 课表讲次（scheduleMap 的值）
type y2ScheduleRow struct {
	SubID    string
	Title    string
	StartAt  int64
	Duration int
	Status   string
	Room     string
	Teacher  string
	Date     string
	TermID   string
	TermName string
}

// y2ScheduleMap 把自己课表里近 N 天的所有讲次抓成一个 Map（courseId -> 讲次[]）。
//
// 这是"讲次直读"失败时的救命通道：课表接口是用户自己的数据，一定给，
// 而且每行就带 sub_status 和 room_name —— 正好是判定有没有录像、教室在哪要的东西。
// 一次覆盖所有课程，比按课程扫便宜得多。
func y2ScheduleMap(jwt string, userId, days int) (map[string][]y2ScheduleRow, error) {
	if days <= 0 {
		days = 150
	}
	now := time.Now()
	to := now.Add(24 * time.Hour)
	from := now.Add(-time.Duration(days) * 24 * time.Hour)

	collect := func(span int) ([]y2WeekRow, bool, error) {
		rows := []y2WeekRow{}
		anyData := false
		for t := from; t.Before(to); t = t.Add(time.Duration(span) * 24 * time.Hour) {
			end := t.Add(time.Duration(span-1) * 24 * time.Hour)
			if end.After(to) {
				end = to
			}
			list, err := y2WeekSchedule(jwt, userId, y2Ymd(t), y2Ymd(end))
			if err != nil {
				if y2IsAuthRejected(err.Error()) {
					return nil, anyData, err
				}
				continue
			}
			if len(list) > 0 {
				anyData = true
			}
			rows = append(rows, list...)
		}
		return rows, anyData, nil
	}

	// 先试 30 天一块（一般接口都吃），拿不到日期再用 7 天；两者都没数据就返回空表
	rows, anyData, err := collect(30)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		w, wAny, err2 := collect(7)
		if err2 != nil {
			return nil, err2
		}
		if len(w) > 0 {
			rows = w
		} else if !anyData && !wAny {
			return map[string][]y2ScheduleRow{}, nil
		}
	}

	out := map[string][]y2ScheduleRow{}
	for _, r := range rows {
		if y2IsLiveStatus(r.Status) {
			continue
		}
		list := out[r.CourseID]
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
		out[r.CourseID] = append(list, y2ScheduleRow{
			SubID: r.SubID, Title: r.Title, StartAt: r.StartAt, Duration: r.Duration,
			Status: r.Status, Room: r.Room, Teacher: r.Teacher, Date: r.Date,
			TermID: "", TermName: "",
		})
	}
	for _, list := range out {
		sort.SliceStable(list, func(i, j int) bool { return list[i].StartAt > list[j].StartAt })
	}
	return out, nil
}

// ---------------------------------------------------------------- 讲次

// y2Session 内部统一的一条讲次（讲次直读 / 课表扫描 / 自己的课表三种来源都归到这里）。
// JSON 标签按"讲次直读"那套字段写（Node 的 mapSub 就是这个字段集和顺序），
// 因为 /diag 的抽检会把讲次原样吐出来。
type y2Session struct {
	SubID    string `json:"subId"`
	Title    string `json:"title"`
	StartAt  int64  `json:"startAt"`
	Duration int    `json:"duration"`
	Status   string `json:"status"`
	Room     string `json:"room"`
	Teacher  string `json:"teacher"`
	Type     string `json:"type"`
	Kind     string `json:"kind"`
	TermID   string `json:"termId"`
	TermName string `json:"termName"`
	Date     string `json:"date"`
	// 下面这些只有"扫课表"那条来源才有，正常不对外输出
	SubTitle string `json:"subTitle,omitempty"`
	Period   string `json:"period,omitempty"`
	CourseID string `json:"courseId,omitempty"`
	EndAt    int64  `json:"endAt,omitempty"`
	Drag     string `json:"drag,omitempty"`
}

// y2MapSub 讲次直读里的一条 → y2Session
func y2MapSub(courseID string, s map[string]any) y2Session {
	startRaw := y2Pick(s, "start_at", "start_time", "begin_at", "start_date", "date")
	typ := y2PickStr(s, "type", "sub_type")
	return y2Session{
		SubID:    y2PickStr(s, "id", "sub_id", "subject_id"),
		Title:    y2PickStr(s, "title", "sub_title", "name"),
		StartAt:  y2TsOf(startRaw),
		Duration: y2Num(y2Pick(s, "duration", "sub_duration", "length")),
		Status:   y2PickStr(s, "sub_status", "status", "live_status"),
		Room:     y2PickStr(s, "room_name", "sub_room_name", "classroom", "class_room", "room", "roomName"),
		Teacher:  y2PickStr(s, "lecturer_name", "teacher_name", "realname"),
		Type:     typ,
		Kind:     y2KindOfSub(typ),
		TermID:   y2PickStr(s, "term", "term_id", "termId"),
		TermName: y2PickStr(s, "term_name", "termName"),
		Date:     y2DateOf(startRaw),
	}
}

// y2Stream 一路画面
type y2Stream struct {
	Type   string `json:"type"`
	Format string `json:"format"`
	URL    string `json:"url"`
}

// y2SubInfo 一讲的可播放信息（/api/y2/sub 直接把它塞进 info 字段）
type y2SubInfo struct {
	Status   string     `json:"status"`
	Kind     string     `json:"kind"`
	Title    string     `json:"title"`
	SubTitle string     `json:"subTitle"`
	Teacher  string     `json:"teacher"`
	Room     string     `json:"room"`
	StartAt  int64      `json:"startAt"`
	EndAt    int64      `json:"endAt"`
	Duration int        `json:"duration"`
	Streams  []y2Stream `json:"streams"`
	Audio    string     `json:"audio"`
}

// y2SubInfoOf 一讲的取流信息（对应 yanhe2.js 的 subInfo）
func y2SubInfoOf(jwt, courseID, subID string) (*y2SubInfo, error) {
	body, err := y2GetJSON(jwt, "/courseapi/v3/portal-home-setting/get-sub-info", y2Params{
		y2P("course_id", courseID),
		y2P("sub_id", subID),
	})
	if err != nil {
		return nil, err
	}
	return y2ParseSubInfo(body)
}

// y2ParseSubInfo 只做解析（测试里用固定 JSON 直接调它）
func y2ParseSubInfo(body any) (*y2SubInfo, error) {
	b := jobj(body)
	if b == nil || !codeIsZeroStrict(b) || jobj(b["data"]) == nil {
		return nil, errors.New("get-sub-info 失败")
	}
	d := jobj(b["data"])

	info := &y2SubInfo{
		Status:   orStr(y2StatusName[y2PickStr(d, "sub_status")], "unknown"),
		Kind:     y2KindOfSub(y2Pick(d, "type", "sub_type")),
		Title:    y2Text(d["course_title"]),
		SubTitle: y2Text(d["sub_title"]),
		Teacher:  y2Text(d["lecturer_name"]),
		Room:     y2PickStr(d, "room_name", "sub_room_name", "classroom", "class_room", "room"),
		StartAt:  y2TsOf(d["start_at"]),
		EndAt:    y2TsOf(d["end_at"]),
		Duration: y2Num(d["duration"]),
		Streams:  []y2Stream{},
	}

	// 画面：type 是数字编码（3 教师 / 2 课件 / 4 教室），同类型只留第一路
	for _, it := range y2Arr(d["video_list"]) {
		item := jobj(it)
		if item == nil {
			continue
		}
		typ := y2RecordedType[y2PickStr(item, "type")]
		url := y2PickStr(item, "preview_url")
		if typ == "" || url == "" {
			continue
		}
		low := strings.ToLower(strings.Split(url, "?")[0])
		format := ""
		switch {
		case strings.HasSuffix(low, ".m3u8"):
			format = "hls"
		case strings.HasSuffix(low, ".mp4"):
			format = "mp4"
		case strings.Contains(url, "/play/") || strings.Contains(url, "/live/"):
			format = "hls"
		}
		if format == "" {
			continue
		}
		dup := false
		for _, s := range info.Streams {
			if s.Type == typ {
				dup = true
				break
			}
		}
		if !dup {
			info.Streams = append(info.Streams, y2Stream{Type: typ, Format: format, URL: url})
		}
	}
	streamOrder := map[string]int{"camera": 0, "screen": 1, "room": 2}
	sort.SliceStable(info.Streams, func(i, j int) bool {
		return streamOrder[info.Streams[i].Type] < streamOrder[info.Streams[j].Type]
	})

	// 音频：只要 .aac
	for _, it := range y2Arr(d["audio_list"]) {
		item := jobj(it)
		if item == nil {
			continue
		}
		u := y2Text(item["url"])
		if u == "" {
			continue
		}
		if strings.HasSuffix(strings.ToLower(strings.Split(u, "?")[0]), ".aac") {
			info.Audio = u
			break
		}
	}
	return info, nil
}

// ---------------------------------------------------------------- 课程检索

const (
	y2CourseListPath  = "/courseapi/v3/multi-search/get-course-list"
	y2DetailPathConst = "/courseapi/v3/multi-search/get-course-detail"
)

// y2NormCourse 上游课程对象 → 统一条目。
// 用 map 而不是 struct：后面 y2Videos / y2Verify 要往条目上"贴"额外字段
// （videoCount / room / verified / source / note），Node 用的是对象展开，
// 用 map 才等价 —— 字段集合本来就随来源不同而不同。
func y2NormCourse(c map[string]any) y2Course {
	id := y2PickStr(c, "id", "course_id", "Id", "courseId")
	if id == "" {
		return nil
	}
	teachers := []string{}
	for _, t := range y2Arr(c["teachers"]) {
		if s := y2PickStr(jobj(t), "realname", "name"); s != "" {
			teachers = append(teachers, s)
		}
	}
	teacher := strings.Join(teachers, "、")
	if teacher == "" {
		teacher = y2PickStr(c, "realname", "teacher", "lecturer_name", "Teacher")
	}
	kind := "record"
	if y2KindOfSub(y2Pick(c, "type", "course_type", "sub_type")) == "live" {
		kind = "live"
	}
	return y2Course{
		"courseId": id,
		"title":    y2PickStr(c, "title", "course_title", "Title", "name"),
		"teacher":  teacher,
		"termId":   y2PickStr(c, "term", "term_id", "termId"),
		"termName": y2PickStr(c, "term_name", "termName", "TermName"),
		"kind":     kind,
		// 接口没给节次数时用 null，别和"确实是 0 讲"混在一起
		"sessions":   y2SessionsCount(y2Pick(c, "sub_count", "subject_count", "num")),
		"cover":      y2PickStr(c, "thumb", "Thumb", "cover"),
		"room":       "",
		"videoCount": nil,
		"verified":   false,
	}
}

// y2SessionsCount 节次数：没给就 null，给了就按 num() 取整
func y2SessionsCount(raw any) any {
	if raw == nil {
		return nil
	}
	if s, ok := raw.(string); ok && strings.TrimSpace(s) == "" {
		return nil
	}
	return y2Num(raw)
}

// y2CodeOK 上游的 code 是不是"成功"（0 / 200 / 1000）。
// 注意 Node 用的是 !==，所以字符串 "0" 也算失败 —— 这里只认 JSON 数字。
func y2CodeOK(v any) bool {
	f, ok := v.(float64)
	if !ok {
		return false
	}
	return f == 0 || f == 200 || f == 1000
}

// y2ParseCourseList 只做解析（searchCourses 的变体探测用它）
func y2ParseCourseList(body any) *y2SearchResult {
	b := jobj(body)
	if b == nil || !y2CodeOK(b["code"]) {
		return &y2SearchResult{courses: []y2Course{}}
	}
	rawData := j2get(b, "data")
	data := jobj(rawData)
	if data == nil {
		data = map[string]any{}
	}
	list := y2Arr(y2First(data["list"], data["data"], data["rows"], rawData))
	courses := []y2Course{}
	for _, it := range list {
		if n := y2NormCourse(jobj(it)); n != nil {
			courses = append(courses, n)
		}
	}
	total := y2Num(data["total"])
	if total == 0 {
		total = y2Num(b["total"])
	}
	if total == 0 {
		total = len(courses)
	}
	return &y2SearchResult{courses: courses, total: total}
}

// y2SearchResult 课程检索结果
type y2SearchResult struct {
	courses []y2Course
	total   int
}

// y2SearchCourses 按课程名检索（覆盖平台上所有学期，不只是"我的课程"）。
// 参数名在各版本部署里不一致，这里一次试几种写法，成功即记住。
// y2SearchByTeacher 按老师名直接查（新版接口只吃课程名，这里探几种参数名）。
// 每种都严格过滤：只留课名或老师里真的包含这个名字的结果。
func y2SearchByTeacher(jwt, name, term string) []y2Course {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return nil
	}
	variants := []y2Param{
		y2P("teacher_name", name), y2P("teacher", name), y2P("lecturer_name", name),
		y2P("realname", name), y2P("keyword", name), y2P("q", name),
	}
	for _, v := range variants {
		body, err := y2GetJSON(jwt, y2CourseListPath, y2Params{
			y2P("term", term), y2P("page", 1), y2P("per_page", 40), y2P("tenant", y2Tenant), v,
		})
		if err != nil {
			continue
		}
		r := y2ParseCourseList(body)
		hit := []y2Course{}
		for _, c := range r.courses {
			if y2FuzzyHas(jstr(c["title"])+" "+jstr(c["teacher"]), name) {
				hit = append(hit, c)
			}
		}
		if len(hit) > 0 {
			return hit
		}
	}
	return nil
}

// y2SearchCourses 按课程名检索（覆盖平台上所有学期，不只是"我的课程"）。
//
// 上游几种写法都试一遍，但**不能"有返回就当命中"**：
//   - like_title 如果被忽略，接口会甩一整页无关课程回来；
//   - title 是精确/前缀匹配，搜「大学物理」时只回「大学物理」本身。
//
// 所以每种写法都先在本地按词过滤（Ⅱ/II/2、全角/半角、空格做归一化），
// 过滤后还有剩才算这条写法可用；全都不行才返回空，交给上层用课程库展开。
func y2SearchCourses(jwt, q, term string, page, perPage int) (*y2SearchResult, error) {
	base := func(extra ...y2Param) y2Params {
		p := y2Params{
			y2P("term", term),
			y2P("page", page),
			y2P("per_page", perPage),
			y2P("tenant", y2Tenant),
		}
		return append(p, extra...)
	}
	tries := []y2Try{
		// 先试包含匹配：上游要是认这个参数，一次就能把「大学物理AⅡ」带出来
		{name: "like_title", path: y2CourseListPath, params: base(y2P("like_title", q))},
		{name: "title", path: y2CourseListPath, params: base(
			y2P("title", q), y2P("order", ""), y2P("publish", ""), y2P("struc_ids", []any{}))},
		{name: "title-plain", path: y2CourseListPath, params: base(y2P("title", q))},
		{name: "title-no-term", path: y2CourseListPath, params: y2Params{
			y2P("page", page), y2P("per_page", perPage), y2P("tenant", y2Tenant), y2P("title", q)}},
	}
	tokens := strings.Fields(q)
	got, _, err := y2Probe(jwt, tries, func(body any) (*y2SearchResult, bool) {
		r := y2ParseCourseList(body)
		if len(r.courses) == 0 {
			return r, false
		}
		kept := []y2Course{}
		for _, c := range r.courses {
			if y2FuzzyAll(jstr(c["title"])+" "+jstr(c["teacher"]), tokens) {
				kept = append(kept, c)
			}
		}
		if len(kept) == 0 {
			return r, false // 上游忽略了关键词（或只给了不相干的课）→ 换下一种写法
		}
		r.courses, r.total = kept, len(kept)
		return r, true
	})
	if err != nil {
		return nil, err
	}
	if got == nil {
		got = &y2SearchResult{}
	}
	// 对应 Node 的 `courses: got.courses || []`
	if got.courses == nil {
		got.courses = []y2Course{}
	}
	return got, nil
}

// ---------------------------------------------------------------- 课程详情

// y2Detail 讲次直读的结果
type y2Detail struct {
	courseID string
	title    string
	termName string
	sessions []y2Session
}

// y2LooksLikeSub 看起来像不像一个讲次对象
func y2LooksLikeSub(v any) bool {
	o := jobj(v)
	if o == nil {
		return false
	}
	hasID := y2HasKey(o, "id", "sub_id", "subject_id")
	hasSub := y2HasKey(o, "type", "sub_type", "sub_status", "start_at", "start_time", "sub_title")
	return hasID && hasSub
}

// y2DeepSubArray 在任意嵌套结构里找讲次数组（数组里多数元素像讲次就算）。
// Node 按 JSON 的插入顺序遍历对象键，Go 的 map 无序 → 这里按键名排序，
// 至少保证同一份数据每次结果一样（歧义结构的取舍见 README 的已知差异）。
func y2DeepSubArray(root any) []any {
	var found []any
	var walk func(o any, depth int)
	walk = func(o any, depth int) {
		if o == nil || found != nil || depth > 5 {
			return
		}
		if s, ok := o.(string); ok {
			t := strings.TrimSpace(s)
			if (strings.HasPrefix(t, "[") || strings.HasPrefix(t, "{")) && len(t) < 500000 {
				var v any
				if json.Unmarshal([]byte(t), &v) == nil {
					walk(v, depth+1)
				}
			}
			return
		}
		if a, ok := o.([]any); ok {
			if len(a) > 0 {
				n := 0
				for _, x := range a {
					if y2LooksLikeSub(x) {
						n++
					}
				}
				need := (len(a)*6 + 9) / 10 // ceil(len*0.6)
				if need < 1 {
					need = 1
				}
				if n >= need {
					found = a
					return
				}
			}
			for _, x := range a {
				walk(x, depth+1)
				if found != nil {
					return
				}
			}
			return
		}
		if m, ok := o.(map[string]any); ok {
			for _, k := range y2Keys(m) {
				walk(m[k], depth+1)
				if found != nil {
					return
				}
			}
		}
	}
	walk(root, 0)
	return found
}

// y2SubListFlatten 把"按章节分组的 sub_list 对象"摊平成一个数组。
// 分组键是日期（2026.9.4），按 key 排序后再拼，保证同一份数据每次顺序一致。
func y2SubListFlatten(v any) []any {
	m := jobj(v)
	if m == nil {
		return nil
	}
	out := []any{}
	for _, k := range y2Keys(m) {
		out = append(out, y2Arr(m[k])...)
	}
	return out
}

// y2ParseDetail 解析 get-course-detail / get-course-info 的返回；不可用返回 nil
func y2ParseDetail(courseID string, body any) *y2Detail {
	b := jobj(body)
	if b == nil {
		return nil
	}
	// code 字段存在时必须是 0 / 200 / 1000（不存在就不管）
	if c, ok := b["code"]; ok && !y2CodeOK(c) {
		return nil
	}
	d := jobj(b["data"])
	if d == nil {
		d = b
	}
	var raw []any
	if a := jarr(d["sub_list"]); len(a) > 0 {
		raw = a
	} else if a := y2SubListFlatten(d["sub_list"]); len(a) > 0 {
		// get-course-detail 的 sub_list 是按章节分组的对象：
		//   {"2026.9.4":[{...}], "2026.10.1":[{...},{...}]}
		// 直接取第一组只会拿到 1 讲，这就是"5 讲只显示 1 个视频"的来源。
		raw = a
	} else if a := jarr(d["origin_sub_list"]); len(a) > 0 {
		raw = a
	} else {
		raw = y2DeepSubArray(d)
	}
	sessions := []y2Session{}
	for _, it := range raw {
		if s := jobj(it); s != nil {
			if m := y2MapSub(courseID, s); m.SubID != "" {
				sessions = append(sessions, m)
			}
		}
	}
	return &y2Detail{
		courseID: courseID,
		title:    y2PickStr(d, "title", "course_title"),
		termName: y2PickStr(d, "term_name", "termName"),
		sessions: sessions,
	}
}

// y2CourseDetail 讲次直读：get-course-detail 一次给全量 sub_list。
//
// 两个关键点：
//  1. "接口通了但一条讲次都没有"是**合法结果**（这门课就是没录像），不能当失败
//  2. 各版本把讲次放在哪不好猜，所以除了 sub_list / origin_sub_list，
//     还会在 data 里递归找"看起来像讲次"的数组，甚至解开 JSON 字符串里的
func y2CourseDetail(jwt, courseID, account string) (*y2Detail, error) {
	tries := []y2Try{
		// 门户课程页用的就是它：sub_list 是**平铺**的完整讲次表（实测 22 讲、10 讲），
		// 而 multi-search 的 sub_list 按章节分组，只取第一组就会少算成 1 讲。
		{name: "portal-course-info", path: "/courseapi/v3/portal-home-setting/get-course-info", params: y2Params{
			y2P("course_id", courseID), y2P("student", account)}},
		{name: "multi-search+student", path: y2DetailPathConst, params: y2Params{
			y2P("course_id", courseID), y2P("student", account)}},
		{name: "multi-search", path: y2DetailPathConst, params: y2Params{y2P("course_id", courseID)}},
		{name: "person/course-api", path: "/vlabpassportapi/person/course-api/get-course-detail", params: y2Params{
			y2P("course_id", courseID)}},
	}
	got, _, err := y2Probe(jwt, tries, func(body any) (*y2Detail, bool) {
		// 解析成功就算数，哪怕 0 条讲次（0 条 = 这门课没录像）
		r := y2ParseDetail(courseID, body)
		return r, r != nil
	})
	if err != nil {
		return nil, err
	}
	if got == nil {
		return nil, errors.New("get-course-detail 没返回可用数据")
	}
	return got, nil
}

// y2ProbeCap 一门课最多逐讲探多少讲（超过就按状态列出，避免被限流）
const y2ProbeCap = 12

// y2CourseVideosResult y2CourseVideos 的结果
type y2CourseVideosResult struct {
	courseID      string
	title         string
	termName      string
	sessions      []y2Session
	// candidates 所有"像录像"的讲次（含没取到流的）。
	// 点开课程时要展示完整讲次表，不能只给探过流的那几讲。
	candidates    []y2Session
	videoCount    int
	totalSessions int
	room          string
	latestDate    string
	probed        bool
	verified      bool
	countExact    bool // videoCount 是不是准数（只抽了几讲验的时候不是）
}

// y2CourseVideos 一门课"到底有没有能看的录像"，以及这些录像的讲次列表。
//
// 判定顺序：
//  1. 有 sub_status=6 的 → 逐个真取一次视频流，取到才算（状态不可靠，必须以流为准）
//  2. 只有 5 / 未知 状态的 → 同样逐讲验流
//  3. 只有 1/2/3（直播中/未开始/处理中）→ 没有录像
//
// 另外补一次教室名：讲次自带就用，没带就找 get-sub-info 要（只问最新一讲）。
func y2CourseVideos(jwt, courseID, account string) (*y2CourseVideosResult, error) {
	d, err := y2CourseDetail(jwt, courseID, account)
	if err != nil {
		return nil, err
	}
	all := append([]y2Session{}, d.sessions...)
	sort.SliceStable(all, func(i, j int) bool { return all[i].StartAt > all[j].StartAt })

	// 候选 = 所有"不像直播中/未开始/处理中"的讲次。
	// 不能只看 status=6：实测常有一门课 5 讲里只有 1 讲是 6、其余是 5（已结束），
	// 只看 6 就会把另外 4 讲（其实也有录像）漏掉，卡片写 5 讲、点进去只剩 1 讲。
	candidates := []y2Session{}
	for _, s := range all {
		if y2IsRecordingStatus(s.Status) || y2MaybeRecording(s.Status) {
			candidates = append(candidates, s)
		}
	}
	probed := len(candidates) > 0

	sessions := []y2Session{}
	verified := true
	// 逐讲验流很贵：一门课 12 讲就是十几次请求，几十门课一起验会被限流，
	// 用户也要等一分钟。判断"有没有录像"抽 4 讲就够；讲次 ≤ 4 的课才是准数。
	probeLimit := 0
	if len(candidates) > 3 {
		probeLimit = 3
	}
	countExact := probeLimit == 0
	if len(candidates) > 0 {
		playable, failed, n := y2VerifySessions(jwt, courseID, candidates, probeLimit, 6)
		sessions = playable
		// 全部请求都失败（网络/权限）→ 退回状态判断，但标记未验证
		if len(sessions) == 0 && failed > 0 && failed == n {
			sessions = candidates
			verified = false
		}
	}

	// 教室：先用讲次自带的，没有就问 get-sub-info（只问最新一讲）
	room := ""
	if len(sessions) > 0 {
		room = sessions[0].Room
	}
	if room == "" && len(sessions) > 0 {
		if info, err := y2SubInfoOf(jwt, courseID, sessions[0].SubID); err == nil && info != nil {
			room = info.Room
		}
	}

	latest := ""
	if len(sessions) > 0 {
		latest = sessions[0].Date
	}
	return &y2CourseVideosResult{
		courseID:      courseID,
		title:         d.title,
		termName:      d.termName,
		sessions:      sessions,
		candidates:    candidates,
		videoCount:    len(sessions),
		totalSessions: len(all),
		room:          room,
		latestDate:    latest,
		probed:        probed,
		verified:      verified,
		countExact:    countExact,
	}, nil
}

// y2FilterRecordings 过滤出能播的讲次（给扫课表那条兜底路径用）
func y2FilterRecordings(list []y2Session) []y2Session {
	out := []y2Session{}
	for _, s := range list {
		if y2IsLiveStatus(s.Status) {
			continue
		}
		if !y2IsRecordingStatus(s.Status) && !y2MaybeRecording(s.Status) {
			continue
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartAt > out[j].StartAt })
	return out
}
