// 延河课堂播放器 · Go 版后端
//
// 目标是和 Node 版（server.js）行为一致，但产物是 ~10MB 单文件、零依赖。
// 页面资源用 go:embed 打进二进制；构建前由 build.ps1 把 ../public 同步到 ./web。
package main

import (
	"context"
	"sync"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

const build = "1.5.0-go"

var (
	port = 8788
	lan  = false
)

// installDNS 用环境变量指定 DNS 服务器（逗号分隔，可带端口，默认 53）。
//
// Android 上没有 /etc/resolv.conf，纯 Go 解析器读不到 nameserver，于是所有域名都解析失败；
// App 会把系统当前 DNS 通过 YHKT_DNS 传进来。桌面端不设这个变量，行为完全不变。
func installDNS() {
	spec := strings.TrimSpace(os.Getenv("YHKT_DNS"))
	if spec == "" {
		return
	}
	var servers []string
	for _, s := range strings.Split(spec, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !strings.Contains(s, ":") {
			s += ":53"
		}
		servers = append(servers, s)
	}
	if len(servers) == 0 {
		return
	}
	var mu sync.Mutex
	next := 0
	d := net.Dialer{Timeout: 3 * time.Second}
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			mu.Lock()
			srv := servers[next%len(servers)]
			next++
			mu.Unlock()
			return d.DialContext(ctx, "udp", srv)
		},
	}
	fmt.Println("  DNS 解析走：" + strings.Join(servers, " "))
}

func main() {
	if p := os.Getenv("PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			port = n
		}
	}
	lan = os.Getenv("LAN") == "1" || os.Getenv("HOST") == "0.0.0.0"

	setUtf8Console()
	installDNS()

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		fmt.Println("页面资源缺失：" + err.Error())
		return
	}

	// 单实例检测：端口上已经有播放器在跑就别再起一个
	if os.Getenv("FORCE_NEW") != "1" {
		if running := probeExisting(port); running != "" {
			printAlreadyRunning(running)
			// 不自动关窗，等用户看完再关；20 秒后自行退出，免得留一个空窗口
			time.Sleep(20 * time.Second)
			return
		}
	}

	// 端口被别的程序占了就往后顺延，最多试 12 个
	host := "127.0.0.1"
	if lan {
		host = "0.0.0.0"
	}
	var ln net.Listener
	for i := 0; i < 12; i++ {
		l, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err == nil {
			ln = l
			break
		}
		port++
	}
	if ln == nil {
		fmt.Println("  启动失败：端口都被占用了")
		return
	}

	mux := http.NewServeMux()
	registerPages(mux, sub)
	registerAPI(mux)

	banner(port)

	// 签名器后台初始化（抓官网 JS 要几秒，别挡住页面）
	go func() {
		if _, live := signerInit(false); live {
			fmt.Println("  签名算法：已从官网实时提取（可自愈）")
		}
	}()
	// 每 6 小时静默刷新一次签名算法
	go func() {
		for {
			time.Sleep(6 * time.Hour)
			_, _ = signerInit(true)
		}
	}()

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 20 * time.Second}
	if err := srv.Serve(ln); err != nil {
		fmt.Printf("启动失败：%v\n", err)
	}
}

// ---------------------------------------------------------------- 页面

func registerPages(mux *http.ServeMux, web fs.FS) {
	serve := func(name, ctype, cacheControl string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			b, err := fs.ReadFile(web, name)
			if err != nil {
				http.Error(w, "missing asset: "+name, 500)
				return
			}
			w.Header().Set("content-type", ctype)
			w.Header().Set("cache-control", cacheControl)
			_, _ = w.Write(b)
		}
	}
	page := serve("index.html", "text/html; charset=utf-8", "no-store")
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			text404(w)
			return
		}
		page(w, r)
	})
	mux.HandleFunc("/old", page)
	mux.HandleFunc("/index.html", page)
	mux.HandleFunc("/y2", serve("y2.html", "text/html; charset=utf-8", "no-store"))
	mux.HandleFunc("/y2.html", serve("y2.html", "text/html; charset=utf-8", "no-store"))
	mux.HandleFunc("/hls.min.js", serve("hls.min.js", "application/javascript; charset=utf-8", "public, max-age=86400"))
	mux.HandleFunc("/icon.png", serve("icon.png", "image/png", "public, max-age=86400"))
}

