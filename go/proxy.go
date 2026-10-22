// /p 录播代理：签名 + 全局串行 + 分片缓存 + m3u8 地址改写
//
// 对应 server.js 的 upstreamOnce / upstreamGet / cacheGet / cachePut /
// abs / enc / dec / proxyHls
package main

import (
	"container/list"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------- 录播拉流客户端

// 实测：CDN 只容忍极少量并发，多了会长时间挂起后 403。
// Node 版是 eachHost 的 agent maxSockets=1：**每个 CDN 域名**串行，域名之间互不影响。
// 所以这里按 host 各给一条通道，本地排队；单连接就能跑满 4.5MB/s。
var (
	cdnSemMu sync.Mutex
	cdnSems  = map[string]chan struct{}{}
)

func cdnSemFor(host string) chan struct{} {
	cdnSemMu.Lock()
	defer cdnSemMu.Unlock()
	if s, ok := cdnSems[host]; ok {
		return s
	}
	if len(cdnSems) > 64 {
		cdnSems = map[string]chan struct{}{}
	}
	s := make(chan struct{}, 1)
	cdnSems[host] = s
	return s
}

var cdnClient = &http.Client{
	Transport: &http.Transport{
		TLSClientConfig:       insecureTLS,
		ForceAttemptHTTP2:     false,
		MaxConnsPerHost:       1,
		MaxIdleConnsPerHost:   1,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		DisableCompression:    true,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("跳转太多")
		}
		setVideoHeaders(req)
		return nil
	},
}

// setVideoHeaders CDN 侧要求的请求头（缺 Referer 会被拒）
func setVideoHeaders(req *http.Request) {
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", origin+"/")
	req.Header.Set("Origin", origin)
	req.Header.Set("Accept", "*/*")
}

type upstreamResp struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
}

// releaseBody 读完/关掉时释放串行锁
type releaseBody struct {
	rc   io.ReadCloser
	once sync.Once
	rel  func()
}

func (b *releaseBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if err != nil {
		b.once.Do(b.rel)
	}
	return n, err
}

func (b *releaseBody) Close() error {
	err := b.rc.Close()
	b.once.Do(b.rel)
	return err
}

// upstreamOnce 发一次上游请求（自动排队，拿到响应后锁一直持有到 body 读完）
func upstreamOnce(rawURL string) (*upstreamResp, error) {
	host := ""
	if u, err := url.Parse(rawURL); err == nil {
		host = u.Host
	}
	sem := cdnSemFor(host)
	sem <- struct{}{}
	release := func() { <-sem }
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		release()
		return nil, err
	}
	setVideoHeaders(req)
	resp, err := cdnClient.Do(req)
	if err != nil {
		release()
		return nil, err
	}
	return &upstreamResp{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       &releaseBody{rc: resp.Body, rel: release},
	}, nil
}

// upstreamGet 非 200 就重试，别让播放器卡在退避里。
// 返回的响应可能 body 已关（非 200 时调用方只看状态码）。
func upstreamGet(rawURL string, tries int) (*upstreamResp, error) {
	var last *upstreamResp
	for i := 0; i < tries; i++ {
		res, err := upstreamOnce(rawURL)
		if err == nil && res.StatusCode == http.StatusOK {
			return res, nil
		}
		if err != nil {
			if i >= tries-1 {
				return nil, err
			}
			sleepMs(300 * (i + 1))
			continue
		}
		_ = res.Body.Close()
		last = res
		if i < tries-1 {
			sleepMs(250 * (i + 1))
		}
	}
	return last, nil
}

func sleepMs(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

// ---------------------------------------------------------------- 分片缓存

const segCacheMax = 96 * 1024 * 1024 // 约 20 片，6~7 分钟画面

type segItem struct {
	url string
	buf []byte
}

var (
	segMu    sync.Mutex
	segIndex = map[string]*list.Element{}
	segLRU   = list.New()
	segBytes int
)

func cacheGet(u string) []byte {
	segMu.Lock()
	defer segMu.Unlock()
	el, ok := segIndex[u]
	if !ok {
		return nil
	}
	segLRU.MoveToBack(el) // LRU
	return el.Value.(*segItem).buf
}

func cachePut(u string, buf []byte) {
	if len(buf) > segCacheMax/4 {
		return
	}
	segMu.Lock()
	defer segMu.Unlock()
	if _, ok := segIndex[u]; ok {
		return
	}
	el := segLRU.PushBack(&segItem{url: u, buf: buf})
	segIndex[u] = el
	segBytes += len(buf)
	for segBytes > segCacheMax && segLRU.Len() > 1 {
		front := segLRU.Front()
		it := front.Value.(*segItem)
		segLRU.Remove(front)
		delete(segIndex, it.url)
		segBytes -= len(it.buf)
	}
}

// ---------------------------------------------------------------- 播放列表缓存

type playlistEntry struct {
	at   time.Time
	text string
}

var (
	plMu      sync.Mutex
	playlists = map[string]playlistEntry{}
)

func playlistGet(u string) (string, bool) {
	plMu.Lock()
	defer plMu.Unlock()
	p, ok := playlists[u]
	if !ok || time.Since(p.at) >= time.Minute {
		return "", false
	}
	return p.text, true
}

func playlistPut(u, text string) {
	plMu.Lock()
	defer plMu.Unlock()
	if len(playlists) > 40 {
		playlists = map[string]playlistEntry{}
	}
	playlists[u] = playlistEntry{at: time.Now(), text: text}
}

// ---------------------------------------------------------------- 小工具

// abs 把播放列表里的相对地址补成绝对地址
func abs(u, base string) string {
	low := strings.ToLower(u)
	if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
		return u
	}
	if strings.HasPrefix(u, "/") {
		return "https://cvideo.yanhekt.cn" + u
	}
	return base + "/" + u
}

