// 内置课程库（catalog.json.gz）+ 老平台学期/检索接口 + 各种学期与教室的小工具
//
// 对应 server.js 里 loadCatalog / catalogSearch / catalogByTeacher /
// parseTermName / courseInTerm / oldSemesters / oldSearch / lookupCourse /
// oldCourseRow / roomName / looksLikeRoomCode / termKeyOf / termEndDate / termIsRecent
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------- 课程条目

// courseRow 是搜索结果里的一门课。
//
// 地址栏里那些"可能没有"的字段用指针 + omitempty，是为了和 Node 版对齐：
//   - 从线上接口来的行，nameEn/code/.../rooms 全都有（哪怕是空字符串也会输出）
//   - 从内置课程库来的行，只有 id/name/teacher/term/room/source
type courseRow struct {
	ID           string             `json:"id"`
	Name         string             `json:"name"`
	NameEn       *string            `json:"nameEn,omitempty"`
	Code         *string            `json:"code,omitempty"`
	Number       *string            `json:"number,omitempty"`
	Teacher      string             `json:"teacher"`
	Term         string             `json:"term"`
	Room         string             `json:"room"`
	Rooms        *[]string          `json:"rooms,omitempty"`
	RoomByNumber *map[string]string `json:"roomByNumber,omitempty"`
	College      *string            `json:"college,omitempty"`
	Participants *int               `json:"participants,omitempty"`
	Source       string             `json:"source"`
}

func strPtr(s string) *string { return &s }
func intPtr(n int) *int       { return &n }

// ---------------------------------------------------------------- 课程库加载

type catRow struct {
	n    int // names 下标
	t    int // teachers 下标
	term int // terms 下标
	id   string
}

type catalogData struct {
	names    []string
	teachers []string
	terms    []string
	rows     []catRow
	meta     map[string]any
	byID     map[string]*catRow
	// 预先把小写形式算好，搜索时不用每行都转一次
	nameLow    []string
	teacherLow []string
	err        string
}

var (
	catOnce  sync.Once
	catCache *catalogData
)

// catRowIn 是 catalog.json.gz 里的一行：[名字下标, 教师下标, 学期下标, 课程ID]
type catRowIn []json.RawMessage

func toIndex(raw json.RawMessage) int {
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return int(f)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		var n int
		fmt.Sscanf(s, "%d", &n)
		return n
	}
	return 0
}

func toIDString(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		// 课程 ID 都是整数，用定点格式避免出现 7.212e+04
		return fmt.Sprintf("%.0f", f)
	}
	return ""
}

// loadCatalog 读一次就缓存（578KB gzip，79k 门课）
func loadCatalog() *catalogData {
	catOnce.Do(func() {
		catCache = readCatalog()
	})
	return catCache
}

func readCatalog() *catalogData {
	cat := &catalogData{meta: map[string]any{}, byID: map[string]*catRow{}}

	raw, err := webFS.ReadFile("web/catalog.json.gz")
	if err != nil {
		cat.err = err.Error()
		fmt.Println("  [warn] 课程库加载失败: " + err.Error())
		return cat
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		cat.err = err.Error()
		fmt.Println("  [warn] 课程库加载失败: " + err.Error())
		return cat
	}
	defer zr.Close()

	// 整份 JSON 只有几 MB，先读进来再逐行解析
	body, err := io.ReadAll(zr)
	if err != nil {
		cat.err = err.Error()
		fmt.Println("  [warn] 课程库加载失败: " + err.Error())
		return cat
	}

	var file struct {
		Names    []string          `json:"names"`
		Teachers []string          `json:"teachers"`
		Terms    []string          `json:"terms"`
		Rows     []json.RawMessage `json:"rows"`
		Meta     map[string]any    `json:"meta"`
	}
	if err := json.Unmarshal(body, &file); err != nil {
		cat.err = err.Error()
		fmt.Println("  [warn] 课程库加载失败: " + err.Error())
		return cat
	}

	cat.names = file.Names
	cat.teachers = file.Teachers
	cat.terms = file.Terms
	if file.Meta != nil {
		cat.meta = file.Meta
	}
	cat.nameLow = make([]string, len(cat.names))
	for i, s := range cat.names {
		cat.nameLow[i] = strings.ToLower(s)
	}
	cat.teacherLow = make([]string, len(cat.teachers))
	for i, s := range cat.teachers {
		cat.teacherLow[i] = strings.ToLower(s)
	}

	cat.rows = make([]catRow, 0, len(file.Rows))
	for _, line := range file.Rows {
		var arr catRowIn
		if err := json.Unmarshal(line, &arr); err != nil || len(arr) < 4 {
			continue
		}
		r := catRow{n: toIndex(arr[0]), t: toIndex(arr[1]), term: toIndex(arr[2]), id: toIDString(arr[3])}
		cat.rows = append(cat.rows, r)
		// 容量已经按行数预分配，取地址不会失效
		cat.byID[r.id] = &cat.rows[len(cat.rows)-1]
	}
	return cat
}