// ---------------------------------------------------------------- 路由

func registerAPI(mux *http.ServeMux) {
	// ---- 状态 ----
	mux.HandleFunc("/api/status", safe(func(w http.ResponseWriter, r *http.Request) {
		user := verifyToken(tokenOf(r))
		writeJSON(w, 200, statusResp{LoggedIn: user != nil, User: user, Signer: signerSource(), Build: build})
	}))

	// ---- Yanhe 2.0（新版平台 aita.yanhekt.cn）----
	registerY2(mux)

	// ---- 登录 / 退出 ----
	mux.HandleFunc("/api/login", safe(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, 405, map[string]any{"ok": false, "error": "method not allowed"})
			return
		}
		body := readBody(r)
		token := strings.TrimSpace(reBearer.ReplaceAllString(jstr(body["token"]), ""))
		if token == "" {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "token 为空"})
			return
		}
		user := verifyToken(token)
		if user == nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": "token 无效或已过期，请重新获取"})
			return
		}
		setCookie(w, cookieName, encodeURIComponent(token), cookieTTL)
		writeJSON(w, 200, map[string]any{"ok": true, "user": user})
	}))

	mux.HandleFunc("/api/logout", safe(func(w http.ResponseWriter, r *http.Request) {
		setCookie(w, cookieName, "", 0)
		writeJSON(w, 200, map[string]any{"ok": true})
	}))

	// 自动登录：去本机浏览器里找已登录的凭证，找到就直接种 cookie
	mux.HandleFunc("/api/login/auto", safe(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			writeJSON(w, 405, map[string]any{"ok": false, "error": "method not allowed"})
			return
		}
		token, user, from, candidates, files := autoLoginOld()
		if user == nil {
			writeJSON(w, 200, map[string]any{
				"ok": false, "error": "没在浏览器里找到登录状态",
				"candidates": candidates, "files": files,
			})
			return
		}
		setCookie(w, cookieName, encodeURIComponent(token), cookieTTL)
		writeJSON(w, 200, map[string]any{"ok": true, "user": user, "from": from})
	}))

	// ---- 内置课程库（本地，不需要登录）----
	mux.HandleFunc("/api/db/terms", safe(func(w http.ResponseWriter, r *http.Request) {
		cat := loadCatalog()
		live := []string{}
		if terms, err := oldSemesters(tokenOf(r)); err == nil {
			for _, t := range terms {
				live = append(live, t.Name)
			}
		}
		// 老平台实时学期表 + 课程库里的（离线兜底），去重
		seen := map[string]bool{}
		terms := []string{}
		for _, t := range append(append([]string{}, live...), cat.terms...) {
			if t == "" || seen[t] {
				continue
			}
			seen[t] = true
			terms = append(terms, t)
		}
		writeJSON(w, 200, dbTermsResp{Ok: true, Terms: terms, Live: len(live), Count: len(cat.rows), Meta: cat.meta, Error: cat.err})
	}))

	mux.HandleFunc("/api/db/search", safe(handleDBSearch))

	// 打开一门课：讲次 + 每讲的播放地址（都走本地 /p 代理）
	mux.HandleFunc("/api/open", safe(func(w http.ResponseWriter, r *http.Request) {
		token := tokenOf(r)
		user := verifyToken(token)
		if user == nil {
			writeJSON(w, 401, map[string]any{"ok": false, "error": "尚未登录", "needLogin": true})
			return
		}
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "缺少课程编号"})
			return
		}
		d := getCourse(id, token, r.URL.Query().Get("refresh") == "1")
		if d != nil && d.fail {
			writeJSON(w, 500, map[string]any{"ok": false, "error": d.errMsg})
			return
		}
		writeJSON(w, 200, d.toJSON())
	}))

	mux.HandleFunc("/api/course", safe(func(w http.ResponseWriter, r *http.Request) {
		token := tokenOf(r)
		user := verifyToken(token)
		if user == nil {
			writeJSON(w, 401, map[string]any{"ok": false, "error": "尚未登录", "needLogin": true})
			return
		}
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "缺少课程编号"})
			return
		}
		d := getCourse(id, token, r.URL.Query().Get("refresh") == "1")
		if d != nil && d.fail {
			writeJSON(w, 500, map[string]any{"ok": false, "error": d.errMsg})
			return
		}
		writeJSON(w, 200, d.toJSON())
	}))

	// 自检：把容易出错的判断（学期解析、教室编码）跑一遍
	mux.HandleFunc("/api/selftest", safe(handleSelftest))

	// ---- 直播 ----
	mux.HandleFunc("/api/live", safe(func(w http.ResponseWriter, r *http.Request) {
		// 页面点「直播」时带 refresh=1，绕过 30s 缓存（也顺带清掉早期失败留下的空列表）
		if r.URL.Query().Get("refresh") != "" {
			liveInvalidate()
		}
		items, at, lerr := liveListOld()
		if items == nil {
			items = []liveItem{}
		}
		writeJSON(w, 200, liveOK{Ok: true, Items: items, At: &at, Err: lerr, Tz: bj.String(),
			Raw: liveRawCount, Src: liveSrcCount})
	}))

	// 直播流代理（不缓存、不排队）
	mux.HandleFunc("/lp", safe(func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("u")
		if raw == "" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte("missing u"))
			return
		}
		proxyLive(w, r, dec(raw))
	}))

	// ---- 录播 ----
	mux.HandleFunc("/p", safe(func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("u")
		if raw == "" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte("missing u"))
			return
		}
		proxyHls(w, r, dec(raw))
	}))

	// 下载一个讲次：把 HLS 分片拼成单文件直接给浏览器
	mux.HandleFunc("/dl", safe(func(w http.ResponseWriter, r *http.Request) {
		token := tokenOf(r)
		user := verifyToken(token)
		if user == nil {
			w.Header().Set("content-type", "text/plain; charset=utf-8")
			w.WriteHeader(401)
			_, _ = w.Write([]byte("need login"))
			return
		}
		opt := dlOpt{
			course:  strings.TrimSpace(r.URL.Query().Get("course")),
			session: strings.TrimSpace(r.URL.Query().Get("session")),
			kind:    "main",
			probe:   r.URL.Query().Get("probe") == "1",
		}
		if r.URL.Query().Get("kind") == "vga" {
			opt.kind = "vga"
		}
		downloadSession(w, r, opt, token, user)
	}))
}

