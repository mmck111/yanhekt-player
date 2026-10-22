// 新版平台的"课程库"：把门户的课程列表整份爬下来，存成本地索引，搜索全在本地做。
//
// 为什么要这么做：
//   - 新版检索接口（get-course-list）的 title 是精确/前缀匹配，写法差一个字就查不到；
//   - 逐门课去问"有没有录像"会被限流，量一大就整片变成"没视频"；
//   - 而门户的 get-all-list 本来就是分页的整表，一次爬完（几千门课）之后，
//     搜课程名 / 老师名 / 课程号都是本地匹配，又快又准，还能做包含匹配和归一化。
//
// 索引落在 %LOCALAPPDATA%\yanhekt\y2-catalog.json.gz，跟 exe 分开发放，
// 过期了再爬一遍就行（默认 7 天）。
package main

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// y2CatListPath 门户"课程中心"整表（分页）
const y2CatListPath = "/courseapi/v3/portal-home-setting/get-all-list"

// y2CatRow 课程库里的一行
type y2CatRow struct {
	ID      string `json:"id"`
	Title   string `json:"t"`
	Teacher string `json:"r"`
	Term    string `json:"m"`
	Code    string `json:"c"`
	Module  string `json:"g"`
	Subs    int    `json:"s"` // 节次数，0 表示接口没给
	Year    int    `json:"y,omitempty"`
}

// y2Catalog 整份索引
type y2Catalog struct {
	Ver    int        `json:"v"`
	At     int64      `json:"at"` // 爬取时间（unix 秒）
	Tenant int        `json:"tenant"`
	Rows   []y2CatRow `json:"rows"`
}

var (
	y2CatMu      sync.Mutex
	y2CatCache   *y2Catalog
	y2CatAt      time.Time
	y2CatErr     string
	y2CatRunning int32
)

// y2CatMaxAge 课程库多久算过期（过期后登录时自动重爬）
const y2CatMaxAge = 7 * 24 * time.Hour

// y2CatalogKick 登录成功后调一次：库里没东西或太旧就在后台爬一遍，不挡登录
func y2CatalogKick(jwt string) {
	if jwt == "" || atomic.LoadInt32(&y2CatRunning) == 1 {
		return
	}
	if cat := y2CatalogLoad(); cat != nil && len(cat.Rows) > 0 && time.Since(time.Unix(cat.At, 0)) < y2CatMaxAge {
		return
	}
	atomic.StoreInt32(&y2CatRunning, 1)
	go func() {
		defer atomic.StoreInt32(&y2CatRunning, 0)
		cat, _, err := y2Crawl(jwt, 100, 300, nil)
		if err != nil {
			return
		}
		_, _ = y2CatalogSave(cat)
	}()
}

// y2CatFile 索引文件路径
func y2CatFile() string {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "yanhekt", "y2-catalog.json.gz")
}

