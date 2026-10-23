// 老平台"这门课有没有录像"的判定结果，落盘缓存。
//
// 为什么要落盘：电脑端的 exe 一直开着，判定结果在内存里热着；
// 手机端每次重开 App 引擎都是新进程，只靠内存缓存的话，
// 每次搜索都要把几十门课重新探一遍（每门一个请求），自然比电脑端慢一大截。
// 判定结果几小时内不会变，所以按 (账号, courseId) 存一份，TTL 6 小时。
package main

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// oldVerdict 一门课的探测结论
type oldVerdict struct {
	Videos   *int   `json:"v,omitempty"`
	Total    *int   `json:"t,omitempty"`
	Verified bool   `json:"k"`
	Tag      string `json:"g,omitempty"`
	Note     string `json:"n,omitempty"`
	Latest   string `json:"l,omitempty"`
	Room     string `json:"r,omitempty"`
	Hide     bool   `json:"h,omitempty"` // 确实没录像，算是被藏掉
	At       int64  `json:"a"`           // 判定时间（unix 秒）
}

// oldVerdictTTL 缓存有效期
const oldVerdictTTL = 6 * time.Hour

var (
	oldVerMu    sync.Mutex
	oldVerCache map[string]oldVerdict
	oldVerDirty bool
	oldVerAt    time.Time
)

// oldVerFile 缓存文件（和新版课程库同目录）
func oldVerFile() string {
	return filepath.Join(filepath.Dir(y2CatFile()), "old-verify.json.gz")
}

func oldVerdictLoad() map[string]oldVerdict {
	oldVerMu.Lock()
	defer oldVerMu.Unlock()
	if oldVerCache != nil {
		return oldVerCache
	}
	oldVerCache = map[string]oldVerdict{}
	f, err := os.Open(oldVerFile())
	if err != nil {
		return oldVerCache
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return oldVerCache
	}
	defer zr.Close()
	m := map[string]oldVerdict{}
	if json.NewDecoder(zr).Decode(&m) == nil {
		oldVerCache = m
	}
	return oldVerCache
}

func oldVerdictKey(courseID, token string) string {
	last10 := token
	if len(last10) > 10 {
		last10 = last10[len(last10)-10:]
	}
	return last10 + ":" + courseID
}

// oldVerdictGet 取一个还在有效期内的判定
func oldVerdictGet(courseID, token string) (oldVerdict, bool) {
	v, ok := oldVerdictLoad()[oldVerdictKey(courseID, token)]
	if !ok || time.Since(time.Unix(v.At, 0)) > oldVerdictTTL {
		return oldVerdict{}, false
	}
	return v, true
}

// oldVerdictSet 记一个判定（顺手标记待落盘）
func oldVerdictSet(courseID, token string, v oldVerdict) {
	m := oldVerdictLoad()
	oldVerMu.Lock()
	v.At = time.Now().Unix()
	m[oldVerdictKey(courseID, token)] = v
	oldVerDirty = true
	oldVerMu.Unlock()
}

// oldVerdictFlush 有改动就写盘（最多 1 分钟写一次）
func oldVerdictFlush(force bool) {
	oldVerMu.Lock()
	if oldVerCache == nil || (!oldVerDirty && !force) {
		oldVerMu.Unlock()
		return
	}
	if !force && time.Since(oldVerAt) < time.Minute {
		oldVerMu.Unlock()
		return
	}
	now := time.Now()
	out := make(map[string]oldVerdict, len(oldVerCache))
	for k, v := range oldVerCache {
		if now.Sub(time.Unix(v.At, 0)) <= 7*24*time.Hour {
			out[k] = v
		}
	}
	oldVerDirty = false
	oldVerAt = now
	oldVerMu.Unlock()

	path := oldVerFile()
	for _, p := range y2CatWritePaths(path) {
		if err := writeGzipJSON(p, out); err == nil {
			return
		}
	}
}

// oldVerdictFrom 把探测结果转成可缓存的形式。
// 网络/权限报错（Tag 为空且没结论）不缓存，否则一次断网会把它钉死 6 小时。
func oldVerdictFrom(rows []probedRow, hide bool) (oldVerdict, bool) {
	if hide {
		return oldVerdict{Hide: true}, true
	}
	if len(rows) == 0 {
		return oldVerdict{}, false
	}
	r := rows[0]
	if !r.Verified && r.Tag == "" {
		return oldVerdict{}, false
	}
	v := oldVerdict{
		Videos:   r.VideoCount,
		Total:    r.TotalSessions,
		Verified: r.Verified,
		Tag:      r.Tag,
		Note:     r.Note,
		Room:     r.Room,
	}
	if r.LatestDate != nil {
		v.Latest = *r.LatestDate
	}
	return v, true
}

// oldVerdictRow 用缓存 + 当前的课程行拼回输出（名字/教师/学期用最新的）
func oldVerdictRow(c courseRow, v oldVerdict) []probedRow {
	if v.Hide {
		return nil
	}
	r := probedRow{
		courseRow:     c,
		VideoCount:    v.Videos,
		TotalSessions: v.Total,
		Verified:      v.Verified,
		Tag:           v.Tag,
		Note:          v.Note,
	}
	if v.Latest != "" {
		l := v.Latest
		r.LatestDate = &l
	}
	if v.Room != "" {
		r.Room = v.Room
	}
	return []probedRow{r}
}
