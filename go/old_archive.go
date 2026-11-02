// 往年课程（旧平台）支持
//
// 2026-11 起旧平台把"往年课程"的 /v1/course 与 /v1/video 都挡了，统一返回
// 12121028「课程已过期」；但 /v1/course/session/list 仍然照常返回讲次，
// 而且每个讲次的 videos[] 里就带着 CDN 播放地址（main / vga）。
//
// 所以这里做两件事：
//  1. 课程详情被"过期"挡住时，改用讲次接口把课程拼出来（buildExpiredCourse）。
//  2. 名字用讲次编号里的课程代码，去新版课程库里反查（oldTitleByCode）。
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// oldExpiredCode 上游"课程已过期"的业务码
const oldExpiredCode = 12121028

// oldIsExpired 判断一次 /v1/course 的返回是不是"课程已过期"
func oldIsExpired(j map[string]any) bool {
	if j == nil {
		return false
	}
	if f, ok := j["code"].(float64); ok && int(f) == oldExpiredCode {
		return true
	}
	if jstr(j["code"]) == fmt.Sprint(oldExpiredCode) {
		return true
	}
	return strings.Contains(jstr(j["message"]), "过期")
}

// oldCodeFromNumber 讲次编号 → 9 位课程代码
//
// 编号形如 20212022110024520305：前 8 位学年、第 9 位学期、10~18 位课程代码、最后两位班号。
// 也有短编号（20211-24-2400186-1627541179636），取不到就返回空串。
func oldCodeFromNumber(num string) string {
	num = strings.TrimSpace(num)
	if len(num) >= 18 && isDigits(num[:18]) {
		return num[9:18]
	}
	return ""
}