// y2CatWritePaths 写索引时可以尝试的路径（按优先级）。
// 有些环境不允许往 %LOCALAPPDATA% 写（被杀软/策略拦住），这时依次退到临时目录、exe 旁边。
func y2CatWritePaths(target string) []string {
	base := filepath.Base(target)
	dirs := []string{filepath.Dir(y2CatFile())}
	if t := os.TempDir(); t != "" {
		dirs = append(dirs, filepath.Join(t, "yanhekt"))
	}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), ".yanhekt-cache"))
	}
	out := []string{}
	seen := map[string]bool{}
	for _, d := range dirs {
		p := filepath.Join(d, base)
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// y2CatReadPaths 读索引时按优先级找：哪个存在用哪个
func y2CatReadPaths() []string { return y2CatWritePaths(y2CatFile()) }

// y2CatalogLoad 读索引（内存缓存，文件变了会重新读）。
// 依次在几个候选路径里找最新的那份：有的机器不让写 %LOCALAPPDATA%，索引会落到临时目录。
func y2CatalogLoad() *y2Catalog {
	path, st := "", os.FileInfo(nil)
	for _, p := range y2CatReadPaths() {
		if fi, err := os.Stat(p); err == nil {
			if st == nil || fi.ModTime().After(st.ModTime()) {
				path, st = p, fi
			}
		}
	}
	if path == "" || st == nil {
		return nil
	}
	y2CatMu.Lock()
	defer y2CatMu.Unlock()
	if y2CatCache != nil && st.ModTime().Equal(y2CatAt) {
		return y2CatCache
	}
	f, err := os.Open(path)
	if err != nil {
		return y2CatCache
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return y2CatCache
	}
	defer zr.Close()
	var cat y2Catalog
	if err := json.NewDecoder(zr).Decode(&cat); err != nil || cat.Ver != 1 {
		y2CatErr = "索引文件坏了：" + err2str(err)
		return y2CatCache
	}
	y2CatCache, y2CatAt = &cat, st.ModTime()
	y2CatPath = path
	return y2CatCache
}

// y2CatPath 当前实际读到的索引路径（给 /api/y2/catalog 显示）
var y2CatPath string

func err2str(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// y2CatalogSave 写索引（先写临时文件再改名，中途失败不会留下半个库）。
// 返回真正写成功的路径。
func y2CatalogSave(cat *y2Catalog) (string, error) {
	var lastErr error
	for _, p := range y2CatWritePaths(y2CatFile()) {
		if err := writeGzipJSON(p, cat); err != nil {
			lastErr = err
			continue
		}
		y2CatMu.Lock()
		y2CatCache, y2CatAt = cat, time.Now()
		y2CatMu.Unlock()
		y2CatPath = p
		return p, nil
	}
	if lastErr == nil {
		lastErr = errors.New("没有可写的目录")
	}
	return "", lastErr
}

// y2CatFromItem 列表里的一条 → 课程库行
func y2CatFromItem(it map[string]any) *y2CatRow {
	if it == nil {
		return nil
	}
	id := y2PickStr(it, "course_id", "id", "courseId")
	if id == "" {
		return nil
	}
	title := y2PickStr(it, "title", "course_title", "name")
	if title == "" {
		return nil
	}
	teacher := y2PickStr(it, "teacher")
	if teacher == "" {
		teacher = y2PickStr(it, "realname", "lecturer_name")
	}
	// 注意：列表里的 public_subs 是"有没有公开讲次"的 0/1 标记，不是节次数，
	// 当成讲次数用会让"2 门课只显示 1 个视频"之类的问题重演，所以只认 sub_count。
	subs := 0
	if v := y2Pick(it, "sub_count", "subject_count"); v != nil {
		subs = y2Num(v)
	}
	return &y2CatRow{
		ID:      id,
		Title:   title,
		Teacher: teacher,
		Term:    y2PickStr(it, "term", "term_name"),
		Code:    y2PickStr(it, "course_code", "serial_number"),
		Subs:    subs,
	}
}

// y2CatItems 从一页响应里取课程数组（data 可能是数组，也可能挂在 list / rows 下）
func y2CatItems(body any) []any {
	b := jobj(body)
	if b == nil {
		return nil
	}
	raw := j2get(b, "data")
	if arr, ok := raw.([]any); ok {
		return arr
	}
	d := jobj(raw)
	if d == nil {
		return nil
	}
	for _, k := range []string{"list", "data", "rows", "items"} {
		if arr, ok := d[k].([]any); ok {
			return arr
		}
	}
	return nil
}

// y2Modules 门户首页的模块（公开接口，不需要登录）
func y2Modules() []map[string]any {
	body, err := y2GetJSON("", "/cmsapi/v1/portal-setting/get-tenant-modules", y2Params{
		y2P("tenant", y2Tenant),
	})
	if err != nil {
		return nil
	}
	b := jobj(body)
	d := jobj(j2get(b, "data"))
	if d == nil {
		return nil
	}
	out := []map[string]any{}
	for _, it := range y2Arr(d["list"]) {
		if m := jobj(it); m != nil {
			out = append(out, m)
		}
	}
	return out
}

// y2CrawlProgress 爬取时的进度（给前端/命令行看）
type y2CrawlProgress struct {
	Module  string `json:"module"`
	Page    int    `json:"page"`
	Got     int    `json:"got"`
	Total   int    `json:"total"`
	Message string `json:"message"`
}

// y2Crawl 把整份课程列表爬下来。
//
// perPage 一次多少条（上游常见上限 100），maxPages 每个模块最多翻多少页（防止死循环）。
func y2Crawl(jwt string, perPage, maxPages int, progress func(y2CrawlProgress)) (*y2Catalog, map[string]any, error) {
	if perPage <= 0 || perPage > 200 {
		perPage = 100
	}
	if maxPages <= 0 {
		maxPages = 300
	}
	mods := y2Modules()
	codes := []string{}
	for _, m := range mods {
		code := y2Text(m["code"])
		if code == "" {
			continue
		}
		if t := y2Text(m["type"]); t != "" && t != "course" {
			continue
		}
		codes = append(codes, code)
	}
	if len(codes) == 0 {
		codes = []string{"mooc", "multi"}
	}

	seen := map[string]bool{}
	rows := []y2CatRow{}
	stats := map[string]any{}
	modStat := map[string]int{}
	authFailed := 0
	for _, code := range codes {
		got := 0
		for page := 1; page <= maxPages; page++ {
			raw, err := y2Req("GET", y2APIURL(y2CatListPath, y2Params{
				y2P("tenant", y2Tenant), y2P("sort", ""), y2P("search", ""),
				y2P("type", code), y2P("page", page), y2P("per_page", perPage),
				y2P("category_id", ""),
			}), jwt)
			if err != nil {
				return nil, stats, err
			}
			if raw.Status == 403 || raw.Status == 401 {
				authFailed++
				break
			}
			items := y2CatItems(raw.JSON)
			if len(items) == 0 {
				break
			}
			added := 0
			for _, it := range items {
				r := y2CatFromItem(jobj(it))
				if r == nil || seen[r.ID] {
					continue
				}
				r.Module = code
				seen[r.ID] = true
				rows = append(rows, *r)
				added++
			}
			got += added
			if progress != nil && (page <= 3 || page%10 == 0) {
				progress(y2CrawlProgress{Module: code, Page: page, Got: got, Total: len(rows), Message: "正在爬取"})
			}
			if added == 0 && page > 1 {
				break // 全是重复/空页 → 这个模块到底了
			}
			if len(items) < perPage {
				break
			}
			time.Sleep(120 * time.Millisecond) // 别把上游打疼
		}
		modStat[code] = got
	}
	if len(rows) == 0 {
		if authFailed > 0 {
			return nil, stats, errors.New("课程列表需要登录后才能爬：请先在新版里登录")
		}
		return nil, stats, errors.New("课程列表没返回任何课程")
	}

	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	cat := &y2Catalog{Ver: 1, At: time.Now().Unix(), Tenant: y2Tenant, Rows: rows}
	stats["modules"] = modStat
	stats["count"] = len(rows)
	stats["file"] = y2CatFile()
	return cat, stats, nil
}

// y2CatYear 这门课大概属于哪个学年：优先看课号前缀（20262027… / 20261-02-…），其次看学期名。
// 注意：手建课程的课号是一串随机十六进制（38e406b8…），里面有"2099"这种假数字，
// 所以只认**前四位**是 20xx 的课号，别在整串里乱找，否则"大学物理实验 Ⅱ"会被排到 2099 年。
func y2CatYear(r y2CatRow) int {
	if r.Year > 1900 {
		return r.Year
	}
	if y := y2TermYear(r.Term); y >= 2019 && y <= 2030 {
		return y
	}
	if len(r.Code) >= 4 && r.Code[0] == '2' && r.Code[1] == '0' {
		n := 0
		ok := true
		for _, c := range r.Code[:4] {
			if c < '0' || c > '9' {
				ok = false
				break
			}
			n = n*10 + int(c-'0')
		}
		// 只认"像学年"的前缀：2019~2030。别的数字（2082、2051…）是课程内部编号，
		// 当真了会把「大学物理实验 Ⅱ」排到 2082 年，把真正要看的课挤下去。
		if ok && n >= 2019 && n <= 2030 {
			return n
		}
	}
	return 0
}

// y2CatalogSearch 本地匹配：每个词都要命中（课名 / 老师 / 课程号 / 课号），
// 归一化后比较（Ⅱ=II=2、全角半角、空格、括号）。
//
// 同一门课在不同学期会有多条记录，这里按「课名+老师」去重，只留最新学期那条，
// 免得搜一次出来几十条一模一样的课。
func y2CatalogSearch(cat *y2Catalog, q string, limit int) []y2CatRow {
	if cat == nil || len(cat.Rows) == 0 {
		return nil
	}
	tokens := strings.Fields(q)
	if len(tokens) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = 60
	}
	qn := y2NormText(q)

	// 1) 过滤 + 去重（同课名同老师只留学期最新的）
	type hit struct {
		row  y2CatRow
		norm string
		rank int
	}
	best := map[string]hit{}
	order := []string{}
	for _, r := range cat.Rows {
		hay := r.Title + " " + r.Teacher + " " + r.Code + " " + r.ID
		if !y2FuzzyAll(hay, tokens) {
			continue
		}
		norm := y2NormText(r.Title)
		rank := 2 // 包含
		switch {
		case norm == qn:
			rank = 0
		case strings.HasPrefix(norm, qn):
			rank = 1
		case y2FuzzyHas(r.Teacher, q):
			rank = 3 // 只命中老师
		}
		key := norm + "|" + y2NormText(r.Teacher)
		h := hit{row: r, norm: norm, rank: rank}
		if old, ok := best[key]; !ok {
			best[key] = h
			order = append(order, key)
		} else if y2CatYear(r) > y2CatYear(old.row) {
			best[key] = h
		}
	}

	hits := make([]hit, 0, len(order))
	for _, k := range order {
		hits = append(hits, best[k])
	}
	// 2) 排序：匹配方式 → 学年新的 → 节次多的 → 课名短的。
	// 学年必须排在课名长度前面：搜「大学物理」时「大学物理AⅡ」（本学期）要压过
	// 「大学物理E」（上一学年），否则用户要找的那门课会被一堆旧课挤下去。
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if ya, yb := y2CatYear(a.row), y2CatYear(b.row); ya != yb {
			return ya > yb
		}
		if a.row.Subs != b.row.Subs {
			return a.row.Subs > b.row.Subs
		}
		if li, lj := len([]rune(a.row.Title)), len([]rune(b.row.Title)); li != lj {
			return li < lj
		}
		return a.row.ID < b.row.ID
	})
	out := make([]y2CatRow, 0, limit)
	// 同一种课名最多占 4 个位置：搜「大学物理」时不能 12 条全是「大学物理AⅡ」，
	// 得让「大学物理AⅠ / E / 实验」也露脸，用户才知道还有别的写法可选。
	perTitle := map[string]int{}
	for _, h := range hits {
		if len(out) >= limit {
			break
		}
		k := y2NormText(h.row.Title)
		if perTitle[k] >= 4 {
			continue
		}
		perTitle[k]++
		out = append(out, h.row)
	}
	return out
}

// y2TermYear 从「2026-2027 第一学期」里取 2026；认不出来给 0
func y2TermYear(term string) int {
	digits := 0
	for _, r := range term {
		if r >= '0' && r <= '9' {
			digits = digits*10 + int(r-'0')
			if digits > 9999 {
				return 0
			}
			continue
		}
		if digits > 1900 {
			return digits
		}
		digits = 0
	}
	return 0
}

// y2CatRowCourse 课程库行 → 统一的课程条目（和 y2NormCourse 的字段对齐）
func y2CatRowCourse(r y2CatRow) y2Course {
	c := y2Course{
		"courseId": r.ID,
		"title":    r.Title,
		"teacher":  r.Teacher,
		"termId":   "",
		"termName": r.Term,
		"kind":     "record",
		"cover":    "",
		"room":     "",
		"verified": false,
		"source":   "catalog",
	}
	if r.Subs > 0 {
		c["sessions"] = r.Subs
	} else {
		// 课程库没给节次数时留 nil：null 表示"不知道"，不能当成"0 讲"
		c["sessions"] = nil
	}
	c["videoCount"] = nil
	return c
}