// ---------------------------------------------------------------- 响应结构

type statusResp struct {
	LoggedIn bool      `json:"loggedIn"`
	User     *userInfo `json:"user"`
	Signer   string    `json:"signer"`
	Build    string    `json:"build"`
}

type dbTermsResp struct {
	Ok    bool           `json:"ok"`
	Terms []string       `json:"terms"`
	Live  int            `json:"live"`
	Count int            `json:"count"`
	Meta  map[string]any `json:"meta"`
	Error string         `json:"error,omitempty"`
}

type liveOK struct {
	Ok    bool       `json:"ok"`
	Items []liveItem `json:"items"`
	At    *int64     `json:"at,omitempty"`
	Err   string     `json:"err,omitempty"`
	// Tz 引擎解析"北京时间串"用的偏移，排查"手机上没有直播"时一眼能看出是不是时区问题
	Tz string `json:"tz"`
	// Raw 上游合并出来的条数（过滤前）：Raw=0 说明接口没给数据，Raw>0 而 Items=0 说明被过滤了
	Raw int `json:"raw"`
	// Src 上游对象数（含没有取流地址的）：Src>0 而 Raw=0 说明都被"没有播放地址"挡掉了
	Src int `json:"src"`
}

// ---------------------------------------------------------------- 工具

var reBearer = regexp.MustCompile(`(?i)^Bearer\s+`)

