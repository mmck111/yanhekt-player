// 搜索：线上检索 + 内置课程库兜底 + 学期过滤 + 逐门探录像
//
// 对应 server.js 的 searchRaw / probeOldCourses 调用与 /api/db/search 路由
package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type rawSearchEntry struct {
	at   time.Time
	data *rawSearchData
}

type rawSearchData struct {
	courses []courseRow
	total   int
	src     string
}

var (
	searchMu    sync.Mutex
	searchCache = map[string]rawSearchEntry{}
)

// searchRaw 搜一批课（只搜列表，不探"有没有录像"）。同一关键词的结果缓存 5 分钟
func searchRaw(q, term, token string, user *userInfo, limit, page int, notes *[]string) *rawSearchData {
	key := "s|" + q + "|" + term + "|" + strconv.Itoa(limit)
	searchMu.Lock()
	hit, ok := searchCache[key]
	searchMu.Unlock()
	if ok && time.Since(hit.at) < 5*time.Minute {
		return hit.data
	}

	wantYear, wantSem, wantOK := parseTermName(term)
	courses := []courseRow{}
	total := 0
	src := "api"

	// 1) 老平台服务端检索（课程名 / 课程编号；带 classrooms 真教室名）
	func() {
		defer func() {
			if e := recover(); e != nil {
				*notes = append(*notes, "服务端检索失败："+fmt.Sprint(e))
			}
		}()
		semesterIDs := []string{}
		if term != "" {
			if terms, err := oldSemesters(token); err == nil {
				for _, t := range terms {
					if noSpace(t.Name) == noSpace(term) {
						semesterIDs = []string{t.ID}
						break
					}
				}
			}
		}

		if wantOK && len(semesterIDs) == 0 {
			// 学期表里没有 semesters 这项（有的部署就是这样）→ 客户端按学年/学期筛，最多翻 4 页
			collected := []courseRow{}
			seen := map[string]bool{}
			for p := 1; p <= 4; p++ {
				r, err := oldSearch(q, nil, p, 50)
				if err != nil {
					*notes = append(*notes, "服务端检索失败："+err.Error())
					return
				}
				rows := []map[string]any{}
				for _, c := range r.list {
					if courseInTerm(c, wantYear, wantSem) {
						rows = append(rows, c)
					}
				}
				for _, c := range rows {
					row := oldCourseRow(c)
					if row != nil && !seen[row.ID] {
						seen[row.ID] = true
						collected = append(collected, *row)
					}
				}
				total = r.total
				if len(collected) >= limit {
					break
				}
				if len(rows) == 0 && p >= 2 {
					break // 已经是更早的学期了，别再翻
				}
			}
			courses = collected
			if len(courses) > limit {
				courses = courses[:limit]
			}
			if len(courses) == 0 {
				// 还是空 → 用内置课程库按学期挑（**全都给出来**）。
				// 前 10 门反查一下拿教室，剩下的由"探节次"顺手补
				catCourses, catTotal := catalogSearch(q, term, limit)
				courses = make([]courseRow, 0, len(catCourses))
				for _, mc := range catCourses {
					courses = append(courses, mc.asRow())
				}
				if catTotal > total {
					total = catTotal
				}
				if len(courses) > 0 {
					total = maxInt(total, len(courses))
				}
				top := courses
				if len(top) > 10 {
					top = top[:10]
				}
				var wg sync.WaitGroup
				for i := range top {
					wg.Add(1)
					go func(row *courseRow) {
						defer wg.Done()
						full, err := lookupCourse(row.ID)
						if err != nil {
							return
						}
						r := oldCourseRow(full)
						if r == nil {
							return
						}
						if r.Room != "" {
							row.Room = r.Room
						}
						if r.Rooms != nil && len(*r.Rooms) > 0 {
							row.Rooms = r.Rooms
						}
						if r.RoomByNumber != nil {
							row.RoomByNumber = r.RoomByNumber
						}
						if row.Teacher == "" && r.Teacher != "" {
							row.Teacher = r.Teacher
						}
						if row.Term == "" && r.Term != "" {
							row.Term = r.Term
						}
					}(&top[i])
				}
				wg.Wait()
				src = "catalog+lookup"
			}
			return
		}

		r, err := oldSearch(q, semesterIDs, page, minInt(limit, 50))
		if err != nil {
			*notes = append(*notes, "服务端检索失败："+err.Error())
			return
		}
		for _, c := range r.list {
			if row := oldCourseRow(c); row != nil {
				courses = append(courses, *row)
			}
		}
		total = r.total
	}()

	// 2) 教师名：服务端 keyword 只匹配课程名，所以再用课程库按教师找，反查补齐
	if len(courses) < 5 && q != "" {
		if byTeacher := catalogByTeacher(q, 20); len(byTeacher) > 0 {
			have := map[string]bool{}
			for _, c := range courses {
				have[c.ID] = true
			}
			missing := []catalogCourse{}
			for _, c := range byTeacher {
				if !have[c.ID] {
					missing = append(missing, c)
				}
			}
			if len(missing) > 12 {
				missing = missing[:12]
			}
			add := make([]courseRow, len(missing))
			var wg sync.WaitGroup
			for i, mc := range missing {
				wg.Add(1)
				go func(idx int, mc catalogCourse) {
					defer wg.Done()
					row := mc.asRow()
					if full, err := lookupCourse(mc.ID); err == nil {
						if r := oldCourseRow(full); r != nil {
							row = *r
						}
					}
					if row.Teacher == "" {
						row.Teacher = mc.Teacher
					}
					if row.Term == "" {
						row.Term = mc.Term
					}
					add[idx] = row
				}(i, mc)
			}
			wg.Wait()
			courses = append(courses, add...)
			total = maxInt(total, len(courses))
			if len(add) > 0 {
				src = "api+teacher" // Node 只在真的补进来时才改 source
			}
		}
	}

	// 3) 服务端完全没有结果 → 退回内置课程库（离线）
	if len(courses) == 0 {
		catCourses, catTotal := catalogSearch(q, term, limit)
		courses = make([]courseRow, 0, len(catCourses))
		for _, mc := range catCourses {
			courses = append(courses, mc.asRow())
		}
		total = catTotal
		src = "catalog"
	}

	data := &rawSearchData{courses: courses, total: total, src: src}
	searchMu.Lock()
	if len(searchCache) > 60 {
		searchCache = map[string]rawSearchEntry{}
	}
	searchCache[key] = rawSearchEntry{at: time.Now(), data: data}
	searchMu.Unlock()
	return data
}

