// 新版平台的路由与转发
//
// 对应 server.js 里 '/api/y2/*'、'/y2play'、'/y2dl'、'/y2stream'、'/diag' 那几段，
// 以及 y2ProxyPlay（透传 Range；m3u8 分片改回本代理；下载模式加 Content-Disposition）。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// y2Token 取新版 JWT（cookie y2tok；Node 版只认 cookie，不认 Authorization）
func y2Token(r *http.Request) string {
	c, err := r.Cookie(y2Cookie)
	if err != nil {
		return ""
	}
	if v, e := y2DecodeURIComponent(c.Value); e == nil {
		return v
	}
	return c.Value
}

var (
	y2ReNewlines   = regexp.MustCompile(`[\r\n]+`)
	y2ReNamedCk    = regexp.MustCompile(`(?:^|;\s*)_token=`)
	y2ReBadNameChr = regexp.MustCompile("[\\\\/:*?\"<>|\r\n\t]")
)

// y2StripQuotes 去掉首尾的引号/反引号（用户从 DevTools 复制的值经常带）
func y2StripQuotes(s string) string {
	s = strings.TrimLeft(s, "\"'`")
	s = strings.TrimRight(s, "\"'`")
	return strings.TrimSpace(s)
}

// y2CleanCookie 整理用户粘进来的整段 cookie
func y2CleanCookie(s string) string {
	s = y2StripQuotes(strings.TrimSpace(s))
	s = y2ReNewlines.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// ---------------------------------------------------------------- 注册

func registerY2(mux *http.ServeMux) {
	mux.HandleFunc("/api/y2/status", safe(handleY2Status))
	mux.HandleFunc("/api/y2/login", safe(handleY2Login))
	mux.HandleFunc("/api/y2/logout", safe(handleY2Logout))
	mux.HandleFunc("/api/y2/login/auto", safe(handleY2LoginAuto))
	mux.HandleFunc("/api/y2/courses", safe(handleY2Courses))
	mux.HandleFunc("/api/y2/terms", safe(handleY2Terms))
	mux.HandleFunc("/api/y2/search", safe(handleY2Search))
	mux.HandleFunc("/api/y2/verify", safe(handleY2Verify))
	mux.HandleFunc("/api/y2/find", safe(handleY2Search))
	mux.HandleFunc("/api/y2/sessions", safe(handleY2Sessions))
	mux.HandleFunc("/api/y2/sub", safe(handleY2Sub))
	mux.HandleFunc("/y2play", safe(handleY2Play))
	mux.HandleFunc("/y2dl", safe(handleY2Download))
	mux.HandleFunc("/y2stream", safe(handleY2Stream))
	mux.HandleFunc("/api/y2/diag", safe(handleY2Diag))
	mux.HandleFunc("/api/y2/crawl", safe(handleY2Crawl))
	mux.HandleFunc("/api/y2/catalog", safe(handleY2Catalog))
	mux.HandleFunc("/diag", safe(handleY2Diag))
}

// ---------------------------------------------------------------- 状态

type y2StatusResp struct {
	LoggedIn  bool  `json:"loggedIn"`
	UserID    any   `json:"userId"`
	ExpiresAt int64 `json:"expiresAt"`
	HasFFmpeg bool  `json:"hasFFmpeg"`
}

var (
	y2SchedWarmMu sync.Mutex
	y2SchedWarm   = map[string]bool{}
)

func y2WarmScheduleAsync(jwt string, id *y2Identity) {
	if jwt == "" || id == nil {
		return
	}
	if _, ok := y2ScheduleCachedOnly(jwt); ok {
		return
	}
	k := y2JwtTail(jwt)
	y2SchedWarmMu.Lock()
	if y2SchedWarm[k] {
		y2SchedWarmMu.Unlock()
		return
	}
	y2SchedWarm[k] = true
	y2SchedWarmMu.Unlock()
	go func() {
		_, _ = y2Schedule(jwt, id)
		y2SchedWarmMu.Lock()
		delete(y2SchedWarm, k)
		y2SchedWarmMu.Unlock()
	}()
}

func handleY2Status(w http.ResponseWriter, r *http.Request) {
	jwt := y2Token(r)
	id := y2Who(jwt)
	resp := y2StatusResp{LoggedIn: id != nil, UserID: nil, ExpiresAt: 0, HasFFmpeg: findFFmpeg() != ""}
	if id != nil {
		resp.UserID = id.userId
		resp.ExpiresAt = y2JwtExp(jwt)
		y2WarmScheduleAsync(jwt, id)
	}
	writeJSON(w, 200, resp)
}

// ---------------------------------------------------------------- 登录

func handleY2Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	body := readBody(r)
	// 用户可能粘的是：裸 JWT / 整段 document.cookie / DevTools 里 _token 的 Value（有时带引号）
	pasted := y2StripQuotes(strings.TrimSpace(jstr(body["jwt"])))
	cookieHeader := y2CleanCookie(jstr(body["cookie"]))

	raw := pasted
	if raw == "" {
		raw = cookieHeader
	}
	raw = y2ExtractJwt(raw)
	if raw == "" {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "没找到 JWT（粘的内容里没有 _token）"})
		return
	}
	cookieOut := cookieHeader
	if cookieOut == "" {
		cookieOut = pasted
	}
	if cookieOut != "" && !y2ReNamedCk.MatchString(cookieOut) {
		cookieOut = "_token=" + cookieOut
	}

	id, style, err := y2Login(raw, cookieOut)
	if err != nil {
		msg := err.Error()
		if msg == "" {
			msg = "Yanhe2 不认这个会话（过期或复制不全）"
		}
		writeJSON(w, 200, map[string]any{"ok": false, "error": msg})
		return
	}
	y2RememberIdentity(raw, id)
	setCookie(w, y2Cookie, encodeURIComponent(raw), y2TTL)
	y2CatalogKick(raw)
	writeJSON(w, 200, map[string]any{"ok": true, "userId": id.userId, "auth": style})
}

func handleY2Logout(w http.ResponseWriter, r *http.Request) {
	setCookie(w, y2Cookie, "", 0)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// handleY2LoginAuto 自动登录：去本机浏览器里找新版平台的 JWT，逐个验证
func handleY2LoginAuto(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	jwt, id, from, candidates, files := autoLoginNew()
	if id == nil {
		writeJSON(w, 200, map[string]any{
			"ok": false, "error": "没在浏览器里找到登录状态",
			"candidates": candidates, "files": files,
		})
		return
	}
	setCookie(w, y2Cookie, encodeURIComponent(jwt), y2TTL)
	y2CatalogKick(jwt)
	writeJSON(w, 200, map[string]any{"ok": true, "userId": id.userId, "from": from})
}

// ---------------------------------------------------------------- 课程 / 学期

func handleY2Courses(w http.ResponseWriter, r *http.Request) {
	jwt := y2Token(r)
	if y2Who(jwt) == nil {
		writeJSON(w, 401, map[string]any{"ok": false, "error": "尚未登录 Yanhe 2.0", "needLogin": true})
		return
	}
	courses, err := y2MyCourses(jwt)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "courses": courses})
}

func handleY2Terms(w http.ResponseWriter, r *http.Request) {
	jwt := y2Token(r)
	if y2Who(jwt) == nil {
		writeJSON(w, 401, map[string]any{"ok": false, "error": "尚未登录 Yanhe 2.0", "needLogin": true})
		return
	}
	terms, err := y2TermList(jwt)
	if err != nil {
		// 拿不到学期表不算致命：前端只显示「全部学期」
		writeJSON(w, 200, map[string]any{"ok": true, "terms": []y2Term{}, "warn": err.Error()})
		return
	}
	if terms == nil {
		terms = []y2Term{}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "terms": terms})
}

// ---------------------------------------------------------------- 检索