// safe 兜住 handler 里的 panic，转成 Node 版一样的 500 JSON
func safe(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if e := recover(); e != nil {
				fmt.Printf("  [ERROR] %v\n", e)
				writeJSON(w, 500, map[string]any{"ok": false, "error": fmt.Sprint(e)})
			}
		}()
		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, `{"ok":false}`, 500)
		return
	}
	w.Header().Set("content-type", "application/json; charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write(b)
}

func text404(w http.ResponseWriter) {
	w.Header().Set("content-type", "text/plain; charset=utf-8")
	w.WriteHeader(404)
	_, _ = w.Write([]byte("404"))
}

// readBody 读 JSON 请求体；解析不了就当空对象（Node 版一样）
func readBody(r *http.Request) map[string]any {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		return map[string]any{}
	}
	return m
}

// probeExisting 看看这个端口上是不是已经有播放器在跑
func probeExisting(p int) string {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(p) + "/api/status")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ""
	}
	var j map[string]any
	if err := json.Unmarshal(body, &j); err != nil {
		return ""
	}
	if _, ok := j["loggedIn"]; !ok {
		return ""
	}
	return jstr(j["build"])
}

// printAlreadyRunning 端口已有服务时的提示（和 Node 版一致）
func printAlreadyRunning(runningBuild string) {
	fmt.Println()
	fmt.Println("  ═══════════════════════════════════════════════════")
	fmt.Println("   播放器已经在运行了，不用再开一个。")
	fmt.Println()
	if runningBuild != "" {
		fmt.Println("   正在运行的版本：" + runningBuild)
	} else {
		fmt.Println("   正在运行的版本：更早的旧版（没有版本号）")
	}
	fmt.Println("   这个文件是版本：" + build)
	if runningBuild != build {
		fmt.Println()
		fmt.Println("   ⚠ 两个版本不一样 —— 你现在看到的是旧的那个！")
		fmt.Println("     先把那个黑窗口关掉（浏览器里的旧页面也关掉），再双击本文件。")
	}
	fmt.Println()
	fmt.Println("   请复制以下链接到浏览器打开：")
	fmt.Println()
	fmt.Println("        http://127.0.0.1:" + strconv.Itoa(port) + "/")
	fmt.Println()
	fmt.Println("   （如果浏览器里已经开着播放器，刷新一下页面就行）")
	fmt.Println("  ═══════════════════════════════════════════════════")
}

func banner(p int) {
	fmt.Println()
	fmt.Println("  ═══════════════════════════════════════════════════")
	fmt.Println("   延河课堂 · 在线播放器（Go 版）      版本 " + build)
	fmt.Println("  ═══════════════════════════════════════════════════")
	fmt.Println()
	url := "http://127.0.0.1:" + strconv.Itoa(p) + "/"
	if copyToClipboard(url) {
		fmt.Println("   地址已经复制到剪贴板 —— 打开浏览器，地址栏 Ctrl+V 回车：")
	} else {
		fmt.Println("   请复制以下链接到浏览器打开：")
	}
	fmt.Println()
	fmt.Println("        " + url)
	if lan {
		for _, a := range lanIPs() {
			fmt.Println("        http://" + net.JoinHostPort(a, strconv.Itoa(p)) + "/")
		}
	}
	fmt.Println()
	fmt.Println("   请勿关闭此窗口，此窗口为服务器。")
	fmt.Println("   关闭窗口 ＝ 关闭播放器。")
	fmt.Println()
	if lan {
		fmt.Println("   （局域网模式：同学用上面那个 10.x 地址也能打开）")
	}
	fmt.Println("  ═══════════════════════════════════════════════════")
	fmt.Println()
}

func lanIPs() []string {
	var out []string
	ifaces, _ := net.Interfaces()
	for _, i := range ifaces {
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() {
				out = append(out, ipn.IP.String())
			}
		}
	}
	return out
}