// catName 取名字（下标越界当年 Node 的 `|| ”` 处理）
func (c *catalogData) catName(i int) string {
	if i >= 0 && i < len(c.names) {
		return c.names[i]
	}
	return ""
}
func (c *catalogData) catTeacher(i int) string {
	if i >= 0 && i < len(c.teachers) {
		return c.teachers[i]
	}
	return ""
}
func (c *catalogData) catTerm(i int) string {
	if i >= 0 && i < len(c.terms) {
		return c.terms[i]
	}
	return ""
}
func (c *catalogData) nameLower(i int) string {
	if i >= 0 && i < len(c.nameLow) {
		return c.nameLow[i]
	}
	return ""
}
func (c *catalogData) teacherLower(i int) string {
	if i >= 0 && i < len(c.teacherLow) {
		return c.teacherLow[i]
	}
	return ""
}

// noSpaceLower 对应 JS 的 `String(s).replace(/\s+/g,”).toLowerCase()`
func noSpaceLower(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f' || r == 0x00a0 || r == 0x3000 || r == 0x200b {
			continue
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}

// catalogSearch 在课程库里搜（课程名或教师名包含关键词），可按学期过滤
func catalogSearch(q, term string, limit int) ([]catalogCourse, int) {
	cat := loadCatalog()
	key := noSpaceLower(q)
	out := []catalogCourse{}
	total := 0
	for _, r := range cat.rows {
		name := cat.catName(r.n)
		teacher := cat.catTeacher(r.t)
		rterm := cat.catTerm(r.term)
		if term != "" && rterm != term {
			continue
		}
		if key != "" {
			if !strings.Contains(cat.nameLower(r.n), key) && !strings.Contains(cat.teacherLower(r.t), key) {
				continue
			}
		}
		total++
		if len(out) < limit {
			out = append(out, catalogCourse{ID: r.id, Name: name, Teacher: teacher, Term: rterm})
		}
	}
	return out, total
}

type catalogCourse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Teacher string `json:"teacher"`
	Term    string `json:"term"`
}

// asRow 把课程库条目变成统一的课程行（Node 版是 {...mc, room:”, source:'catalog'}）
func (c catalogCourse) asRow() courseRow {
	return courseRow{ID: c.ID, Name: c.Name, Teacher: c.Teacher, Term: c.Term, Room: "", Source: "catalog"}
}

// catalogByTeacher 只看教师名（课程库离线用）
func catalogByTeacher(name string, limit int) []catalogCourse {
	cat := loadCatalog()
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return nil
	}
	seen := map[string]bool{}
	out := []catalogCourse{}
	for _, r := range cat.rows {
		if !strings.Contains(cat.teacherLower(r.t), key) {
			continue
		}
		if seen[r.id] {
			continue
		}
		seen[r.id] = true
		out = append(out, catalogCourse{ID: r.id, Name: cat.catName(r.n), Teacher: cat.catTeacher(r.t), Term: cat.catTerm(r.term)})
		if len(out) >= limit {
			break
		}
	}
	return out
}