// enc base64url（无填充，和 Node 的 buffer.toString('base64url') 一致）
func enc(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// dec 解 base64url；两种写法都试一下
func dec(s string) string {
	t := strings.TrimRight(s, "=")
	// base64url（页面用 btoa 后替换 +/ 再删 =）优先，标准字母表也收（Node 的 Buffer 两种都认）
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.StdEncoding} {
		if b, err := enc.DecodeString(t); err == nil {
			return string(b)
		}
		if b, err := enc.DecodeString(s); err == nil {
			return string(b)
		}
	}
	return ""
}

var reURIAttr = regexp.MustCompile(`URI="([^"]+)"`)

// ---------------------------------------------------------------- /p

// proxyHls /p：m3u8 改写成 /p 本地地址，分片直接转发并缓存
func proxyHls(w http.ResponseWriter, r *http.Request, rawURL string) {
	token := tokenOf(r)
	user := verifyToken(token)
	if user == nil {
		w.Header().Set("content-type", "text/plain; charset=utf-8")
		w.WriteHeader(401)
		_, _ = w.Write([]byte("need login"))
		return
	}

	isPlaylist := strings.HasSuffix(strings.Split(rawURL, "?")[0], ".m3u8")

	if isPlaylist {
		if text, ok := playlistGet(rawURL); ok {
			w.Header().Set("content-type", "application/vnd.apple.mpegurl")
			w.Header().Set("cache-control", "no-store")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(text))
			return
		}
	} else if hit := cacheGet(rawURL); hit != nil {
		w.Header().Set("content-type", "video/mp2t")
		w.Header().Set("content-length", strconv.Itoa(len(hit)))
		w.Header().Set("cache-control", "public, max-age=86400, immutable")
		w.WriteHeader(200)
		_, _ = w.Write(hit)
		return
	}

	vid, err := videoToken(token, user.Badge)
	if err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	up, err := upstreamGet(signerSign(rawURL, vid), 3)
	if err != nil {
		w.Header().Set("content-type", "text/plain; charset=utf-8")
		w.WriteHeader(504)
		_, _ = w.Write([]byte("upstream error: " + err.Error()))
		return
	}
	if up == nil || up.StatusCode != http.StatusOK {
		code := 504
		if up != nil {
			code = up.StatusCode
			_ = up.Body.Close()
		}
		w.Header().Set("content-type", "text/plain; charset=utf-8")
		w.WriteHeader(code)
		_, _ = w.Write([]byte("CDN " + strconv.Itoa(code)))
		return
	}
	defer up.Body.Close()

	if isPlaylist {
		body, err := io.ReadAll(up.Body)
		if err != nil {
			w.Header().Set("content-type", "text/plain; charset=utf-8")
			w.WriteHeader(504)
			_, _ = w.Write([]byte("upstream error: " + err.Error()))
			return
		}
		text := string(body)
		base := ""
		if i := strings.LastIndex(rawURL, "/"); i >= 0 {
			base = rawURL[:i]
		}
		lines := strings.Split(text, "\n")
		var out strings.Builder
		out.Grow(len(text) + 256)
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
					return `URI="/p?u=` + enc(abs(sub[1], base)) + `"`
				}))
			default:
				out.WriteString("/p?u=" + enc(abs(t, base)))
			}
			if i < len(lines)-1 {
				out.WriteString("\n")
			}
		}
		outText := out.String()
		playlistPut(rawURL, outText)
		w.Header().Set("content-type", "application/vnd.apple.mpegurl")
		w.Header().Set("cache-control", "no-store")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(outText))
		return
	}

	// 分片：边转发边攒进缓存；浏览器一断（拖动会 abort）就掐断上游，
	// 免得那条唯一的连接还被旧分片占着，拖动要白等一秒多。
	ct := up.Header.Get("content-type")
	if ct == "" {
		ct = "video/mp2t"
	}
	w.Header().Set("content-type", ct)
	w.Header().Set("cache-control", "public, max-age=86400, immutable")
	w.WriteHeader(200)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 64*1024)
	chunks := make([][]byte, 0, 64)
	clientGone := false
	for {
		n, rerr := up.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				clientGone = true
				break
			}
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			chunks = append(chunks, chunk)
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
	if !clientGone && len(chunks) > 0 {
		cachePut(rawURL, joinBytes(chunks))
	}
}

func joinBytes(chunks [][]byte) []byte {
	total := 0
	for _, c := range chunks {
		total += len(c)
	}
	out := make([]byte, 0, total)
	for _, c := range chunks {
		out = append(out, c...)
	}
	return out
}
