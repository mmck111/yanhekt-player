// 录像判定结果的磁盘缓存
//
// 判定"这门课有没有能播的录像"要逐讲真取一次流，一门课几个请求。
// 搜索结果里几十门课一起验，第一次要等很久；而判定结果几小时内不会变，
// 所以按 courseId 存一份，第二次搜索直接命中缓存。
package main

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// y2Verdict 一门课的录像判定
type y2Verdict struct {
	Videos  int    `json:"v"` // 能播的讲次数
	Total   int    `json:"t"` // 讲次总数
	Room    string `json:"r,omitempty"`
	At      int64  `json:"a"` // 判定时间（unix 秒）
	Checked bool   `json:"k"` // 真的验过（false = 只探到一部分）
}

// y2VerdictTTL 缓存有效期
const y2VerdictTTL = 12 * time.Hour

var (
	y2VerMu    sync.Mutex
	y2VerCache map[string]y2Verdict
	y2VerDirty bool
	y2VerAt    time.Time
)

// y2VerFile 缓存文件（和课程库同目录）
func y2VerFile() string {
	return filepath.Join(filepath.Dir(y2CatFile()), "y2-verify.json.gz")
}

// y2VerdictLoad 读缓存（进程内只读一次）
func y2VerdictLoad() map[string]y2Verdict {
	y2VerMu.Lock()
	defer y2VerMu.Unlock()
	if y2VerCache != nil {
		return y2VerCache
	}
	y2VerCache = map[string]y2Verdict{}
	f, err := os.Open(y2VerFile())
	if err != nil {
		return y2VerCache
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return y2VerCache
	}
	defer zr.Close()
	m := map[string]y2Verdict{}
	if json.NewDecoder(zr).Decode(&m) == nil {
		y2VerCache = m
	}
	return y2VerCache
}

// y2VerdictGet 取一个还在有效期内的判定
func y2VerdictGet(courseID string) (y2Verdict, bool) {
	m := y2VerdictLoad()
	v, ok := m[courseID]
	if !ok || time.Since(time.Unix(v.At, 0)) > y2VerdictTTL {
		return y2Verdict{}, false
	}
	return v, true
}

// y2VerdictSet 记一个判定（顺手标记待落盘）
func y2VerdictSet(courseID string, v y2Verdict) {
	m := y2VerdictLoad()
	y2VerMu.Lock()
	v.At = time.Now().Unix()
	m[courseID] = v
	y2VerDirty = true
	y2VerMu.Unlock()
}

// y2VerdictFlush 有改动就写盘（最多 1 分钟内写一次）
func y2VerdictFlush(force bool) {
	y2VerMu.Lock()
	if y2VerCache == nil || (!y2VerDirty && !force) {
		y2VerMu.Unlock()
		return
	}
	if !force && time.Since(y2VerAt) < time.Minute {
		y2VerMu.Unlock()
		return
	}
	// 清掉过期项，别让文件越长越大
	now := time.Now()
	out := make(map[string]y2Verdict, len(y2VerCache))
	for k, v := range y2VerCache {
		if now.Sub(time.Unix(v.At, 0)) <= 7*24*time.Hour {
			out[k] = v
		}
	}
	y2VerDirty = false
	y2VerAt = now
	y2VerMu.Unlock()

	path := y2VerFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	for _, p := range y2CatWritePaths(path) {
		if err := writeGzipJSON(p, out); err == nil {
			return
		}
	}
}

// writeGzipJSON 写 gzip JSON（先写临时文件再改名）
func writeGzipJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	zw, _ := gzip.NewWriterLevel(f, gzip.BestCompression)
	err = json.NewEncoder(zw).Encode(v)
	if e := zw.Close(); err == nil {
		err = e
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