func noSpace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r', '\v', '\f', 0x00a0, 0x3000:
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ---------------------------------------------------------------- 路由

type dbSearchOK struct {
	Ok        bool     `json:"ok"`
	Source    string   `json:"source"`
	Total     int      `json:"total"`
	Courses   []any    `json:"courses"`
	Hidden    *int     `json:"hidden,omitempty"`
	Probed    bool     `json:"probed"`
	Notes     []string `json:"notes"`
	Truncated *bool    `json:"truncated,omitempty"`
	Cap       *int     `json:"cap,omitempty"`
	NeedLogin *bool    `json:"needLogin,omitempty"`
}

type dbSearchFail struct {
	Ok    bool   `json:"ok"`
	Error string `json:"error"`
}

// searchScore 搜索结果的"匹配度"打分（和录像多少无关）：
//   - 关键词按空格拆词，每个词都要在 课程名 / 教师名 / 课程号 里至少命中一处
//   - 完全相等 6 分，前缀 4 分，包含 3 分（教师名按顿号/逗号拆开逐个比）
//   - 整串和课程名 / 教师名完全相等再加分
func searchScore(name, teacher, code, q string) int {
	qn := strings.ToLower(strings.TrimSpace(q))
	if qn == "" {
		return 0
	}
	tokens := strings.Fields(qn)
	nm := strings.ToLower(name)
	cd := strings.ToLower(code)
	teachers := splitTeacherNames(teacher)
	fieldScore := func(f, t string) int {
		if f == "" {
			return 0
		}
		if f == t {
			return 6
		}
		if strings.HasPrefix(f, t) {
			return 4
		}
		if len([]rune(t)) >= 2 && strings.Contains(f, t) {
			return 3
		}
		return 0
	}
	total := 0
	for _, t := range tokens {
		best := fieldScore(nm, t)
		if s := fieldScore(cd, t) - 1; s > best {
			best = s
		}
		for _, tn := range teachers {
			if s := fieldScore(tn, t); s > best {
				best = s
			}
		}
		if best <= 0 {
			return 0 // 有一个词没命中，就不算匹配
		}
		total += best
	}
	if nm != "" && nm == qn {
		total += 4
	}
	for _, tn := range teachers {
		if tn == qn {
			total += 3
			break
		}
	}
	return total
}