func handleY2Search(w http.ResponseWriter, r *http.Request) {
	jwt := y2Token(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	term := strings.TrimSpace(r.URL.Query().Get("term"))
	page := 1
	if n, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("page"))); err == nil && n > 1 {
		page = n
	}
	reqLimit := 0
	if n, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit"))); err == nil && n > 0 {
		if n > 100 {
			n = 100
		}
		reqLimit = n
	}
	if q == "" && term == "" {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "请输入课程名"})
		return
	}

	id := y2Who(jwt)
	resolveTermFilter := func(rawTerm, j string) string {
		if rawTerm == "" {
			return ""
		}
		if strings.Contains(rawTerm, "-") || strings.Contains(rawTerm, "学期") {
			return rawTerm
		}
		if j != "" {
			if terms, err := y2TermList(j); err == nil {
				for _, t := range terms {
					if t.ID == rawTerm {
						return t.Name
					}
				}
			}
		}
		return ""
	}
	if id == nil {
		// 没登录也能搜本地课程库：先把课找出来，点播放时再让用户登录
		if q != "" {
			if cat := y2CatalogLoad(); cat != nil {
				lim := 40
				if reqLimit > 0 {
					lim = reqLimit
				}
				tf := resolveTermFilter(term, "")
				if hits, matched := y2CatalogSearchScoped(cat, q, tf, lim); len(hits) > 0 {
					out := make([]y2Course, 0, len(hits))
					for _, h := range hits {
						c := y2CatRowCourse(h)
						c["note"] = "未登录：只显示课程库匹配"
						out = append(out, c)
					}
					writeJSON(w, 200, map[string]any{
						"ok": true, "courses": out, "total": matched, "shown": len(out),
						"hidden": 0, "unverified": 0, "source": "local",
						"notes": []string{fmt.Sprintf("未登录：本地课程库命中 %d 门，登录后才能看录像", matched)},
					})
					return
				}
			}
		}
		writeJSON(w, 401, map[string]any{"ok": false, "error": "尚未登录 Yanhe 2.0", "needLogin": true})
		return
	}

	notes := []string{}
	courses := []y2Course{}
	total := 0
	source := "catalog"
	hidden, unverified := 0, 0

	// 0) 本地课程库优先：库里搜是本地匹配，包含/归一化/序号写法都能对上，
	//    也不吃上游的限流。库里没命中再走线上检索。
	//    注意 limit=20：同一位老师同一学期可能有好几个教学班（孙新的数值分析就有 2 个），
	//    以前去重 + 限 12 条会把它们吃掉。
	localHits, localMatched := 0, 0
	if q != "" {
		if cat := y2CatalogLoad(); cat != nil && len(cat.Rows) > 0 {
			lim := 20
			if reqLimit > 0 {
				lim = reqLimit
			}
			tf := resolveTermFilter(term, jwt)
			hits, matched := y2CatalogSearchScoped(cat, q, tf, lim)
			for _, h := range hits {
				courses = append(courses, y2CatRowCourse(h))
			}
			if len(hits) > 0 {
				localHits, localMatched = len(hits), matched
				total = matched
				source = "local"
				notes = append(notes, fmt.Sprintf("本地课程库命中 %d 门，显示前 %d 门（库存 %d 门）", matched, len(hits), len(cat.Rows)))
			}
		}
	}
	_ = localMatched

	if localHits == 0 {
		if r0, err := y2SearchCourses(jwt, q, term, page, 20); err == nil {
			courses = r0.courses
			total = r0.total
		} else {
			notes = append(notes, "课程检索："+err.Error())
		}
	}

	// 新版检索只吃课程名。关键词按空格拆开处理：
	//   「数值分析 孙新」→ 最长词当课程名，其余词要求出现在课名或老师里
	//   「孙新」→ 课程名查不到时，用离线课程库的老师索引找出他教的课名逐门回查
	tokens := strings.Fields(q)

	// title 是精确/前缀匹配：搜「大学物理」查不到「大学物理AII」。
	// 不管上游返回了几门，都用内置课程库做一次「课名包含」展开再逐门回查 ——
	// 只在上游返回 0 门时展开是不够的：搜「大学物理」时上游会返回「大学物理」本身，
	// 结果非空就再也不会去找「大学物理AⅡ / AII / 2」这些写法了。
	if localHits == 0 && q != "" {
		longest := ""
		for _, t := range tokens {
			if len([]rune(t)) > len([]rune(longest)) {
				longest = t
			}
		}
		if longest == "" {
			longest = q
		}
		cat, _ := catalogSearch(longest, "", 400)
		titles := []string{}
		seenT := map[string]bool{}
		for _, c := range courses {
			seenT[y2NormText(jstr(c["title"]))] = true
		}
		for _, c := range cat {
			n := strings.TrimSpace(c.Name)
			k := y2NormText(n)
			if n == "" || seenT[k] || !y2FuzzyAll(n, tokens) {
				continue
			}
			seenT[k] = true
			titles = append(titles, n)
			if len(titles) >= 10 {
				break
			}
		}
		if len(titles) > 0 {
			added := 0
			seenC := map[string]bool{}
			for _, c := range courses {
				seenC[jstr(c["courseId"])] = true
			}
			var mu sync.Mutex
			sem := make(chan struct{}, 4)
			var wg sync.WaitGroup
			for _, title := range titles {
				wg.Add(1)
				go func(title string) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					r2, err := y2SearchCourses(jwt, title, term, 1, 20)
					if err != nil {
						return
					}
					mu.Lock()
					defer mu.Unlock()
					for _, c := range r2.courses {
						id := jstr(c["courseId"])
						if seenC[id] {
							continue
						}
						if !y2FuzzyAll(jstr(c["title"])+" "+jstr(c["teacher"]), tokens) {
							continue
						}
						seenC[id] = true
						added++
						courses = append(courses, c)
					}
				}(title)
			}
			wg.Wait()
			if added > 0 {
				total = len(courses)
				notes = append(notes, "按课程名包含匹配")
			}
		}
	}

	if len(tokens) > 1 && len(courses) > 0 {
		titleQ := ""
		for _, t := range tokens {
			if len([]rune(t)) > len([]rune(titleQ)) {
				titleQ = t
			}
		}
		rest := []string{}
		for _, t := range tokens {
			if t != titleQ {
				rest = append(rest, t)
			}
		}
		kept := []y2Course{}
		for _, c := range courses {
			if y2FuzzyAll(jstr(c["title"])+" "+jstr(c["teacher"]), rest) {
				kept = append(kept, c)
			}
		}
		courses = kept
		total = len(courses)
	}

	if len(courses) == 0 && len(tokens) > 0 {
		// 挑一个"像老师名"的词：在课程库教师索引里能命中的那个
		tname, best := "", 0
		for _, t := range tokens {
			if n := len(catalogByTeacher(t, 1)); n > best {
				best, tname = n, t
			}
		}
		if tname != "" && best > 0 {
			rest := []string{}
			for _, t := range tokens {
				if t != tname {
					rest = append(rest, strings.ToLower(t))
				}
			}
			if direct := y2SearchByTeacher(jwt, tname, term); len(direct) > 0 {
				seen0 := map[string]bool{}
				for _, c := range courses {
					seen0[jstr(c["courseId"])] = true
				}
				for _, c := range direct {
					id := jstr(c["courseId"])
					if seen0[id] {
						continue
					}
					hay := jstr(c["title"]) + " " + jstr(c["teacher"])
					if !y2FuzzyHas(hay, tname) || !y2FuzzyAll(hay, rest) {
						continue
					}
					seen0[id] = true
					courses = append(courses, c)
				}
				notes = append(notes, "新版接口按老师名直接命中")
			}

			titles := teacherCourseTitles(tname, 12)
			seen := map[string]bool{}
			for _, c := range courses {
				seen[jstr(c["courseId"])] = true
			}
			others := []string{}
			for _, t := range tokens {
				if t != tname {
					others = append(others, t)
				}
			}
			var mu sync.Mutex
			sem := make(chan struct{}, 4)
			var wg sync.WaitGroup
			for _, title := range titles {
				wg.Add(1)
				go func(title string) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					r2, err := y2SearchCourses(jwt, title, term, 1, 20)
					if err != nil {
						return
					}
					mu.Lock()
					defer mu.Unlock()
					for _, c := range r2.courses {
						id := jstr(c["courseId"])
						if seen[id] {
							continue
						}
						hay := jstr(c["title"]) + " " + jstr(c["teacher"])
						if !y2FuzzyHas(hay, tname) || !y2FuzzyAll(hay, others) {
							continue
						}
						seen[id] = true
						courses = append(courses, c)
					}
				}(title)
			}
			wg.Wait()
			if len(courses) > 0 {
				total = len(courses)
				notes = append(notes, "按教师名匹配："+tname)
			}
		}
	}

	// 全部失败才退回「扫最近三周课表」的老办法（那边已经只统计有录像的讲次）
	if len(courses) == 0 {
		if list, err := y2FindCourses(jwt, q, 21); err == nil {
			if len(list) > 0 {
				courses = list
				total = len(list)
				source = "schedule"
			}
		} else {
			notes = append(notes, "课表扫描："+err.Error())
		}
	}

	// 录像校验不再挡在搜索里：课程库匹配是本地毫秒级的，慢的只有"逐门问平台有没有录像"。
	// 这里只套用 12 小时内已经验过的结果（有缓存就顺带把没录像的筛掉），
	// 没缓存的课程先照常列出来，由前端随后调 /api/y2/verify 补徽章。
	if len(courses) > 0 {
		kept := make([]y2Course, 0, len(courses))
		for _, c := range courses {
			v, ok := y2VerdictGet(jstr(c["courseId"]))
			if !ok {
				kept = append(kept, c)
				continue
			}
			if v.Videos <= 0 && v.Checked {
				hidden++
				continue
			}
			c["verified"] = true
			c["totalSessions"] = v.Total
			if v.Room != "" {
				c["room"] = v.Room
			}
			if v.Checked {
				c["videoCount"] = v.Videos
			}
			kept = append(kept, c)
		}
		courses = kept
	}

	if len(courses) > 1 && q != "" && localHits == 0 {
		qn, tokNorms := y2NormalizeTokens(q)
		termNames := map[string]string{}
		if terms, e := y2TermList(jwt); e == nil {
			for _, t := range terms {
				termNames[t.ID] = t.Name
			}
		}
		scoreOf := func(c y2Course) int {
			var tNorms []string
			for _, p := range splitTeacherNames(jstr(c["teacher"])) {
				if np := y2NormText(p); np != "" {
					tNorms = append(tNorms, np)
				}
			}
			return y2ScoreNormalized(y2NormText(jstr(c["title"])), y2NormText(jstr(c["code"])), tNorms, qn, tokNorms)
		}
		timeOf := func(c y2Course) int {
			tn := jstr(c["termName"])
			if tn == "" {
				tn = termNames[jstr(c["termId"])]
			}
			return y2TermRankOfString(tn, jstr(c["code"]))
		}
		sort.SliceStable(courses, func(i, j int) bool {
			si, sj := scoreOf(courses[i]), scoreOf(courses[j])
			if si != sj {
				return si > sj
			}
			ti, tj := timeOf(courses[i]), timeOf(courses[j])
			if ti != tj {
				return ti > tj
			}
			return len([]rune(jstr(courses[i]["title"]))) < len([]rune(jstr(courses[j]["title"])))
		})
	}

	writeJSON(w, 200, map[string]any{
		"ok": true, "courses": courses, "total": total, "shown": len(courses),
		"hidden": hidden, "unverified": unverified, "source": source, "notes": notes,
	})
}