// oldTermFromNumber 讲次编号 → 学年 / 学期
func oldTermFromNumber(num string) (string, int) {
	num = strings.TrimSpace(num)
	if len(num) >= 9 && isDigits(num[:9]) {
		year := num[:4] + "-" + num[4:8]
		sem := int(num[8] - '0')
		if sem == 1 || sem == 2 {
			return year, sem
		}
		return year, 0
	}
	return "", 0
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------- 代码 → 课程名

var (
	oldTitleMu   sync.Mutex
	oldTitleMap  map[string]string
	oldTitleFrom *y2Catalog
)

// oldTitleByCode 用课程代码在（本地）新版课程库里反查课程名。
// 老课程和新课程用的是同一套 9 位课程代码，所以 2021 年的课也能查到名字。
func oldTitleByCode(code string) string {
	if code == "" {
		return ""
	}
	cat := y2CatalogLoad()
	if cat == nil {
		return ""
	}
	oldTitleMu.Lock()
	defer oldTitleMu.Unlock()
	if oldTitleMap == nil || oldTitleFrom != cat {
		m := make(map[string]string, len(cat.Rows))
		for _, r := range cat.Rows {
			c := strings.TrimSpace(r.Code)
			if len(c) >= 18 && isDigits(c[:18]) {
				c = c[9:18]
			} else if len(c) < 9 {
				continue
			}
			if c == "" {
				continue
			}
			if _, ok := m[c]; !ok && r.Title != "" {
				m[c] = r.Title
			}
		}
		oldTitleMap, oldTitleFrom = m, cat
	}
	return oldTitleMap[code]
}

// ---------------------------------------------------------------- 过期课程

// buildExpiredCourse 课程详情被"过期"挡住时，用讲次接口把课程拼出来。
// 拿不到任何讲次就返回 nil（交给上层照常报错）。
func buildExpiredCourse(id, token string) *courseData {
	raw, err := rawSessions(id, token)
	if err != nil || len(raw) == 0 {
		return nil
	}

	sessions := make([]sessionRow, 0, len(raw))
	code, year, semester := "", "", 0
	for _, s := range raw {
		// 讲次里自带 videos[]：main / vga 就是 CDN 播放地址，不需要再问 /v1/video
		var v map[string]any
		if a := jarr(s["videos"]); len(a) > 0 {
			v = jobj(a[0])
		}
		if v == nil {
			if o := jobj(s["videos"]); o != nil {
				v = o
			}
		}
		var main, vga any
		var duration any
		if v != nil {
			if truthy(v["main"]) {
				main = jstr(v["main"])
			}
			if truthy(v["vga"]) {
				vga = jstr(v["vga"])
			}
			if truthy(v["duration"]) {
				duration = jnum(v["duration"])
			}
		}

		if code == "" {
			if c := oldCodeFromNumber(jstr(s["number"])); c != "" {
				code = c
			}
		}
		if year == "" {
			if y, sem := oldTermFromNumber(jstr(s["number"])); y != "" {
				year, semester = y, sem
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

	playable := 0
	for _, s := range sessions {
		if truthy(s.Main) || truthy(s.VGA) {
			playable++
		}
	}

	name := oldTitleByCode(code)
	if name == "" {
		if n, t := catalogCourseByID(id); n != "" {
			name = n
			if year == "" && len(t) >= 9 {
				year = t[:9] // "2021-2022 第一学期" → "2021-2022"
			}
		}
	}
	if name == "" {
		name = "往年课程 " + id
	}

	return &courseData{
		ok: true,
		course: &courseInfo{
			ID:         id,
			Name:       name,
			Code:       code,
			College:    oldProfessorCollege(id, token),
			Year:       year,
			Semester:   semester,
			Professors: oldProfessors(id, token),
		},
		sessions: sessions,
		stats:    statsInfo{Total: len(sessions), Playable: playable, Unresolved: len(sessions) - playable},
	}
}

// oldProfessorCache 老师信息（每门课一次请求，别每次都问）
type oldProfEntry struct {
	names []string
	col   string
}

var (
	oldProfMu    sync.Mutex
	oldProfCache = map[string]oldProfEntry{}
)

// oldProfessors / oldProfessorCollege 取老师与学院：/v1/course/professor/list 不受"过期"限制
func oldProfessors(id, token string) []string { return oldProfInfo(id, token).names }

func oldProfessorCollege(id, token string) string { return oldProfInfo(id, token).col }

func oldProfInfo(id, token string) oldProfEntry {
	oldProfMu.Lock()
	if hit, ok := oldProfCache[id]; ok {
		oldProfMu.Unlock()
		return hit
	}
	oldProfMu.Unlock()

	ent := oldProfEntry{names: []string{}}
	if j, err := apiCall("/v1/course/professor/list?course_id="+encodeURIComponent(id), token); err == nil {
		for _, it := range jarr(j["data"]) {
			o := jobj(it)
			if o == nil {
				continue
			}
			if n := jstr(o["name"]); n != "" && len(ent.names) < 5 {
				ent.names = append(ent.names, n)
			}
			if ent.col == "" {
				if c := jobj(o["college"]); c != nil {
					ent.col = jstr(c["name"])
				}
			}
		}
	}

	oldProfMu.Lock()
	if len(oldProfCache) > 500 {
		oldProfCache = map[string]oldProfEntry{}
	}
	oldProfCache[id] = ent
	oldProfMu.Unlock()
	return ent
}

// ---------------------------------------------------------------- 往年课程索引

// oldIndexRec 索引里的一行（由 tools/old-crawl（node 脚本）生成，一行一个 JSON）
type oldIndexRec struct {
	ID   int    `json:"id"`
	Num  string `json:"num"`
	N    int    `json:"n"`
	Date string `json:"date"`
	Code string `json:"code"`
	T    string `json:"t"`
}

// oldIndexFile 索引文件路径：和课程库、签名缓存放同一个用户目录
func oldIndexFile() string {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "yanhekt", "old-index.jsonl")
}

var (
	oldIndexMu   sync.Mutex
	oldIndexRows []oldIndexRec
	oldIndexMod  time.Time
)

// oldIndexLoad 读索引（按文件修改时间缓存，爬取中途也能读到最新的一段）
func oldIndexLoad() []oldIndexRec {
	path := oldIndexFile()
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	oldIndexMu.Lock()
	defer oldIndexMu.Unlock()
	if oldIndexRows != nil && st.ModTime().Equal(oldIndexMod) {
		return oldIndexRows
	}
	f, err := os.Open(path)
	if err != nil {
		return oldIndexRows
	}
	defer f.Close()
	rows := make([]oldIndexRec, 0, 4096)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r oldIndexRec
		if json.Unmarshal([]byte(line), &r) == nil && r.ID > 0 {
			rows = append(rows, r)
		}
	}
	oldIndexRows, oldIndexMod = rows, st.ModTime()
	return rows
}

// oldIndexCount 索引里有多少门课（给 UI 显示/判断是否值得搜）
func oldIndexCount() int { return len(oldIndexLoad()) }

// oldRecordTitle 一门往年课的名字：先用课程代码去新版课程库反查
func oldRecordTitle(r oldIndexRec) string {
	if t := oldTitleByCode(r.Code); t != "" {
		return t
	}
	return "往年课程 " + strconv.Itoa(r.ID)
}

// oldArchiveSearch 在往年课程索引里按课程名/代码找课，返回可直接并入搜索结果的 courseRow。
func oldArchiveSearch(q string, limit int) []courseRow {
	rows := oldIndexLoad()
	if len(rows) == 0 || limit <= 0 {
		return nil
	}
	key := strings.ToLower(strings.TrimSpace(q))
	key = strings.ReplaceAll(key, " ", "")
	out := make([]courseRow, 0, limit)
	for _, r := range rows {
		title := oldRecordTitle(r)
		hay := strings.ToLower(title)
		if key != "" && !strings.Contains(hay, key) && !strings.Contains(strings.ToLower(r.Code), key) {
			continue
		}
		year := ""
		if len(r.Date) >= 10 {
			year = r.Date[:4]
		}
		out = append(out, courseRow{
			ID:     strconv.Itoa(r.ID),
			Name:   title,
			Code:   strPtr(r.Code),
			Number: strPtr(r.Num),
			Term:   year,
			Source: "archive",
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}