func splitTeacherNames(s string) []string {
	out := []string{}
	for _, part := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return r == '、' || r == ',' || r == '，' || r == ';' || r == '；' || r == '/' || r == ' '
	}) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// y2CourseVideoCount 取课程行上的录像数（可能是 int / float64 / nil）
func y2CourseVideoCount(row y2Course) int {
	switch v := row["videoCount"].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

// handleDBSearch /api/db/search
func handleDBSearch(w http.ResponseWriter, r *http.Request) {
	qs := r.URL.Query()
	q := strings.TrimSpace(qs.Get("q"))
	term := strings.TrimSpace(qs.Get("term"))
	limit := jint(qs.Get("limit"))
	if limit == 0 {
		limit = 40
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	page := jint(qs.Get("page"))
	if page < 1 {
		page = 1
	}
	if q == "" && term == "" {
		writeJSON(w, 400, dbSearchFail{Ok: false, Error: "请输入课程名或教师名"})
		return
	}

	notes := []string{}
	token := tokenOf(r)
	var user *userInfo
	if token != "" {
		user = verifyToken(token)
	}
	raw := searchRaw(q, term, token, user, limit, page, &notes)

	courses := []any{}
	for _, c := range raw.courses {
		courses = append(courses, c)
	}

	// 要"有几讲有录像"就得逐门探节次（每门 1 个请求）。
	// **全都探**（单次最多 120 门），再按录像多少排序 —— 只给前 N 门会漏掉有录像的课
	if qs.Get("probe") == "1" && user != nil {
		const cap = 120
		list := raw.courses
		if len(list) > cap {
			list = list[:cap]
		}
		probed, hidden := probeOldCourses(list, token, 8)
		// 排序：先看课名/教师名的匹配度，再按录像数从多到少。
		// 不然搜「数值分析」，名字里只有「分析」的课（刚好录像多）会排到真正的「数值分析」前面。
		sort.SliceStable(probed, func(i, j int) bool {
			si := searchScore(probed[i].Name, probed[i].Teacher, "", q)
			sj := searchScore(probed[j].Name, probed[j].Teacher, "", q)
			if si != sj {
				return si > sj
			}
			return videoCountOf(probed[i]) > videoCountOf(probed[j])
		})
		sorted := make([]any, 0, len(probed))
		for _, p := range probed {
			sorted = append(sorted, p)
		}
		truncated := len(raw.courses) > cap
		capN := cap
		writeJSON(w, 200, dbSearchOK{
			Ok: true, Source: raw.src, Total: raw.total, Courses: sorted,
			Hidden: &hidden, Probed: true, Notes: notes,
			Truncated: &truncated, Cap: &capN,
		})
		return
	}

	needLogin := user == nil
	writeJSON(w, 200, dbSearchOK{
		Ok: true, Source: raw.src, Total: raw.total, Courses: courses,
		Probed: false, Notes: notes, NeedLogin: &needLogin,
	})
}

func videoCountOf(p probedRow) int {
	if p.VideoCount == nil {
		return 0
	}
	return *p.VideoCount
}