// handleY2Verify 前端拿到列表后，再单独来问"这几门到底有没有录像"。
// 一次最多 20 门，5 路并发，结果写进 12 小时缓存（下次搜索直接命中）。
func handleY2Verify(w http.ResponseWriter, r *http.Request) {
	jwt := y2Token(r)
	id := y2Who(jwt)
	if id == nil {
		writeJSON(w, 401, map[string]any{"ok": false, "error": "尚未登录 Yanhe 2.0", "needLogin": true})
		return
	}
	body := readBody(r)
	ids := []string{}
	for _, v := range y2Arr(body["ids"]) {
		s := strings.TrimSpace(y2Text(v))
		if s != "" {
			ids = append(ids, s)
		}
	}
	if len(ids) > 20 {
		ids = ids[:20]
	}
	if len(ids) == 0 {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "缺少 ids"})
		return
	}

	out := make([]map[string]any, len(ids))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, cid := range ids {
		if v, ok := y2VerdictGet(cid); ok {
			out[i] = map[string]any{"courseId": cid, "videos": v.Videos, "total": v.Total, "room": v.Room, "checked": v.Checked, "cached": true}
			continue
		}
		wg.Add(1)
		go func(i int, cid string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res := map[string]any{"courseId": cid, "videos": 0, "total": 0, "checked": false}
			v, err := y2Videos(jwt, id, cid, false, "", "", false)
			if err == nil && v != nil {
				res["videos"] = v.videoCount
				res["total"] = v.totalSessions
				res["room"] = v.room
				// checked 表示"数量和有没有都确定了"：只抽了几讲验的，数字不准，
				// 前端就别写"3 讲有录像"，写"有录像 + 共 10 讲"更诚实。
				checked := v.totalSessions > 0 && v.verified && v.countExact
				res["checked"] = checked
				res["verified"] = v.videoCount > 0
				y2VerdictSet(cid, y2Verdict{Videos: v.videoCount, Total: v.totalSessions, Room: v.room, Checked: checked})
			} else if err != nil {
				res["error"] = err.Error()
			}
			out[i] = res
		}(i, cid)
	}
	wg.Wait()
	y2VerdictFlush(false)
	writeJSON(w, 200, map[string]any{"ok": true, "items": out})
}

// ---------------------------------------------------------------- 讲次

type y2SessionOut struct {
	SubID    string `json:"subId"`
	Title    string `json:"title"`
	Period   string `json:"period"`
	Date     string `json:"date"`
	Duration int    `json:"duration"`
	Status   string `json:"status"`
	Room     string `json:"room"`
	Teacher  string `json:"teacher"`
	TermID   string `json:"termId"`
	TermName string `json:"termName"`
}

func handleY2Sessions(w http.ResponseWriter, r *http.Request) {
	jwt := y2Token(r)
	id := y2Who(jwt)
	if id == nil {
		writeJSON(w, 401, map[string]any{"ok": false, "error": "尚未登录 Yanhe 2.0", "needLogin": true})
		return
	}
	qv := r.URL.Query()
	courseID := strings.TrimSpace(qv.Get("courseId"))
	term := strings.TrimSpace(qv.Get("term"))
	q := strings.TrimSpace(qv.Get("q"))
	termName := strings.TrimSpace(qv.Get("termName"))
	if courseID == "" {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "缺少 courseId"})
		return
	}
	if qv.Get("refresh") == "1" {
		y2VideoInvalidate(jwt, courseID)
	}

	data, err := y2Videos(jwt, id, courseID, true, y2LongestToken(q), termName, true)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	notes := []string{}
	if data.note != "" {
		notes = append(notes, data.note)
	}

	list := append([]y2Session{}, data.sessions...)
	if term != "" {
		scoped := []y2Session{}
		for _, s := range list {
			if s.TermID == "" || s.TermID == term {
				scoped = append(scoped, s)
			}
		}
		if len(scoped) > 0 {
			list = scoped
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].StartAt > list[j].StartAt })

	out := make([]y2SessionOut, 0, len(list))
	for _, s := range list {
		out = append(out, y2SessionOut{
			SubID: s.SubID, Title: s.Title, Period: s.Period, Date: s.Date,
			Duration: s.Duration, Status: s.Status, Room: s.Room, Teacher: s.Teacher,
			TermID: s.TermID, TermName: s.TermName,
		})
	}
	writeJSON(w, 200, map[string]any{
		"ok": true, "source": data.source, "notes": notes,
		"hidden":   data.totalSessions - data.videoCount,
		"total":    data.totalSessions,
		"sessions": out,
	})
}

func handleY2Sub(w http.ResponseWriter, r *http.Request) {
	jwt := y2Token(r)
	if y2Who(jwt) == nil {
		writeJSON(w, 401, map[string]any{"ok": false, "error": "尚未登录 Yanhe 2.0", "needLogin": true})
		return
	}
	qv := r.URL.Query()
	courseID := strings.TrimSpace(qv.Get("courseId"))
	subID := strings.TrimSpace(qv.Get("subId"))

	info, err := y2SubInfoOf(jwt, courseID, subID)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if info == nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": "取流失败"})
		return
	}
	if len(info.Streams) == 0 {
		writeJSON(w, 200, map[string]any{"ok": false, "error": "这一讲没有录像文件（可能还没生成回放）"})
		return
	}
	audios := info.Audios
	if audios == nil {
		audios = []y2AudioSource{}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "info": info, "audios": audios})
}

// ---------------------------------------------------------------- 播放 / 下载

func handleY2Play(w http.ResponseWriter, r *http.Request) {
	jwt := y2Token(r)
	id := y2Who(jwt)
	if id == nil {
		w.WriteHeader(401)
		_, _ = w.Write([]byte("need login"))
		return
	}
	raw := r.URL.Query().Get("u")
	if raw == "" {
		w.WriteHeader(400)
		_, _ = w.Write([]byte("missing u"))
		return
	}
	if err := y2ProxyPlay(w, r, dec(raw), id, y2ProxyOpts{}); err != nil {
		w.WriteHeader(502)
		_, _ = w.Write([]byte("play error: " + err.Error()))
	}
}

func handleY2Download(w http.ResponseWriter, r *http.Request) {
	jwt := y2Token(r)
	id := y2Who(jwt)
	if id == nil {
		w.Header().Set("content-type", "text/plain; charset=utf-8")
		w.WriteHeader(401)
		_, _ = w.Write([]byte("need login"))
		return
	}
	qv := r.URL.Query()
	c := qv.Get("c")
	s := qv.Get("s")
	k := qv.Get("k")
	if k == "" {
		k = "camera"
	}
	info, err := y2SubInfoOf(jwt, c, s)
	if err != nil {
		w.Header().Set("content-type", "text/plain; charset=utf-8")
		w.WriteHeader(502)
		_, _ = w.Write([]byte("下载出错：" + err.Error()))
		return
	}
	name := qv.Get("title")
	if name == "" {
		name = "course-" + c
	}
	name = y2ReBadNameChr.ReplaceAllString(name, "_")
	if r := []rune(name); len(r) > 80 {
		name = string(r[:80])
	}
	queueID := strings.TrimSpace(qv.Get("queueId"))
	if k == "dual" {
		handleY2DualDownload(w, r, name, queueID, info, id)
		return
	}
	st, ok := y2PickStream(info, k)
	if !ok {
		w.Header().Set("content-type", "text/plain; charset=utf-8")
		w.WriteHeader(404)
		_, _ = w.Write([]byte("这一讲没有可下载的画面"))
		return
	}
	if strings.HasSuffix(strings.ToLower(strings.Split(st.URL, "?")[0]), ".m3u8") {
		if err := y2DownloadHLS(w, r, st.URL, name, id, queueID); err != nil {
			w.Header().Set("content-type", "text/plain; charset=utf-8")
			w.WriteHeader(502)
			_, _ = w.Write([]byte("下载出错：" + err.Error()))
		}
		return
	}
	resumeOff, _ := strconv.ParseInt(strings.TrimSpace(qv.Get("startBytes")), 10, 64)
	if err := y2DownloadMP4(w, r, st.URL, id, y2ProxyOpts{filename: name + ".mp4", ext: ".mp4", reqPath: r.URL.RequestURI(), resumeFrom: resumeOff, queueID: queueID}); err != nil {
		w.Header().Set("content-type", "text/plain; charset=utf-8")
		w.WriteHeader(502)
		_, _ = w.Write([]byte("下载出错：" + err.Error()))
	}
}