// ---------------------------------------------------------------- 学期工具

var termYearRe = regexp.MustCompile(`(20\d{2})\s*[-—~]\s*(20\d{2})`)

// parseTermName 学期名 → (学年, 第几学期)。和 Node 版一样，认不出就 ok=false
func parseTermName(name string) (year string, sem int, ok bool) {
	m := termYearRe.FindStringSubmatch(name)
	if m == nil {
		return "", 0, false
	}
	switch {
	case strings.Contains(name, "第三") || strings.Contains(name, "3学期"):
		sem = 3
	case strings.Contains(name, "第二") || strings.Contains(name, "2学期"):
		sem = 2
	case strings.Contains(name, "第一") || strings.Contains(name, "1学期"):
		sem = 1
	}
	return m[1] + "-" + m[2], sem, true
}

// courseInTerm 这门课是不是这个学期的
func courseInTerm(c map[string]any, wantYear string, wantSem int) bool {
	if wantYear == "" && wantSem == 0 {
		return true
	}
	y := strings.TrimSpace(jstr(c["school_year"]))
	if y != "" && y != wantYear {
		return false
	}
	s := jint(c["semester"])
	if wantSem != 0 && s != 0 && s != wantSem {
		return false
	}
	return true
}

// tagItem 学期表里的一项
type tagItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Sort int    `json:"sort"`
}

var (
	tagMu    sync.Mutex
	tagCache []tagItem
	tagAt    time.Time
)

// oldSemesters 学期表：GET /v1/tag/list?with_sub=true → param==='semesters' 的那一组
func oldSemesters(token string) ([]tagItem, error) {
	tagMu.Lock()
	if tagCache != nil && time.Since(tagAt) < time.Hour {
		list := tagCache
		tagMu.Unlock()
		return list, nil
	}
	tagMu.Unlock()

	j, err := apiCall("/v1/tag/list?with_sub=true", token)
	if err != nil {
		return nil, err
	}
	var list []tagItem
	for _, t := range jarr(j["data"]) {
		o := jobj(t)
		if o == nil || jstr(o["param"]) != "semesters" {
			continue
		}
		for _, ch := range jarr(o["children"]) {
			c := jobj(ch)
			if c == nil {
				continue
			}
			name := strings.TrimSpace(jstr(c["name"]))
			if name == "" {
				continue
			}
			list = append(list, tagItem{ID: jstr(c["id"]), Name: name, Sort: jint(c["sort"])})
		}
		break // Node 用 find，只认第一个 semesters 组
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Sort > list[j].Sort })
	// 只有真的拿到了才缓存，否则下次登录后再试
	if len(list) > 0 {
		tagMu.Lock()
		tagCache = list
		tagAt = time.Now()
		tagMu.Unlock()
	}
	return list, nil
}

// ---------------------------------------------------------------- 老平台检索

type oldSearchResult struct {
	list     []map[string]any
	total    int
	page     int
	lastPage int
}

// oldSearch 课程检索 /v2/course/list（带 classrooms 真教室名），匿名可用
func oldSearch(keyword string, semesterIDs []string, page, pageSize int) (*oldSearchResult, error) {
	v := url.Values{}
	for _, id := range semesterIDs {
		v.Add("semesters[]", id)
	}
	v.Set("page", fmt.Sprint(page))
	v.Set("page_size", fmt.Sprint(pageSize))
	if keyword != "" {
		v.Set("keyword", keyword)
	}

	j, err := apiCall("/v2/course/list?"+v.Encode(), "")
	if err != nil {
		return nil, err
	}
	if !codeIsZeroLoose(j) {
		msg := jstr(j["message"])
		if msg == "" {
			msg = "课程检索被拒绝"
		}
		return nil, fmt.Errorf("%s", msg)
	}

	d := jobj(j["data"])
	var list []map[string]any
	if d != nil {
		if a := jarr(d["data"]); a != nil {
			for _, it := range a {
				if m := jobj(it); m != nil {
					list = append(list, m)
				}
			}
		} else if a := jarr(d["list"]); a != nil {
			for _, it := range a {
				if m := jobj(it); m != nil {
					list = append(list, m)
				}
			}
		}
	}
	total := jint(d["total"])
	if total == 0 {
		total = len(list)
	}
	p := jint(d["current_page"])
	if p == 0 {
		p = page
	}
	last := jint(d["last_page"])
	if last == 0 {
		last = 1
	}
	return &oldSearchResult{list: list, total: total, page: p, lastPage: last}, nil
}

