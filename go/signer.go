// 动态签名器（goja 版）：运行时从官网 bundle 抠出签名模块执行
//
// 对应 signer.js。官网一改版就重新抓、重新跑，抓不到就退回磁盘缓存 / 内置默认值。
// 对外只有四个操作，和官网模块一致： t() v() s(ts) p(url)
package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
)

const signSite = "https://www.yanhekt.cn"

// 内置默认值：官网抓不到时的最后一道保险
const (
	defaultK     = "1138b69dfef641d9d7ba49137d2d4875"
	defaultV     = "v1"
	defaultPHash = "c3d47d7b3aa8caf2983b313cb6cd142f"
)

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// dataDir 缓存写到用户目录，避免打包成 exe 后往程序目录写文件（和 Node 版同一个目录）
func dataDir() string {
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		return filepath.Join(la, "yhkt-player")
	}
	return filepath.Join(os.TempDir(), "yhkt-player")
}

func signerCacheFile() string { return filepath.Join(dataDir(), "signer-cache.json") }
func signerJSDir() string     { return filepath.Join(dataDir(), "js") }

// ---------------------------------------------------------------- 引擎接口

type signerEngine interface {
	T() int64        // 时间戳（秒）
	V() string       // 版本号，一般是 v1
	S(int64) string  // 签名
	P(string) string // 往路径里插一段固定 hash
}

// md5Engine 缓存 / 默认值走这条：k、v、phash 直接算，不执行 JS
type md5Engine struct {
	k, v, phash string
}

func (e md5Engine) T() int64  { return time.Now().Unix() }
func (e md5Engine) V() string { return e.v }
func (e md5Engine) S(n int64) string {
	return md5Hex(e.k + "_" + e.v + "_" + strconv.FormatInt(n, 10))
}
func (e md5Engine) P(u string) string {
	o := strings.LastIndex(u, "/")
	if o > -1 {
		return u[:o] + "/" + e.phash + u[o:]
	}
	return u
}

func fallbackEngine() signerEngine {
	return md5Engine{k: defaultK, v: defaultV, phash: defaultPHash}
}

// ---------------------------------------------------------------- goja 引擎

// jsSigner 把官网模块实例包起来。goja 的 Runtime 不是并发安全的，所以每次调用都加锁。
type jsSigner struct {
	mu  sync.Mutex
	vm  *goja.Runtime
	obj *goja.Object
	tFn goja.Callable
	vFn goja.Callable
	sFn goja.Callable
	pFn goja.Callable
	kFn goja.Callable
}