var (
	y2MasterRe   = regexp.MustCompile(`#EXT-X-STREAM-INF`)
	y2MapURIAttr = regexp.MustCompile(`URI="([^"]+)"`)
)

func y2ResolvePlaylistRef(ref, origURL string, finalURL *url.URL) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ref
	}
	low := strings.ToLower(ref)
	if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
		return ref
	}
	origParsed, err := url.Parse(origURL)
	if err != nil {
		return ref
	}
	baseParsed := origParsed
	if finalURL != nil && finalURL.Host != "" && strings.EqualFold(finalURL.Host, origParsed.Host) {
		baseParsed = finalURL
	}
	refParsed, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return baseParsed.ResolveReference(refParsed).String()
}

// y2DownloadHLS 新版录像 HLS：直接直连上游签名拉片，4 路并发有序拼片 + 空闲卡顿看门狗 + 断线重试
func y2DownloadHLS(w http.ResponseWriter, r *http.Request, streamURL, name string, id *y2Identity, queueID string) error {
	return y2DownloadHLSWithTracker(w, r, streamURL, name, id, queueID, nil)
}

func y2DownloadHLSWithTracker(w http.ResponseWriter, r *http.Request, streamURL, name string, id *y2Identity, queueID string, extTracker *dlTrackItem) error {
	fetchUpstream := func(ctx context.Context, rawURL string, maxBytes int64) (int, []byte, *url.URL, error) {
		target, err := y2SignedURL(rawURL, id)
		if err != nil {
			return 0, nil, nil, err
		}
		segCtx, segCancel := context.WithTimeout(ctx, 55*time.Second)
		defer segCancel()
		req, err := http.NewRequestWithContext(segCtx, "GET", target, nil)
		if err != nil {
			return 0, nil, nil, err
		}
		req.Header.Set("User-Agent", y2UA)
		req.Header.Set("Referer", y2Base+"/")
		req.Header.Set("Accept", "*/*")
		resp, err := y2StreamClient.Do(req)
		if err != nil {
			return 0, nil, nil, err
		}
		var finalURL *url.URL
		if resp.Request != nil && resp.Request.URL != nil {
			finalURL = resp.Request.URL
		}
		if resp.StatusCode != 200 {
			_ = resp.Body.Close()
			return resp.StatusCode, nil, finalURL, nil
		}
		b, rerr := readBodyStallSafe(segCtx, segCancel, resp.Body, 25*time.Second, maxBytes)
		return resp.StatusCode, b, finalURL, rerr
	}

	curURL := streamURL
	text := ""
	var lastFinalURL *url.URL
	for hop := 0; hop < 4; hop++ {
		var hopErr error
		var st int
		var b []byte
		var finalU *url.URL
		for try := 0; try < 3; try++ {
			st, b, finalU, hopErr = fetchUpstream(r.Context(), curURL, y2SegMax)
			if hopErr == nil && st == 200 {
				break
			}
			sleepCtx(r.Context(), 300*(try+1))
		}
		if hopErr != nil {
			return hopErr
		}
		if st != 200 {
			return fmt.Errorf("播放列表 HTTP %d", st)
		}
		text = string(b)
		lastFinalURL = finalU
		if !y2MasterRe.MatchString(text) {
			break
		}
		child := ""
		for _, line := range strings.Split(text, "\n") {
			t := strings.TrimSpace(line)
			if t != "" && !strings.HasPrefix(t, "#") {
				child = t
				break
			}
		}
		if child == "" {
			return fmt.Errorf("主播放列表里没有子列表")
		}
		curURL = y2ResolvePlaylistRef(child, curURL, finalU)
	}

	if strings.Contains(text, "#EXT-X-KEY") && strings.Contains(text, "METHOD=AES-128") {
		return fmt.Errorf("这讲的播放列表是 AES-128 加密的，暂时不能离线拼片")
	}

	initURL := ""
	segs := []string{}
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "#EXT-X-MAP") {
			if m := y2MapURIAttr.FindStringSubmatch(t); len(m) > 1 {
				initURL = y2ResolvePlaylistRef(m[1], curURL, lastFinalURL)
			}
			continue
		}
		if strings.HasPrefix(t, "#") {
			continue
		}
		segs = append(segs, y2ResolvePlaylistRef(t, curURL, lastFinalURL))
	}
	if len(segs) == 0 {
		return fmt.Errorf("播放列表里没有分片")
	}

	order := segs
	if initURL != "" {
		order = append([]string{initURL}, segs...)
	}

	startSeg, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("startSeg")))
	startBytes, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("startBytes")), 10, 64)
	if startSeg < 0 || startSeg >= len(order) {
		startSeg = 0
		startBytes = 0
	}

	// 先取起始分片（带重试）：取不到才报错，避免给用户损坏文件
	var first []byte
	var firstSt int
	var firstErr error
	for try := 0; try < 4; try++ {
		firstSt, first, _, firstErr = fetchUpstream(r.Context(), order[startSeg], 32<<20)
		if firstErr == nil && firstSt == 200 && len(first) > 0 {
			break
		}
		sleepCtx(r.Context(), 350*(try+1))
	}
	if firstErr != nil {
		return fmt.Errorf("第一片取不到：%v", firstErr)
	}
	if firstSt != 200 || len(first) == 0 {
		return fmt.Errorf("第一片取不到（HTTP %d）", firstSt)
	}

	dlCtx, dlCancel := context.WithCancel(r.Context())
	defer dlCancel()
	tracker := extTracker
	if tracker == nil {
		tracker = dlTrackBindOrStart(queueID, "y2", name+".ts", len(order), 0)
		tracker.setReqPath(r.URL.RequestURI())
	} else {
		dlTrackMu.Lock()
		tracker.TotalSegs = len(order)
		tracker.Title = name + ".ts"
		dlTrackMu.Unlock()
	}
	if startSeg > 0 {
		tracker.setResumeInit(startSeg, startBytes)
		w.Header().Set("X-YHKT-Resume-Seg", strconv.Itoa(startSeg))
	}
	tracker.setCancel(dlCancel)
	w.Header().Set("content-type", "video/mp2t")
	w.Header().Set("content-disposition", `attachment; filename="lecture.ts"; filename*=UTF-8''`+y2FormEncode(name+".ts"))
	w.Header().Set("cache-control", "no-store")
	w.Header().Set("X-YHKT-DL-ID", tracker.ID)
	w.WriteHeader(200)
	_, _ = w.Write(first)

	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}
	done := startSeg + 1
	failed, consecutiveFails := 0, 0
	doneBytes := startBytes + int64(len(first))
	tracker.update(done, doneBytes)
	gone := false
	fatalHalf := false

	var throttled atomic.Bool
	getSegRetry := func(u string) []byte {
		for n := 0; n < 6; n++ {
			if dlCtx.Err() != nil || tracker.isCancelled() {
				return nil
			}
			st, b, _, err := fetchUpstream(dlCtx, u, 32<<20)
			if err == nil && st == 200 && len(b) > 0 {
				return b
			}
			if st == 404 {
				return nil
			}
			if st == 403 || st == 429 {
				throttled.Store(true)
				if !sleepCtx(dlCtx, 900*(n+1)) {
					return nil
				}
				continue
			}
			if !sleepCtx(dlCtx, 300*(n+1)) {
				return nil
			}
		}
		return nil
	}

	type y2SegRes struct {
		idx int
		buf []byte
	}
	const winSize = 8
	results := make(map[int][]byte, winSize+2)
	nextWrite := startSeg + 1
	nextAssign := startSeg + 1
	inFlight := 0
	resCh := make(chan y2SegRes, winSize+2)

	maxWorkers := func() int {
		if throttled.Load() {
			return 2
		}
		return 4
	}

	for nextWrite < len(order) {
		if dlCtx.Err() != nil || tracker.isCancelled() || gone {
			gone = true
			break
		}
		for nextAssign < len(order) && inFlight < maxWorkers() && nextAssign-nextWrite < winSize {
			idx := nextAssign
			nextAssign++
			inFlight++
			go func(si int, su string) {
				resCh <- y2SegRes{idx: si, buf: getSegRetry(su)}
			}(idx, order[idx])
		}
		if inFlight == 0 {
			break
		}
		res := <-resCh
		inFlight--
		if res.buf == nil {
			results[res.idx] = []byte{}
		} else {
			results[res.idx] = res.buf
		}
		for nextWrite < len(order) {
			buf, ready := results[nextWrite]
			if !ready {
				break
			}
			delete(results, nextWrite)
			if len(buf) == 0 {
				failed++
				consecutiveFails++
				if consecutiveFails >= 8 {
					fatalHalf = true
					gone = true
					break
				}
				nextWrite++
				continue
			}
			consecutiveFails = 0
			done++
			if _, e := w.Write(buf); e != nil {
				gone = true
				break
			}
			doneBytes += int64(len(buf))
			nextWrite++
			tracker.update(nextWrite, doneBytes)
			if flusher != nil {
				flusher.Flush()
			}
		}
		if gone {
			break
		}
	}
	if tracker.isCancelled() {
		tracker.finish("cancelled", "用户已取消")
	} else if gone || fatalHalf || dlCtx.Err() != nil {
		msg := "连接已断开，可在下载管理中继续"
		if fatalHalf {
			msg = "部分分片连续获取失败（已保留断点），请点击继续下载"
		}
		tracker.finish("interrupted", msg)
	} else {
		tracker.finish("done", "")
	}
	fmt.Printf("新版下载结束：成功 %d 片，失败 %d 片\n", done, failed)
	return nil
}