// lookupCourse 按课程编号反查（keyword=id），用来补齐 classrooms / 学期 / 教师
type lookupEntry struct {
	at time.Time
	c  map[string]any
}

var (
	lookupMu    sync.Mutex
	lookupCache = map[string]lookupEntry{}
)

func lookupCourse(id string) (map[string]any, error) {
	lookupMu.Lock()
	hit, ok := lookupCache[id]
	lookupMu.Unlock()
	if ok && time.Since(hit.at) < 10*time.Minute {
		return hit.c, nil
	}

	r, err := oldSearch(id, nil, 1, 5)
	if err != nil {
		return nil, err
	}
	var c map[string]any
	for _, x := range r.list {
		if jstr(x["id"]) == id {
			c = x
			break
		}
	}
	if c == nil && len(r.list) > 0 {
		c = r.list[0]
	}

	lookupMu.Lock()
	if len(lookupCache) > 500 {
		lookupCache = map[string]lookupEntry{}
	}
	lookupCache[id] = lookupEntry{at: time.Now(), c: c}
	lookupMu.Unlock()
	return c, nil
}

// oldCourseRow 老平台课程对象 → 统一条目
func oldCourseRow(c map[string]any) *courseRow {
	if c == nil {
		return nil
	}
	id := jstr(c["id"])
	if id == "" {
		return nil
	}

	// classrooms: [{id,name,number}] —— name 是真名，number 才是节次 location 里存的编码
	rooms := []string{}
	byNumber := map[string]string{}
	for _, it := range jarr(c["classrooms"]) {
		r := jobj(it)
		if r == nil {
			continue
		}
		name := jstr(r["name"])
		num := jstr(r["number"])
		if name == "" && num == "" {
			continue
		}
		if name != "" {
			rooms = append(rooms, name)
		}
		if num != "" && name != "" {
			byNumber[strings.ToUpper(strings.TrimSpace(num))] = name
		}
	}

	profs := []string{}
	for _, it := range jarr(c["professors"]) {
		if s, ok := it.(string); ok {
			if s != "" {
				profs = append(profs, s)
			}
			continue
		}
		p := jobj(it)
		if p == nil {
			continue
		}
		name := orStr(jstr(p["name"]), jstr(p["realname"]))
		if name != "" {
			profs = append(profs, name)
		}
	}

	y := jstr(c["school_year"])
	s := jint(c["semester"])
	term := ""
	if y != "" {
		cn := []string{"", "一", "二", "三"}
		semName := fmt.Sprint(s)
		if s >= 0 && s < len(cn) && cn[s] != "" {
			semName = cn[s]
		}
		term = y + " 第" + semName + "学期"
	}

	room := ""
	if len(rooms) > 0 {
		room = rooms[0]
	}
	nameEn := jstr(c["name_en"])
	code := jstr(c["code"])
	number := jstr(c["number"])
	college := jstr(c["college_name"])
	participants := jint(c["participant_count"])
	return &courseRow{
		ID:           id,
		Name:         strings.TrimSpace(orStr(jstr(c["name_zh"]), jstr(c["name"]))),
		NameEn:       &nameEn,
		Code:         &code,
		Number:       &number,
		Teacher:      strings.Join(profs, "、"),
		Term:         term,
		Room:         room,
		Rooms:        &rooms,
		RoomByNumber: &byNumber,
		College:      &college,
		Participants: &participants,
		Source:       "old-api",
	}
}