func (j *jsSigner) call(fn goja.Callable, args ...goja.Value) (goja.Value, error) {
	if fn == nil {
		return nil, fmt.Errorf("签名模块缺少方法")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return fn(j.obj, args...)
}

// signerWarn 签名模块出错时提示一次（地址会因此被 CDN 拒，别静默）
var (
	signerWarnMu   sync.Mutex
	signerWarnedBy = map[string]bool{}
)

func signerWarn(what string, err error) {
	signerWarnMu.Lock()
	seen := signerWarnedBy[what]
	signerWarnedBy[what] = true
	signerWarnMu.Unlock()
	if !seen {
		fmt.Printf("  [warn] 签名模块 %s 出错（签出来的地址可能被 CDN 拒绝）：%v\n", what, err)
	}
}

func (j *jsSigner) T() int64 {
	v, err := j.call(j.tFn)
	if err != nil || v == nil {
		signerWarn("t()", err)
		return time.Now().Unix()
	}
	return v.ToInteger()
}

func (j *jsSigner) V() string {
	v, err := j.call(j.vFn)
	if err != nil || v == nil {
		signerWarn("v()", err)
		return defaultV
	}
	return v.String()
}

func (j *jsSigner) S(n int64) string {
	j.mu.Lock()
	arg := j.vm.ToValue(n)
	j.mu.Unlock()
	v, err := j.call(j.sFn, arg)
	if err != nil || v == nil {
		signerWarn("s()", err)
		return ""
	}
	return v.String()
}

func (j *jsSigner) P(u string) string {
	j.mu.Lock()
	arg := j.vm.ToValue(u)
	j.mu.Unlock()
	v, err := j.call(j.pFn, arg)
	if err != nil || v == nil {
		signerWarn("p()", err)
		return u
	}
	return v.String()
}

// kVal 取模块里的 k()（不是所有版本都有）
func (j *jsSigner) kVal() (string, bool) {
	if j.kFn == nil {
		return "", false
	}
	v, err := j.call(j.kFn)
	if err != nil || v == nil {
		return "", false
	}
	return v.String(), true
}

// newJSSigner 把抠出来的模块体实例化成一个对象
func newJSSigner(body string) (*jsSigner, error) {
	vm := goja.New()

	// 模块是 webpack 的工厂函数 (n,t,e)=>{ ... }，e 只需要 d（defineProperty 包装）。
	//
	// 另外要补一个"像浏览器"的运行环境：官网的 p() 会读 navigator.userAgent
	// 来决定往地址里插哪段 hash（移动端和桌面端不一样）。Node 版能跑，是因为
	// Node 18+ 自带 navigator（UA 是 "Node.js/xx"，走的是桌面分支）。
	// 这里给它一个桌面 Chrome 的 UA，结果和 Node 版、和真实浏览器都一致；
	// 没有它 p() 会抛 ReferenceError，签出来的地址会被 CDN 403。
	browserEnv := `var navigator = {
  userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
  platform: "Win32", vendor: "Google Inc.", language: "zh-CN",
  languages: ["zh-CN", "zh"], maxTouchPoints: 0, hardwareConcurrency: 8,
  userAgentData: { mobile: false, platform: "Windows" }
};
var location = {
  href: "https://www.yanhekt.cn/", origin: "https://www.yanhekt.cn",
  protocol: "https:", host: "www.yanhekt.cn", hostname: "www.yanhekt.cn",
  port: "", pathname: "/", search: "", hash: ""
};
var screen = { width: 1920, height: 1080, availWidth: 1920, availHeight: 1040 };
`
	harness := "(function(){\n" + browserEnv +
		"  function d(obj, defs){ Object.keys(defs).forEach(function(k){ Object.defineProperty(obj,k,{get:defs[k],enumerable:true,configurable:true}); }); }\n" +
		"  var factory = (function(n,t,e){" + body + "});\n" +
		"  var t = {};\n" +
		"  factory({}, t, {d: d});\n" +
		"  if (!t.A) throw new Error('模块未导出 A');\n" +
		"  return new t.A();\n" +
		"})()"

	// 万一模块里有死循环，别把整个进程挂住
	timer := time.AfterFunc(10*time.Second, func() { vm.Interrupt("签名模块执行超时") })
	val, err := vm.RunString(harness)
	timer.Stop()
	vm.ClearInterrupt()
	if err != nil {
		return nil, err
	}

	obj := val.ToObject(vm)
	js := &jsSigner{vm: vm, obj: obj}
	get := func(name string) goja.Callable {
		v := obj.Get(name)
		if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
			return nil
		}
		fn, _ := goja.AssertFunction(v)
		return fn
	}
	js.tFn, js.vFn, js.sFn, js.pFn = get("t"), get("v"), get("s"), get("p")
	js.kFn = get("k")
	if js.sFn == nil || js.pFn == nil {
		return nil, fmt.Errorf("签名模块缺方法")
	}
	return js, nil
}

// ---------------------------------------------------------------- 状态

var (
	signerStateMu sync.RWMutex
	signerCur     signerEngine
	signerSrc     = "pending"
)

func signerSetEngine(e signerEngine, src string) {
	signerStateMu.Lock()
	signerCur = e
	signerSrc = src
	signerStateMu.Unlock()
}

func signerCurrent() signerEngine {
	signerStateMu.RLock()
	e := signerCur
	signerStateMu.RUnlock()
	if e == nil {
		return fallbackEngine()
	}
	return e
}