func handleY2Stream(w http.ResponseWriter, r *http.Request) {
	jwt := y2Token(r)
	id := y2Who(jwt)
	if id == nil {
		w.WriteHeader(401)
		_, _ = w.Write([]byte("need login"))
		return
	}
	qv := r.URL.Query()
	c := qv.Get("c")
	k := qv.Get("k")
	if k == "" {
		k = "camera"
	}
	info, err := y2SubInfoOf(jwt, c, qv.Get("s"))
	if err != nil {
		w.WriteHeader(502)
		_, _ = w.Write([]byte("stream error"))
		return
	}
	st, ok := y2PickStream(info, k)
	if !ok {
		w.WriteHeader(404)
		_, _ = w.Write([]byte("no stream"))
		return
	}
	w.Header().Set("location", "/y2play?u="+enc(st.URL))
	w.WriteHeader(302)
}

// y2PickStream 按类型挑画面，挑不到退回第一路
func y2PickStream(info *y2SubInfo, kind string) (y2Stream, bool) {
	if info == nil {
		return y2Stream{}, false
	}
	for _, s := range info.Streams {
		if s.Type == kind {
			return s, true
		}
	}
	if len(info.Streams) > 0 {
		return info.Streams[0], true
	}
	return y2Stream{}, false
}

// y2ProxyOpts 下载模式（要整份文件 + 附件文件名）
type y2ProxyOpts struct {
	filename   string
	ext        string
	reqPath    string
	resumeFrom int64
	queueID    string
	extTracker *dlTrackItem
}

func y2ParseContentRangeTotal(cr string) int64 {
	// e.g. "bytes 0-4194303/123456789"
	idx := strings.LastIndex(cr, "/")
	if idx < 0 {
		return 0
	}
	totStr := strings.TrimSpace(cr[idx+1:])
	if totStr == "" || totStr == "*" {
		return 0
	}
	v, _ := strconv.ParseInt(totStr, 10, 64)
	return v
}

// y2DownloadMP4 分块 Range 下载器（4MB/块 · 3 路并发预取 · 每块独立重签名与重连恢复）
// 彻底解决单条长连接传到一半因 CDN 断开或签名过期导致的「下载到一半就停了」。
func y2DownloadMP4(w http.ResponseWriter, r *http.Request, rawURL string, id *y2Identity, opts y2ProxyOpts) error {
	const chunkSize int64 = 4 << 20
	startOffset := opts.resumeFrom
	if startOffset < 0 {
		startOffset = 0
	}

	dlCtx, dlCancel := context.WithCancel(r.Context())
	defer dlCancel()

	openRange := func(start, end int64) (*http.Response, error) {
		target, err := y2SignedURL(rawURL, id)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(dlCtx, "GET", target, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", y2UA)
		req.Header.Set("Referer", y2Base+"/")
		req.Header.Set("Accept", "*/*")
		if end >= start && start >= 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
		} else if start > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", start))
		}
		return y2StreamClient.Do(req)
	}

	var firstResp *http.Response
	var firstErr error
	for try := 0; try < 4; try++ {
		firstResp, firstErr = openRange(startOffset, startOffset+chunkSize-1)
		if firstErr == nil && firstResp != nil && (firstResp.StatusCode == 206 || firstResp.StatusCode == 200) {
			break
		}
		if firstResp != nil {
			_ = firstResp.Body.Close()
		}
		if !sleepCtx(dlCtx, 350*(try+1)) {
			break
		}
	}
	if firstErr != nil {
		return firstErr
	}
	if firstResp == nil {
		return fmt.Errorf("上游响应为空")
	}
	if firstResp.StatusCode != 206 && firstResp.StatusCode != 200 {
		code := firstResp.StatusCode
		_ = firstResp.Body.Close()
		return fmt.Errorf("上游返回 HTTP %d", code)
	}

	ct := firstResp.Header.Get("content-type")
	if ct == "" {
		ct = "video/mp4"
	}
	setAttachHeaders := func(dlID string, resumed bool, resumedFrom int64) {
		w.Header().Set("content-type", ct)
		w.Header().Set("content-disposition",
			`attachment; filename="lecture`+opts.ext+`"; filename*=UTF-8''`+encodeURIComponent(opts.filename))
		w.Header().Set("cache-control", "no-store")
		w.Header().Set("X-YHKT-DL-ID", dlID)
		if resumed && resumedFrom > 0 {
			w.Header().Set("X-YHKT-Resume-Offset", strconv.FormatInt(resumedFrom, 10))
		}
	}

	totalSize := y2ParseContentRangeTotal(firstResp.Header.Get("content-range"))
	isChunked206 := firstResp.StatusCode == 206 && totalSize > 0
	if isChunked206 {
		firstCtx, firstCancel := context.WithTimeout(dlCtx, 60*time.Second)
		firstData, err := readBodyStallSafe(firstCtx, firstCancel, firstResp.Body, 30*time.Second, chunkSize+1<<20)
		firstCancel()
		if err != nil {
			return fmt.Errorf("读取首块失败：%v", err)
		}
		if startOffset >= totalSize {
			_ = dlTrackBindOrStart(opts.queueID, "y2", opts.filename, 1, totalSize)
			return nil
		}
		remStart := startOffset + int64(len(firstData))
		var chunkStarts []int64
		var chunkEnds []int64
		for off := remStart; off < totalSize; off += chunkSize {
			end := off + chunkSize - 1
			if end >= totalSize {
				end = totalSize - 1
			}
			chunkStarts = append(chunkStarts, off)
			chunkEnds = append(chunkEnds, end)
		}
		totalSegs := 1 + len(chunkStarts)
		tracker := opts.extTracker
		if tracker == nil {
			tracker = dlTrackBindOrStart(opts.queueID, "y2", opts.filename, totalSegs, totalSize)
			if opts.reqPath != "" {
				tracker.setReqPath(opts.reqPath)
			} else {
				tracker.setReqPath(r.URL.RequestURI())
			}
		} else {
			dlTrackMu.Lock()
			tracker.TotalSegs = totalSegs
			tracker.TotalBytes = totalSize
			tracker.Title = opts.filename
			dlTrackMu.Unlock()
		}
		resumed := startOffset > 0
		if resumed {
			tracker.setResumeInit(0, startOffset)
		}
		tracker.setCancel(dlCancel)
		setAttachHeaders(tracker.ID, resumed, startOffset)
		if totalSize > startOffset {
			w.Header().Set("content-length", strconv.FormatInt(totalSize-startOffset, 10))
		}
		w.WriteHeader(200)

		flusher, _ := w.(http.Flusher)
		if _, werr := w.Write(firstData); werr != nil {
			tracker.finish("interrupted", "连接已断开，可在下载管理中继续")
			return nil
		}
		writtenTotal := startOffset + int64(len(firstData))
		tracker.update(1, writtenTotal)
		if flusher != nil {
			flusher.Flush()
		}

		fetchChunk := func(cs, ce int64) ([]byte, error) {
			var lastErr error
			for n := 0; n < 7; n++ {
				if dlCtx.Err() != nil || tracker.isCancelled() {
					return nil, context.Canceled
				}
				cCtx, cCancel := context.WithTimeout(dlCtx, 65*time.Second)
				target, err := y2SignedURL(rawURL, id)
				if err != nil {
					cCancel()
					return nil, err
				}
				req, err := http.NewRequestWithContext(cCtx, "GET", target, nil)
				if err != nil {
					cCancel()
					return nil, err
				}
				req.Header.Set("User-Agent", y2UA)
				req.Header.Set("Referer", y2Base+"/")
				req.Header.Set("Accept", "*/*")
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", cs, ce))
				resp, err := y2StreamClient.Do(req)
				if err == nil && resp != nil {
					if resp.StatusCode == 206 || resp.StatusCode == 200 {
						b, rerr := readBodyStallSafe(cCtx, cCancel, resp.Body, 25*time.Second, chunkSize+1<<20)
						cCancel()
						wantLen := int(ce - cs + 1)
						if rerr == nil && len(b) == wantLen {
							return b, nil
						}
						if rerr == nil {
							rerr = fmt.Errorf("分块长度不匹配 %d/%d", len(b), wantLen)
						}
						lastErr = rerr
					} else {
						code := resp.StatusCode
						_ = resp.Body.Close()
						cCancel()
						lastErr = fmt.Errorf("HTTP %d", code)
						if code == 403 || code == 429 || code >= 500 {
							if !sleepCtx(dlCtx, 800*(n+1)) {
								return nil, context.Canceled
							}
							continue
						}
					}
				} else {
					cCancel()
					lastErr = err
				}
				if !sleepCtx(dlCtx, 350*(n+1)) {
					return nil, context.Canceled
				}
			}
			if lastErr == nil {
				lastErr = fmt.Errorf("分块获取失败")
			}
			return nil, lastErr
		}

		type chunkRes struct {
			idx int
			buf []byte
			err error
		}
		const maxWorkers = 3
		const winSize = 5
		readyMap := make(map[int][]byte, winSize+2)
		nextAssign, nextWrite, inFlight := 0, 0, 0
		resCh := make(chan chunkRes, winSize+2)
		gone := false
		var fetchErr error

		for nextWrite < len(chunkStarts) {
			if dlCtx.Err() != nil || tracker.isCancelled() || gone {
				gone = true
				break
			}
			for nextAssign < len(chunkStarts) && inFlight < maxWorkers && nextAssign-nextWrite < winSize {
				i := nextAssign
				nextAssign++
				inFlight++
				go func(ci int, cs, ce int64) {
					b, e := fetchChunk(cs, ce)
					resCh <- chunkRes{idx: ci, buf: b, err: e}
				}(i, chunkStarts[i], chunkEnds[i])
			}
			if inFlight == 0 {
				break
			}
			cr := <-resCh
			inFlight--
			if cr.err != nil {
				fetchErr = cr.err
				gone = true
				break
			}
			readyMap[cr.idx] = cr.buf
			for nextWrite < len(chunkStarts) {
				b, ok := readyMap[nextWrite]
				if !ok {
					break
				}
				delete(readyMap, nextWrite)
				if _, werr := w.Write(b); werr != nil {
					gone = true
					break
				}
				writtenTotal += int64(len(b))
				nextWrite++
				tracker.update(1+nextWrite, writtenTotal)
				if flusher != nil {
					flusher.Flush()
				}
			}
			if gone {
				break
			}
		}
		if tracker.isCancelled() {
			tracker.finish("cancelled", "用户已取消")
		} else if gone || fetchErr != nil || dlCtx.Err() != nil {
			msg := "连接已断开，可在下载管理中继续"
			if fetchErr != nil && !errors.Is(fetchErr, context.Canceled) {
				msg = "分块中断（" + fetchErr.Error() + "），已保留断点"
			}
			tracker.finish("interrupted", msg)
		} else {
			tracker.markDone()
		}
		return nil
	}

	// 回落模式（上游不支持 206 分块，或未返回总长度）
	defer firstResp.Body.Close()
	totalBytes, _ := strconv.ParseInt(firstResp.Header.Get("content-length"), 10, 64)
	resumed := startOffset > 0 && firstResp.StatusCode == 206
	if resumed && totalBytes > 0 {
		totalBytes += startOffset
	}
	tracker := opts.extTracker
	if tracker == nil {
		tracker = dlTrackBindOrStart(opts.queueID, "y2", opts.filename, 1, totalBytes)
		if opts.reqPath != "" {
			tracker.setReqPath(opts.reqPath)
		} else {
			tracker.setReqPath(r.URL.RequestURI())
		}
	} else {
		dlTrackMu.Lock()
		tracker.TotalSegs = 1
		tracker.TotalBytes = totalBytes
		tracker.Title = opts.filename
		dlTrackMu.Unlock()
	}
	if resumed {
		tracker.setResumeInit(0, startOffset)
	}
	tracker.setCancel(dlCancel)
	setAttachHeaders(tracker.ID, resumed, startOffset)
	if cl := firstResp.Header.Get("content-length"); cl != "" {
		w.Header().Set("content-length", cl)
	}
	w.WriteHeader(200)
	_, copyErr := io.CopyBuffer(&dlTrackWriter{w: w, tracker: tracker}, firstResp.Body, make([]byte, 64*1024))
	if tracker.isCancelled() {
		tracker.finish("cancelled", "用户已取消")
	} else if copyErr != nil || dlCtx.Err() != nil {
		tracker.finish("interrupted", "连接已断开，可在下载管理中继续")
	} else {
		tracker.markDone()
	}
	return nil
}