var roomCodeRe = regexp.MustCompile(`^[0-9A-Za-z_-]{5,}$`)

var (
	reHan    = regexp.MustCompile(`[\x{4e00}-\x{9fa5}]`)
	reSpace  = regexp.MustCompile(`\s`)
	reRoomKw = regexp.MustCompile(`楼|室|馆|教|厅|场|中心|实验`)
)

// looksLikeRoomCode 看起来像房间编码（280101004X）而不是教室名
func looksLikeRoomCode(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return true
	}
	if reHan.MatchString(t) {
		return false // 有中文 → 名字
	}
	if reSpace.MatchString(t) {
		return false
	}
	if reRoomKw.MatchString(t) {
		return false
	}
	return roomCodeRe.MatchString(t)
}

// roomName 节次里的 location 可能是编码，用课程自带的 classrooms 换成真名
func roomName(loc string, row *courseRow) string {
	t := strings.TrimSpace(loc)
	if t == "" {
		return ""
	}
	if row != nil && row.RoomByNumber != nil {
		if hit, ok := (*row.RoomByNumber)[strings.ToUpper(t)]; ok && hit != "" {
			return hit
		}
	}
	if looksLikeRoomCode(t) {
		return ""
	}
	return t
}

// ---------------------------------------------------------------- 学期的“新/旧”

var termKeyYearRe = regexp.MustCompile(`(\d{4})\s*[-–—~]\s*(\d{4})`)

// termKeyOf 学期名 → "起止年|第几学期"（两个平台的写法都能对上）
func termKeyOf(s string) string {
	y := termKeyYearRe.FindStringSubmatch(s)
	sem := 0
	switch {
	case hasAny(s, "第一", "第1", "秋季", "秋学期"):
		sem = 1
	case hasAny(s, "第二", "第2", "春季", "春学期"):
		sem = 2
	case hasAny(s, "第三", "第3", "夏季", "夏学期"):
		sem = 3
	}
	year := ""
	if y != nil {
		year = y[1] + "-" + y[2]
	}
	return fmt.Sprintf("%s|%d", year, sem)
}

func hasAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

// termEndDate 这门课的学期大概什么时候结束（用来判断"是不是还在进行中"）
func termEndDate(termStr string) (time.Time, bool) {
	year, sem, ok := parseTermName(termStr)
	if !ok {
		m := termKeyYearRe.FindStringSubmatch(termStr)
		if m == nil {
			return time.Time{}, false
		}
		t, err := time.ParseInLocation("2006-01-02", m[2]+"-09-01", bj)
		return t, err == nil
	}
	var yEnd int
	fmt.Sscanf(year[5:9], "%d", &yEnd)
	end := fmt.Sprintf("%d-09-15", yEnd)
	switch sem {
	case 1:
		end = fmt.Sprintf("%d-02-15", yEnd)
	case 2:
		end = fmt.Sprintf("%d-07-20", yEnd)
	}
	t, err := time.ParseInLocation("2006-01-02", end, bj)
	return t, err == nil
}

// termIsRecent 学期是不是"近的"：本学期的课没录像很常见，不能当成"没有录像"藏掉
func termIsRecent(termStr string) bool {
	end, ok := termEndDate(termStr)
	if !ok {
		return true // 学期不明 → 宁可不藏
	}
	return end.AddDate(0, 0, 100).After(time.Now())
}

// teacherCourseTitles 用离线课程库的老师索引，找出这个人教过的课名（去重、限量）
func teacherCourseTitles(name string, limit int) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, r := range catalogByTeacher(name, 60) {
		n := strings.TrimSpace(r.Name)
		if n == "" {
			continue
		}
		k := strings.ToLower(strings.ReplaceAll(n, " ", ""))
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, n)
		if len(out) >= limit {
			break
		}
	}
	return out
}