func signerSource() string {
	signerStateMu.RLock()
	defer signerStateMu.RUnlock()
	return signerSrc
}

// signerSign 给 CDN 地址加上 4 个参数（对应 signer.js 的 sign）
func signerSign(src, token string) string {
	e := signerCurrent()
	ts := e.T()
	return fmt.Sprintf("%s?Xvideo_Token=%s&Xclient_Timestamp=%d&Xclient_Signature=%s&Xclient_Version=%s&Platform=yhkt_user",
		e.P(src), token, ts, e.S(ts), e.V())
}

// ---------------------------------------------------------------- 抓官网

var signerFetchClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig:       insecureTLS,
		ForceAttemptHTTP2:     false,
		MaxIdleConnsPerHost:   2,
		ResponseHeaderTimeout: 30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("跳转太多")
		}
		return nil
	},
}

func httpGet(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")
	resp, err := signerFetchClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d %s", resp.StatusCode, url)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}

var reBundleSrc = regexp.MustCompile(`src="(/static/js/[^"]+\.js)"`)
var reMainBundle = regexp.MustCompile(`/main-`)

// listBundles 官网首页引了哪些 JS；签名模块在 main-*.js 里，优先试这些
func listBundles() ([]string, error) {
	body, err := httpGet(signSite + "/")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	files := []string{}
	for _, m := range reBundleSrc.FindAllStringSubmatch(string(body), -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		files = append(files, m[1])
	}
	sort.SliceStable(files, func(i, j int) bool {
		ai, bi := 1, 1
		if reMainBundle.MatchString(files[i]) {
			ai = 0
		}
		if reMainBundle.MatchString(files[j]) {
			bi = 0
		}
		return ai < bi
	})
	return files, nil
}

// fetchBundle 取一个 JS；本地有 6 小时内的缓存就直接用
func fetchBundle(p string) (string, error) {
	name := p[strings.LastIndex(p, "/")+1:]
	dir := signerJSDir()
	cachePath := filepath.Join(dir, name)

	// 缓存是纯优化：目录不可写也不能影响取 bundle
	if st, err := os.Stat(cachePath); err == nil && st.Size() > 1000 && time.Since(st.ModTime()) < 6*time.Hour {
		if b, err := os.ReadFile(cachePath); err == nil {
			return string(b), nil
		}
	}

	body, err := httpGet(signSite + p)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err == nil {
		_ = os.WriteFile(cachePath, body, 0o644)
	}
	return string(body), nil
}

// ---------------------------------------------------------------- 抠模块

var (
	reModuleHead = regexp.MustCompile(`(\d{3,7}):\((\w+),(\w+),(\w+)\)=>\{`)
	reSignProbe  = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
	rePhashSeg   = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// extractModule 从 bundle 里抠出含 `v:()=>"v1"` 的那个 webpack 模块体（含外层花括号）
func extractModule(src string) string {
	marker := strings.Index(src, `v:()=>"v1"`)
	if marker < 0 {
		return ""
	}

	// 向前找最近的模块定义头 NNNNNN:(a,b,c)=>{
	start := -1
	for _, m := range reModuleHead.FindAllStringSubmatchIndex(src, -1) {
		if m[0] >= marker {
			break
		}
		start = m[1] - 1 // 指向 "{"
	}
	if start < 0 {
		return ""
	}

	// 配平大括号（跳过字符串，模板串里的 ${} 不处理，和 Node 版一致）
	depth := 0
	i := start
	for ; i < len(src); i++ {
		ch := src[i]
		if ch == '{' {
			depth++
		} else if ch == '}' {
			depth--
			if depth == 0 {
				break
			}
		} else if ch == '"' || ch == '\'' || ch == '`' {
			q := ch
			i++
			for i < len(src) && src[i] != q {
				if src[i] == '\\' {
					i++
				}
				i++
			}
		}
	}
	if depth != 0 {
		return ""
	}
	return src[start : i+1]
}

// ---------------------------------------------------------------- 初始化

type signerCacheJSON struct {
	K      string `json:"k"`
	V      string `json:"v"`
	PHash  string `json:"phash"`
	Source string `json:"source"`
	At     string `json:"at"`
}

func loadSignerCache() *signerCacheJSON {
	b, err := os.ReadFile(signerCacheFile())
	if err != nil {
		return nil
	}
	var c signerCacheJSON
	if err := json.Unmarshal(b, &c); err != nil || c.K == "" {
		return nil
	}
	return &c
}

func saveSignerCache(c signerCacheJSON) {
	if err := os.MkdirAll(dataDir(), 0o755); err != nil {
		return
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(signerCacheFile(), b, 0o644)
}

var signerInitMu sync.Mutex

// signerInit 启动时调用一次；之后每 6 小时可再调一次做静默刷新。
// 返回（来源, 是否来自官网实时提取）。
func signerInit(force bool) (string, bool) {
	signerInitMu.Lock()
	defer signerInitMu.Unlock()

	// 先从磁盘缓存恢复，保证立刻可用
	if c := loadSignerCache(); c != nil && !force {
		signerStateMu.RLock()
		have := signerCur != nil && strings.HasPrefix(signerSrc, "live:")
		signerStateMu.RUnlock()
		if !have {
			signerSetEngine(md5Engine{k: c.K, v: c.V, phash: c.PHash}, "cache")
		}
	}

	err := extractLiveSigner()
	if err != nil {
		// 网络不通就沿用缓存 / 默认值
		if os.Getenv("YHKT_DEBUG") == "1" {
			fmt.Println("[signer] 动态提取失败:", err)
		}
	}

	if signerCur == nil {
		signerSetEngine(fallbackEngine(), "default")
	}
	src := signerSource()
	return src, strings.HasPrefix(src, "live:")
}

// extractLiveSigner 抓官网 → 挨个 bundle 找 → 实例化 → 自检 → 写入缓存
func extractLiveSigner() error {
	bundles, err := listBundles()
	if err != nil {
		return err
	}
	if os.Getenv("YHKT_DEBUG") == "1" {
		fmt.Println("[signer] 候选 bundle:", len(bundles))
	}

	for _, p := range bundles {
		src, err := fetchBundle(p)
		if err != nil {
			if os.Getenv("YHKT_DEBUG") == "1" {
				fmt.Println("[signer] 取不到", p, err)
			}
			continue
		}
		if !strings.Contains(src, `v:()=>"v1"`) {
			continue
		}
		body := extractModule(src)
		if body == "" {
			continue
		}
		js, err := newJSSigner(body)
		if err != nil {
			if os.Getenv("YHKT_DEBUG") == "1" {
				fmt.Println("[signer] 实例化失败:", err)
			}
			continue
		}

		// 自检：s() 应该是 32 位十六进制
		probe := js.S(1700000000)
		if !reSignProbe.MatchString(probe) {
			if os.Getenv("YHKT_DEBUG") == "1" {
				fmt.Println("[signer] 签名自检失败:", probe)
			}
			continue
		}

		name := p[strings.LastIndex(p, "/")+1:]
		signerSetEngine(js, "live:"+name)

		// p() 里插入的那段固定 hash，靠试一次反推
		phash := defaultPHash
		out := js.P("https://cvideo.yanhekt.cn/a/b/c.m3u8")
		parts := strings.Split(out, "/")
		if len(parts) >= 2 {
			seg := parts[len(parts)-2]
			if rePhashSeg.MatchString(seg) {
				phash = seg
			}
		}

		k, ok := js.kVal()
		if !ok {
			k = defaultK
		}
		saveSignerCache(signerCacheJSON{K: k, V: js.V(), PHash: phash, Source: "live:" + name, At: time.Now().Format(time.RFC3339)})
		return nil
	}
	return fmt.Errorf("没在官网 bundle 里找到签名模块")
}