// y2ProxyPlay /play/ 转发：透传 Range；m3u8 会把分片地址改回本代理（每片现签 + 缓存 + 预取首片）
func y2ProxyPlay(w http.ResponseWriter, r *http.Request, rawURL string, id *y2Identity, opts y2ProxyOpts) error {
	if opts.filename != "" {
		return y2DownloadMP4(w, r, rawURL, id, opts)
	}

	lowPath := strings.ToLower(strings.Split(rawURL, "?")[0])
	isPlaylist := strings.HasSuffix(lowPath, ".m3u8")
	rg := r.Header.Get("Range")

	if isPlaylist {
		if text, ok := playlistGet("y2:" + rawURL); ok {
			w.Header().Set("content-type", "application/vnd.apple.mpegurl")
			w.Header().Set("cache-control", "no-store")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(text))
			return nil
		}
	} else if rg == "" && !strings.HasSuffix(lowPath, ".mp4") {
		if hit := cacheGet("y2:" + rawURL); hit != nil {
			w.Header().Set("content-type", "video/mp2t")
			w.Header().Set("content-length", strconv.Itoa(len(hit)))
			w.Header().Set("cache-control", "public, max-age=3600")
			w.WriteHeader(200)
			_, _ = w.Write(hit)
			return nil
		}
	}

	var up *http.Response
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		target, serr := y2SignedURL(rawURL, id)
		if serr != nil {
			return serr
		}
		req, rerr := http.NewRequestWithContext(r.Context(), "GET", target, nil)
		if rerr != nil {
			return rerr
		}
		req.Header.Set("User-Agent", y2UA)
		req.Header.Set("Referer", y2Base+"/")
		req.Header.Set("Accept", "*/*")
		if rg != "" {
			req.Header.Set("Range", rg)
		}
		up, err = y2StreamClient.Do(req)
		if err == nil && up != nil {
			if up.StatusCode != 403 && up.StatusCode != 429 && up.StatusCode < 500 {
				break
			}
			if attempt == 0 {
				_ = up.Body.Close()
				up = nil
				if !sleepCtx(r.Context(), 220) {
					return r.Context().Err()
				}
				continue
			}
			break
		}
		if err != nil {
			if attempt == 0 && r.Context().Err() == nil && sleepCtx(r.Context(), 220) {
				continue
			}
			return err
		}
	}
	if up == nil {
		return err
	}
	defer up.Body.Close()

	baseURL := rawURL
	if i := strings.Index(baseURL, "#"); i >= 0 {
		baseURL = baseURL[:i]
	}
	var finalURL *url.URL
	if up.Request != nil && up.Request.URL != nil {
		finalURL = up.Request.URL
	}

	if isPlaylist && up.StatusCode == 200 {
		body, err := io.ReadAll(io.LimitReader(up.Body, y2SegMax))
		if err != nil {
			return err
		}
		text := string(body)
		lines := strings.Split(text, "\n")
		var out strings.Builder
		out.Grow(len(text) + 256)
		var prefetchURLs []string
		for i, line := range lines {
			t := strings.TrimSpace(line)
			switch {
			case t == "":
				out.WriteString(line)
			case strings.HasPrefix(t, "#"):
				out.WriteString(reURIAttr.ReplaceAllStringFunc(line, func(m string) string {
					sub := reURIAttr.FindStringSubmatch(m)
					if len(sub) < 2 {
						return m
					}
					uAbs := y2ResolvePlaylistRef(sub[1], baseURL, finalURL)
					if strings.HasPrefix(strings.ToUpper(t), "#EXT-X-MAP") && len(prefetchURLs) < 1 {
						prefetchURLs = append(prefetchURLs, uAbs)
					}
					return `URI="/y2play?u=` + enc(uAbs) + `"`
				}))
			default:
				uAbs := y2ResolvePlaylistRef(t, baseURL, finalURL)
				if !strings.HasSuffix(strings.ToLower(strings.Split(uAbs, "?")[0]), ".m3u8") && len(prefetchURLs) < 1 {
					prefetchURLs = append(prefetchURLs, uAbs)
				}
				out.WriteString("/y2play?u=" + enc(uAbs))
			}
			if i < len(lines)-1 {
				out.WriteString("\n")
			}
		}
		outText := out.String()
		if strings.Contains(text, "#EXT") {
			playlistPut("y2:"+rawURL, outText)
		}
		if len(prefetchURLs) > 0 {
			go y2PrefetchSegs(prefetchURLs, id)
		}
		w.Header().Set("content-type", "application/vnd.apple.mpegurl")
		w.Header().Set("cache-control", "no-store")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(outText))
		return nil
	}

	pass := []string{"content-type", "content-length", "content-range", "accept-ranges", "last-modified", "etag"}
	for _, k := range pass {
		if v := up.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.Header().Set("cache-control", "public, max-age=3600")

	status := up.StatusCode
	if status == 0 {
		status = 200
	}
	w.WriteHeader(status)

	if rg == "" && status == 200 && !strings.HasSuffix(lowPath, ".mp4") {
		buf := make([]byte, 64*1024)
		var chunks [][]byte
		var totalBytes int
		clientGone := false
		flusher, _ := w.(http.Flusher)
		for {
			n, rerr := up.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					clientGone = true
					break
				}
				if totalBytes+n <= segCacheMax/4 {
					c := make([]byte, n)
					copy(c, buf[:n])
					chunks = append(chunks, c)
					totalBytes += n
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
			if rerr != nil {
				if rerr != io.EOF {
					clientGone = true
				}
				break
			}
			if r.Context().Err() != nil {
				clientGone = true
				break
			}
		}
		if !clientGone && len(chunks) > 0 && totalBytes <= segCacheMax/4 {
			cachePut("y2:"+rawURL, joinBytes(chunks))
		}
		return nil
	}
	_, _ = io.CopyBuffer(w, up.Body, make([]byte, 64*1024))
	return nil
}

