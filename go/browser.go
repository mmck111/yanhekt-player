// 自动登录：从本机浏览器已存的登录态里找 token
//
// 对应 server.js 的 browserRoots / localStoreFiles / scanBrowserStore /
// scanBrowserStoreCached / autoLoginOld。
//
// Edge / Chrome 的 localStorage 落在 leveldb 文件里，值是明文。
// 只读本机文件；找到的凭证只写进本机 cookie，别无他用。
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const lsMaxBytes = 6 * 1024 * 1024

type browserRoot struct {
	name string
	root string
}

type storeFile struct {
	browser string
	file    string
	mtime   time.Time
}

// browserRoots 各浏览器的用户数据目录
func browserRoots() []browserRoot {
	if runtime.GOOS != "windows" {
		return nil
	}
	la := os.Getenv("LOCALAPPDATA")
	if la == "" {
		return nil
	}
	return []browserRoot{
		{"Edge", filepath.Join(la, "Microsoft", "Edge", "User Data")},
		{"Chrome", filepath.Join(la, "Google", "Chrome", "User Data")},
		{"Brave", filepath.Join(la, "BraveSoftware", "Brave-Browser", "User Data")},
		{"Vivaldi", filepath.Join(la, "Vivaldi", "User Data")},
	}
}

var reLeveldbFile = regexp.MustCompile(`(?i)\.(log|ldb)$`)

// localStoreFiles 各浏览器所有 profile 的 Local Storage 文件，最近改过的排前面
func localStoreFiles() []storeFile {
	out := []storeFile{}
	for _, b := range browserRoots() {
		entries, err := os.ReadDir(b.root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(b.root, e.Name(), "Local Storage", "leveldb")
			names, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, f := range names {
				if !reLeveldbFile.MatchString(f.Name()) {
					continue
				}
				full := filepath.Join(dir, f.Name())
				st, err := os.Stat(full)
				if err != nil || !st.Mode().IsRegular() || st.Size() > lsMaxBytes {
					continue
				}
				out = append(out, storeFile{browser: b.name, file: full, mtime: st.ModTime()})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].mtime.After(out[j].mtime) })
	if len(out) > 200 {
		out = out[:200]
	}
	return out
}

type scanResult struct {
	oldTokens [][2]string // [token, 浏览器]
	jwts      [][2]string // [JWT, 浏览器]（新版平台 aita.yanhekt.cn）
	files     int
	err       string
}

var (
	reOldToken = regexp.MustCompile(`token\\?"\s*:\s*\\?"([0-9a-fA-F]{20,64})`)
	reNewJwt   = regexp.MustCompile(`eyJ[\w-]{10,}\.[\w-]{10,}\.[\w-]{10,}`)
)

// scanBrowserStore 扫一遍浏览器本地存储，捞出两边的登录凭证。
//
//	老平台：www.yanhekt.cn 下 localStorage 的 auth.token（32 位十六进制）
//	新平台：aita.yanhekt.cn 下的 JWT（_token）
func scanBrowserStore() scanResult {
	res := scanResult{}
	seen := map[string]bool{}
	seenJwt := map[string]bool{}
	for _, f := range localStoreFiles() {
		buf, err := os.ReadFile(f.file)
		if err != nil {
			continue
		}
		res.files++
		s := string(buf) // latin1：一个字节一个字符，和 Node 的 toString('latin1') 等价
		if !strings.Contains(s, "yanhekt") {
			continue
		}
		for i := 0; ; {
			idx := strings.Index(s[i:], "www.yanhekt.cn")
			if idx < 0 {
				break
			}
			i += idx
			end := i + 12000
			if end > len(s) {
				end = len(s)
			}
			for _, m := range reOldToken.FindAllStringSubmatch(s[i:end], -1) {
				if !seen[m[1]] {
					seen[m[1]] = true
					res.oldTokens = append(res.oldTokens, [2]string{m[1], f.browser})
				}
			}
			i += len("www.yanhekt.cn")
		}
		for i := 0; ; {
			idx := strings.Index(s[i:], "aita.yanhekt.cn")
			if idx < 0 {
				break
			}
			i += idx
			end := i + 20000
			if end > len(s) {
				end = len(s)
			}
			for _, m := range reNewJwt.FindAllString(s[i:end], -1) {
				if !seenJwt[m] {
					seenJwt[m] = true
					res.jwts = append(res.jwts, [2]string{m, f.browser})
				}
			}
			i += len("aita.yanhekt.cn")
		}
	}
	return res
}

var (
	autoScanMu sync.Mutex
	autoScanAt time.Time
	autoScan   scanResult
	scanValid  bool
)

// scanBrowserStoreCached 扫一次缓存 10 秒（自动登录按钮可以连点）
func scanBrowserStoreCached() scanResult {
	autoScanMu.Lock()
	if scanValid && time.Since(autoScanAt) < 10*time.Second {
		r := autoScan
		autoScanMu.Unlock()
		return r
	}
	autoScanMu.Unlock()

	r := scanBrowserStore()
	autoScanMu.Lock()
	autoScan, autoScanAt, scanValid = r, time.Now(), true
	autoScanMu.Unlock()
	return r
}

// autoLoginOld 自动登录老平台：验证候选 token，返回第一个有效的
func autoLoginOld() (token string, user *userInfo, from string, candidates, files int) {
	scan := scanBrowserStoreCached()
	candidates = len(scan.oldTokens)
	files = scan.files
	limit := candidates
	if limit > 40 {
		limit = 40
	}
	for _, c := range scan.oldTokens[:limit] {
		if u := verifyToken(c[0]); u != nil {
			return c[0], u, c[1], candidates, files
		}
	}
	return "", nil, "", candidates, files
}

// autoLoginNew 自动登录新平台：逐个验证候选 JWT，返回第一个有效的
func autoLoginNew() (jwt string, id *y2Identity, from string, candidates, files int) {
	scan := scanBrowserStoreCached()
	candidates = len(scan.jwts)
	files = scan.files
	limit := candidates
	if limit > 8 {
		limit = 8
	}
	for _, c := range scan.jwts[:limit] {
		if who := y2Who(c[0]); who != nil {
			return c[0], who, c[1], candidates, files
		}
	}
	return "", nil, "", candidates, files
}