func y2PrefetchSegs(urls []string, id *y2Identity) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	for _, u := range urls {
		ckey := "y2:" + u
		if cacheGet(ckey) != nil {
			continue
		}
		prefetchMu.Lock()
		if prefetchInFlight[ckey] {
			prefetchMu.Unlock()
			continue
		}
		if len(prefetchInFlight) > 128 {
			prefetchInFlight = map[string]bool{}
		}
		prefetchInFlight[ckey] = true
		prefetchMu.Unlock()

		if target, err := y2SignedURL(u, id); err == nil {
			if req, err := http.NewRequestWithContext(ctx, "GET", target, nil); err == nil {
				req.Header.Set("User-Agent", y2UA)
				req.Header.Set("Referer", y2Base+"/")
				if up, err := y2StreamClient.Do(req); err == nil && up != nil {
					if up.StatusCode == http.StatusOK {
						if b, rerr := io.ReadAll(io.LimitReader(up.Body, segCacheMax/4)); rerr == nil && len(b) > 0 {
							cachePut(ckey, b)
						}
					}
					_ = up.Body.Close()
				}
			}
		}
		prefetchMu.Lock()
		delete(prefetchInFlight, ckey)
		prefetchMu.Unlock()
	}
}

type dlTrackWriter struct {
	w       io.Writer
	tracker *dlTrackItem
}

func (tw *dlTrackWriter) Write(p []byte) (int, error) {
	n, err := tw.w.Write(p)
	if n > 0 {
		tw.tracker.addBytes(int64(n))
	}
	return n, err
}

// ---------------------------------------------------------------- 课程库（爬取 / 查看）

// handleY2Catalog 看本地课程库的状态（不需要登录）
func handleY2Catalog(w http.ResponseWriter, r *http.Request) {
	cat := y2CatalogLoad()
	out := map[string]any{"ok": true, "file": y2CatFile()}
	if y2CatPath != "" {
		out["file"] = y2CatPath
	}
	if cat == nil {
		out["count"] = 0
		out["hint"] = "还没有本地课程库：登录新版后访问 /api/y2/crawl 爬一份"
		writeJSON(w, 200, out)
		return
	}
	out["count"] = len(cat.Rows)
	out["at"] = time.Unix(cat.At, 0).Format("2006-01-02 15:04:05")
	out["ageHours"] = int(time.Since(time.Unix(cat.At, 0)).Hours())
	out["crawling"] = atomic.LoadInt32(&y2CatRunning) == 1
	head := cat.Rows
	if len(head) > 5 {
		head = head[:5]
	}
	out["sample"] = head
	writeJSON(w, 200, out)
}

// handleY2Crawl 把新版门户的整份课程列表爬下来存成本地索引
func handleY2Crawl(w http.ResponseWriter, r *http.Request) {
	jwt := y2Token(r)
	if jwt == "" {
		writeJSON(w, 401, map[string]any{"ok": false, "error": "尚未登录 Yanhe 2.0", "needLogin": true})
		return
	}
	if y2Who(jwt) == nil {
		writeJSON(w, 401, map[string]any{"ok": false, "error": "登录已失效，请重新登录新版", "needLogin": true})
		return
	}
	perPage, maxPages := 100, 300
	if n, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("per_page"))); err == nil && n > 0 {
		perPage = n
	}
	if n, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("pages"))); err == nil && n > 0 {
		maxPages = n
	}
	t0 := time.Now()
	cat, stats, err := y2Crawl(jwt, perPage, maxPages, nil)
	if err != nil {
		code := 502
		if strings.Contains(err.Error(), "登录") {
			code = 401
		}
		writeJSON(w, code, map[string]any{"ok": false, "error": err.Error(), "stats": stats})
		return
	}
	stats["saveError"] = nil
	if saved, e := y2CatalogSave(cat); e != nil {
		stats["saveError"] = e.Error()
	} else {
		stats["file"] = saved
	}
	stats["ms"] = time.Since(t0).Milliseconds()
	head := cat.Rows
	if len(head) > 8 {
		head = head[:8]
	}
	writeJSON(w, 200, map[string]any{"ok": true, "stats": stats, "sample": head})
}

// ---------------------------------------------------------------- 自检

// y2Diag 自检：登录后打开 /diag，能看到每个接口的真实状态与字段名（不含 token / 手机号）
func handleY2Diag(w http.ResponseWriter, r *http.Request) {
	jwt := y2Token(r)
	id := y2Who(jwt)

	report := map[string]any{
		"at":       time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"loggedIn": id != nil,
		"steps":    []any{},
	}
	steps := []any{}

	step := func(name string, fn func() (map[string]any, error)) {
		t0 := time.Now()
		extra, err := fn()
		ms := time.Since(t0).Milliseconds()
		if err != nil {
			steps = append(steps, map[string]any{"name": name, "ok": false, "ms": ms, "error": err.Error()})
			return
		}
		entry := map[string]any{"name": name, "ok": true, "ms": ms}
		for k, v := range extra {
			entry[k] = v
		}
		steps = append(steps, entry)
	}

	if id != nil {
		step("账号", func() (map[string]any, error) {
			return map[string]any{
				"userId": id.userId, "tenantId": id.tenantId,
				"hasAccount": id.account != "",
			}, nil
		})

		step("学期列表", func() (map[string]any, error) {
			raw, err := y2Req("GET", y2APIURL("/vlabpassportapi/v1/course/term-list", nil), jwt)
			if err != nil {
				return nil, err
			}
			terms, _ := y2TermList(jwt)
			if terms == nil {
				terms = []y2Term{}
			}
			sh := map[string]any{"parse": "fail"}
			if raw.JSON != nil {
				sh = y2Shape(raw.JSON)
			}
			head := terms
			if len(head) > 12 {
				head = head[:12]
			}
			return map[string]any{
				"http": raw.Status, "shape": sh,
				"terms": head, "termCount": len(terms),
			}, nil
		})

		foundCourses := []y2Course{}
		step("课程检索「数值分析」", func() (map[string]any, error) {
			raw, err := y2Req("GET", y2APIURL(y2CourseListPath, y2Params{
				y2P("title", "数值分析"), y2P("page", 1), y2P("per_page", 20), y2P("tenant", y2Tenant),
			}), jwt)
			if err != nil {
				return nil, err
			}
			var sh map[string]any
			if raw.JSON != nil {
				sh = y2Shape(raw.JSON)
			}
			out := map[string]any{"http": raw.Status, "shape": sh}
			if r0, e := y2SearchCourses(jwt, "数值分析", "", 1, 20); e == nil && r0 != nil {
				foundCourses = r0.courses
			} else if e != nil {
				out["err"] = e.Error()
			}
			fields := []map[string]any{}
			for i, c := range foundCourses {
				if i >= 5 {
					break
				}
				fields = append(fields, map[string]any{
					"id": c["courseId"], "title": y2RuneCut(jstr(c["title"]), 18),
					"termId": c["termId"], "termName": c["termName"], "节次数": c["sessions"],
				})
			}
			out["count"] = len(foundCourses)
			out["学期字段"] = fields
			return out, nil
		})

		// 关键诊断：学期时间窗 + 扫课表到底扫到多少
		step("学期时间窗 + 扫课表", func() (map[string]any, error) {
			termNames := map[string]string{}
			terms := []y2Term{}
			if ts, e := y2TermList(jwt); e == nil {
				terms = ts
				for _, t := range ts {
					termNames[t.ID] = t.Name
				}
			}
			windows := y2WindowsFor(foundCourses, termNames, 2)
			headTerms := terms
			if len(headTerms) > 10 {
				headTerms = headTerms[:10]
			}
			pending := []map[string]any{}
			for _, w := range windows {
				pending = append(pending, map[string]any{
					"学期": w.terms, "起止": w.from + " ~ " + w.to,
				})
			}
			scanned := []map[string]any{}
			for _, w := range windows {
				mp, _ := y2ScanWindows(jwt, "数值分析", []y2WindowFull{w})
				nRows, nCourses := 0, 0
				for _, v := range mp {
					nCourses++
					nRows += len(v)
				}
				scanned = append(scanned, map[string]any{
					"学期": w.terms, "窗口": w.from + " ~ " + w.to,
					"扫到的课程数": nCourses, "扫到的讲次数": nRows,
				})
			}
			return map[string]any{"学期表": headTerms, "待扫窗口": pending, "扫描结果": scanned}, nil
		})

		// 关键诊断：前 3 门课的讲次接口到底返回了什么
		for i, c := range foundCourses {
			if i >= 3 {
				break
			}
			cc := c
			step("讲次 courseId="+jstr(cc["courseId"])+" 《"+y2RuneCut(jstr(cc["title"]), 16)+"》", func() (map[string]any, error) {
				out := map[string]any{"课程库节次数": cc["sessions"]}
				// 1) 原样请求，记录 HTTP + code/msg + 结构
				raw, err := y2Req("GET", y2APIURL(y2DetailPathConst, y2Params{
					y2P("course_id", jstr(cc["courseId"])), y2P("student", id.account),
				}), jwt)
				if err != nil {
					return nil, err
				}
				entry := map[string]any{"http": raw.Status, "变体": "student"}
				if raw.JSON != nil {
					entry["shape"] = y2Shape(raw.JSON)
				} else {
					entry["shape"] = map[string]any{"parse": "fail"}
				}
				var d *y2Detail
				if v, e := y2CourseDetail(jwt, jstr(cc["courseId"]), id.account); e == nil {
					d = v
				} else {
					entry["err"] = e.Error()
				}
				statuses := map[string]int{}
				nSessions := 0
				hasRoom := false
				if d != nil {
					nSessions = len(d.sessions)
					for _, s := range d.sessions {
						k := s.Status
						if k == "" {
							k = "(空)"
						}
						statuses[k]++
						if s.Room != "" {
							hasRoom = true
						}
					}
				}
				entry["解析出讲次"] = nSessions
				entry["状态分布"] = statuses
				entry["有教室名"] = hasRoom
				// Node 是拿第一条讲次的 Object.keys 当"字段名清单"
				fields := []string{}
				if nSessions > 0 {
					fields = []string{"subId", "title", "startAt", "duration", "status", "room",
						"teacher", "type", "kind", "termId", "termName", "date"}
				}
				entry["讲次字段"] = fields
				out["detail"] = entry

				// 2) 有没有录像 + 教室
				var v *y2CourseVideosResult
				if got, e := y2CourseVideos(jwt, jstr(cc["courseId"]), id.account); e == nil {
					v = got
				} else {
					out["录像判定错误"] = e.Error()
				}
				sessions := []y2Session{}
				room := ""
				if v != nil {
					sessions = v.sessions
					room = v.room
				}
				out["有录像"] = len(sessions)
				out["教室"] = room
				sample := sessions
				if len(sample) > 3 {
					sample = sample[:3]
				}
				out["抽检"] = sample

				// 3) 最新一讲取流是否真的有文件
				if len(sessions) > 0 {
					first := sessions[0]
					raw2, err2 := y2Req("GET", y2APIURL("/courseapi/v3/portal-home-setting/get-sub-info", y2Params{
						y2P("course_id", jstr(cc["courseId"])), y2P("sub_id", first.SubID),
					}), jwt)
					if err2 != nil {
						return nil, err2
					}
					sh := map[string]any{"parse": "fail"}
					if raw2.JSON != nil {
						sh = y2Shape(raw2.JSON)
					}
					info, ierr := y2SubInfoOf(jwt, jstr(cc["courseId"]), first.SubID)
					nStreams := 0
					iroom := ""
					if ierr == nil && info != nil {
						nStreams = len(info.Streams)
						iroom = info.Room
					}
					got := map[string]any{"http": raw2.Status, "shape": sh, "画面数": nStreams, "教室": iroom}
					if ierr != nil {
						got["err"] = ierr.Error()
					}
					out["取流"] = got
				}
				return out, nil
			})
		}

		// 4) 搜索变体：同一个词换各种参数名，看哪种真做「包含匹配」
		dq := strings.TrimSpace(r.URL.Query().Get("q"))
		if dq == "" {
			dq = "大学物理"
		}
		step("搜索变体「"+dq+"」", func() (map[string]any, error) {
			vars := y2DiagSearch(jwt, dq)
			// 顺带把「大学物理」这种短词在上游的返回条数记下来，方便判断是精确还是包含
			return map[string]any{"variants": vars}, nil
		})

		// 5) 指定课（?course=xxx）的真实讲次情况
		if cid := strings.TrimSpace(r.URL.Query().Get("course")); cid != "" {
			step("指定课讲次（"+cid+"）", func() (map[string]any, error) {
				return y2DiagCourse(jwt, cid, id.account), nil
			})
		}
	}

	report["steps"] = steps

	if r.URL.Path == "/diag" {
		b := y2JSONIndent(report)
		var h strings.Builder
		h.WriteString(`<!doctype html><meta charset="utf-8"><title>自检</title>`)
		h.WriteString(`<style>body{background:#0f1115;color:#e8eaed;font:13px/1.7 ui-monospace,Consolas,monospace;padding:20px}`)
		h.WriteString(`pre{white-space:pre-wrap;word-break:break-word}</style>`)
		h.WriteString(`<h3 style="font-family:system-ui">接口自检（把这一整段发给开发者即可，里面没有你的 token）</h3>`)
		h.WriteString("<pre>" + strings.ReplaceAll(b, "<", "&lt;") + "</pre>")
		w.Header().Set("content-type", "text/html; charset=utf-8")
		w.Header().Set("cache-control", "no-store")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(h.String()))
		return
	}
	writeJSON(w, 200, report)
}

// y2Shape 只描述结构，不含具体值（自检用）
func y2Shape(j any) map[string]any {
	m := jobj(j)
	if m == nil {
		return map[string]any{"type": y2TypeName(j)}
	}
	o := map[string]any{}
	keys := y2Keys(m)
	if len(keys) > 25 {
		keys = keys[:25]
	}
	o["keys"] = keys
	if v, ok := m["code"]; ok {
		o["code"] = v
	}
	if v, ok := m["msg"]; ok {
		o["msg"] = y2RuneCut(jstr(v), 120)
	}
	if v, ok := m["message"]; ok {
		o["message"] = y2RuneCut(jstr(v), 120)
	}
	d := jobj(j2get(m, "data"))
	if d != nil {
		dk := y2Keys(d)
		if len(dk) > 30 {
			dk = dk[:30]
		}
		o["dataKeys"] = dk
	}
	cands := []any{
		j2get(d, "list"), d, j2get(m, "list"),
		j2get(jobj(j2get(m, "params")), "list"),
		j2get(jobj(j2get(m, "result")), "data"),
	}
	for _, c := range cands {
		var a []any
		if arr, ok := c.([]any); ok {
			a = arr
		} else if cm := jobj(c); cm != nil {
			a = y2Arr(cm)
		}
		if len(a) > 0 {
			if first := jobj(a[0]); first != nil {
				ik := y2Keys(first)
				if len(ik) > 40 {
					ik = ik[:40]
				}
				o["itemKeys"] = ik
				o["count"] = len(a)
				break
			}
		}
	}
	return o
}

// y2TypeName 近似 JS 的 typeof（自检报告里用）
func y2TypeName(v any) string {
	switch v.(type) {
	case nil:
		return "object"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64, int, int64:
		return "number"
	}
	return "object"
}

// y2RuneCut 按字符截断（Node 的 slice(0,n)）
func y2RuneCut(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

// y2JSONIndent 缩进 JSON（/diag 页面用）。
// 关掉 HTML 转义：Node 的 JSON.stringify 不会把 < > & 变成 \u003c
func y2JSONIndent(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimRight(buf.String(), "\n")
}
