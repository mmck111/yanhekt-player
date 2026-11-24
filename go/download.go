// /dl 下载：把一个讲次的 HLS 分片按顺序拼成单个 .ts 直接回写浏览器
//
// 对应 server.js 的 readAll / resolveMedia / fetchSegment / downloadSession
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 专用于老平台 /dl 离线下载的连接池：与播放通道彻底隔离，增大缓冲并支持 2~3 路并发稳态拉片
var dlOldClient = &http.Client{
	Transport: &http.Transport{
		TLSClientConfig:       insecureTLS,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          32,
		MaxConnsPerHost:       4,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   8 * time.Second,
		ResponseHeaderTimeout: 18 * time.Second,
		ReadBufferSize:        64 << 10,
		WriteBufferSize:       64 << 10,
		DialContext:           (&net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
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

// readBodyStallSafe 在带空闲超时的情况下读完分片响应体；卡死超过 idleTimeout 立刻中断以便重连
func readBodyStallSafe(ctx context.Context, cancelSeg context.CancelFunc, rc io.ReadCloser, idleTimeout time.Duration, maxBytes int64) ([]byte, error) {
	defer rc.Close()
	var buf bytes.Buffer
	tmp := make([]byte, 32*1024)
	timer := time.AfterFunc(idleTimeout, cancelSeg)
	defer timer.Stop()
	var total int64
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		timer.Reset(idleTimeout)
		n, err := rc.Read(tmp)
		if n > 0 {
			total += int64(n)
			if maxBytes > 0 && total > maxBytes {
				return nil, fmt.Errorf("分片体积超过上限")
			}
			buf.Write(tmp[:n])
		}
		if err != nil {
			if err == io.EOF {
				return buf.Bytes(), nil
			}
			return nil, err
		}
	}
}

// ---------------------------------------------------------------- 播放列表解析

type mediaInfo struct {
	segments      []string
	mapURI        string
	duration      float64
	encrypted     bool
	endlist       bool
	mediaSequence *string
}

var (
	reStreamInf   = regexp.MustCompile(`(?i)#EXT-X-STREAM-INF:`)
	reMapURI      = regexp.MustCompile(`(?i)#EXT-X-MAP:[^\n]*?URI="([^"]+)"`)
	reExtInf      = regexp.MustCompile(`#EXTINF:\s*([\d.]+)`)
	reMediaSeq    = regexp.MustCompile(`(?i)#EXT-X-MEDIA-SEQUENCE:\s*(\d+)`)
	reMp4Seg      = regexp.MustCompile(`(?i)\.(m4s|mp4)(\?|$)`)
	reKeyLine     = regexp.MustCompile(`(?i)#EXT-X-KEY:`)
	reIllegalName = regexp.MustCompile("[\\\\/:*?\"<>|\r\n\t]")
)

func readAll(rc io.Reader) ([]byte, error) { return io.ReadAll(rc) }

// resolveMedia 取一个播放列表（如果是"多码率总表"就顺着往下找到真正的分片列表）
func resolveMedia(src, vid string) (*mediaInfo, error) {
	u := src
	text := ""
	base := ""
	for guard := 0; guard < 4; guard++ {
		up, err := upstreamGet(signerSign(u, vid), 3)
		if err != nil {
			return nil, err
		}
		if up == nil || up.StatusCode != http.StatusOK {
			if up == nil {
				return nil, fmt.Errorf("取播放列表失败")
			}
			_ = up.Body.Close()
			return nil, fmt.Errorf("取播放列表失败（CDN %d）", up.StatusCode)
		}
		body, err := readAll(up.Body)
		_ = up.Body.Close()
		if err != nil {
			return nil, err
		}
		text = string(body)
		base = u[:strings.LastIndex(u, "/")]
		if !reStreamInf.MatchString(text) {
			break
		}
		variant := ""
		for _, l := range strings.Split(text, "\n") {
			t := strings.TrimSpace(l)
			if t != "" && !strings.HasPrefix(t, "#") {
				variant = t
				break
			}
		}
		if variant == "" {
			return nil, fmt.Errorf("播放列表里找不到可用的清晰度")
		}
		u = abs(variant, base)
	}

	m := &mediaInfo{}
	for _, l := range strings.Split(text, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		m.segments = append(m.segments, abs(t, base))
	}
	if sub := reMapURI.FindStringSubmatch(text); len(sub) > 1 {
		m.mapURI = abs(sub[1], base)
	}
	for _, sub := range reExtInf.FindAllStringSubmatch(text, -1) {
		f, _ := strconv.ParseFloat(sub[1], 64)
		m.duration += f
	}
	// 加密判断：跟着 #EXT-X-KEY 行看 METHOD 是不是 NONE（Go 正则不支持 lookahead）
	for _, l := range strings.Split(text, "\n") {
		if !reKeyLine.MatchString(l) {
			continue
		}
		up := strings.ToUpper(l)
		idx := strings.Index(up, "METHOD=")
		if idx < 0 {
			continue
		}
		if !strings.HasPrefix(up[idx+len("METHOD="):], "NONE") {
			m.encrypted = true
			break
		}
	}
	m.endlist = strings.Contains(strings.ToUpper(text), "#EXT-X-ENDLIST")
	if sub := reMediaSeq.FindStringSubmatch(text); len(sub) > 1 {
		s := sub[1]
		m.mediaSequence = &s
	}
	return m, nil
}

// fetchSegmentCtx 拉一个分片：带单分片超时与空闲看门狗，遇 403/429 自动降速退避，遇 401/403 必要时刷新 token
func fetchSegmentCtx(ctx context.Context, rawURL string, getVid func(force bool) string, onThrottle func(), tries int) ([]byte, int) {
	if cached := cacheGet(rawURL); cached != nil {
		return cached, http.StatusOK
	}
	vid := getVid(false)
	lastCode := 0
	for i := 0; i < tries; i++ {
		if ctx.Err() != nil {
			return nil, lastCode
		}
		segCtx, segCancel := context.WithTimeout(ctx, 55*time.Second)
		signed := signerSign(rawURL, vid)
		req, err := http.NewRequestWithContext(segCtx, "GET", signed, nil)
		if err != nil {
			segCancel()
			return nil, lastCode
		}
		setVideoHeaders(req)
		resp, err := dlOldClient.Do(req)
		if err == nil && resp != nil {
			lastCode = resp.StatusCode
			if resp.StatusCode == http.StatusOK {
				buf, rerr := readBodyStallSafe(segCtx, segCancel, resp.Body, 25*time.Second, 32<<20)
				segCancel()
				if rerr == nil && len(buf) > 0 {
					cachePut(rawURL, buf)
					return buf, http.StatusOK
				}
			} else {
				_ = resp.Body.Close()
				segCancel()
				if resp.StatusCode == 404 {
					return nil, 404
				}
				if resp.StatusCode == 401 || resp.StatusCode == 403 {
					if newVid := getVid(true); newVid != "" {
						vid = newVid
					}
					if onThrottle != nil {
						onThrottle()
					}
					if !sleepCtx(ctx, 900*(i+1)) {
						return nil, lastCode
					}
					continue
				}
				if resp.StatusCode == 429 || resp.StatusCode >= 500 {
					if onThrottle != nil {
						onThrottle()
					}
					if !sleepCtx(ctx, 800*(i+1)) {
						return nil, lastCode
					}
					continue
				}
			}
		} else {
			segCancel()
		}
		if i < tries-1 && !sleepCtx(ctx, 300*(i+1)) {
			return nil, lastCode
		}
	}
	return nil, lastCode
}

// ---------------------------------------------------------------- /dl

type dlOpt struct {
	course  string
	session string
	kind    string // main / vga
	probe   bool
}

// downloadSession 课程录像是 HLS（一节课被切成几百个分片），浏览器里没法右键另存。
// 这里按顺序把分片拉下来、边拉边写回浏览器，得到一个能直接播放的整文件。
//
// 两个坑：
//  1. 分片多（两小时的课 700+ 片），中间偶发失败很常见 —— 必须重试 + 跳过
//  2. 播放列表可能是多码率总表，要先顺着 #EXT-X-STREAM-INF 找到真正的分片列表
func downloadSession(w http.ResponseWriter, r *http.Request, opt dlOpt, token string, user *userInfo) {
	started := false
	fail := func(code int, msg string) {
		if !started {
			w.Header().Set("content-type", "text/plain; charset=utf-8")
			w.WriteHeader(code)
		}
		_, _ = w.Write([]byte(msg))
	}

	d := getCourse(opt.course, token, false)
	if d != nil && d.fail {
		fail(500, "下载出错："+d.errMsg)
		return
	}
	if d == nil || !d.ok {
		msg := ""
		if d != nil {
			msg = d.errMsg
		}
		fail(404, "读取课程失败："+msg)
		return
	}
	var s *sessionRow
	for i := range d.sessions {
		if jstr(d.sessions[i].ID) == opt.session {
			s = &d.sessions[i]
			break
		}
	}
	if s == nil {
		fail(404, "找不到这一讲")
		return
	}
	src := s.mainURL()
	if opt.kind == "vga" {
		src = s.vgaURL()
	}
	if src == "" {
		label := "全景"
		if opt.kind == "vga" {
			label = "课件"
		}
		fail(404, "这一讲没有"+label+"画面")
		return
	}

	vid, err := videoToken(token, user.Badge)
	if err != nil {
		fail(500, "下载出错："+err.Error())
		return
	}
	media, err := resolveMedia(src, vid)
	if err != nil {
		fail(500, "下载出错："+err.Error())
		return
	}

	// 诊断模式：不下文件，只把这一讲的结构报出来（片数、时长、是否加密……）
	if opt.probe {
		var segDur any
		if n := len(media.segments); n > 0 {
			segDur = round2(media.duration / float64(n))
		}
		var first, last any
		if len(media.segments) > 0 {
			first = media.segments[0]
			last = media.segments[len(media.segments)-1]
		}
		initSeg := "no"
		if media.mapURI != "" {
			initSeg = "yes"
		}
		var seq any
		if media.mediaSequence != nil {
			seq = *media.mediaSequence
		}
		writeJSON(w, 200, map[string]any{
			"ok": true,
			"session": map[string]any{
				"id": s.ID, "date": s.Date, "title": s.Title, "duration": s.Duration,
			},
			"segments":           len(media.segments),
			"totalDurationSec":   int(media.duration + 0.5),
			"segmentDurationSec": segDur,
			"encrypted":          media.encrypted,
			"hasEndList":         media.endlist,
			"mediaSequence":      seq,
			"initSegment":        initSeg,
			"firstSegment":       first,
			"lastSegment":        last,
		})
		return
	}

	if len(media.segments) == 0 {
		fail(502, "播放列表里没有分片")
		return
	}
	if media.encrypted {
		fail(501, "这批录像用了加密的播放列表，暂时不支持下载")
		return
	}

	order := media.segments
	if media.mapURI != "" {
		order = append([]string{media.mapURI}, media.segments...)
	}
	kindLabel := "全景"
	if opt.kind == "vga" {
		kindLabel = "课件"
	}
	courseName := ""
	if d.course != nil {
		courseName = jstr(d.course.Name)
	}
	title := courseName + "_" + s.Date + "_" + kindLabel
	title = reIllegalName.ReplaceAllString(title, "_")
	if runes := []rune(title); len(runes) > 80 {
		title = string(runes[:80])
	}
	tsLike := true
	if len(order) > 0 && reMp4Seg.MatchString(order[0]) {
		tsLike = false
	}
	ext := ".ts"
	ctype := "video/mp2t"
	if !tsLike {
		ext = ".mp4"
		ctype = "video/mp4"
	}

	fmt.Printf("下载开始：%s，共 %d 片，约 %d 分钟\n", title, len(media.segments), int(media.duration/60+0.5))
	dlCtx, dlCancel := context.WithCancel(r.Context())
	defer dlCancel()
	startSeg, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("startSeg")))
	startBytes, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("startBytes")), 10, 64)
	if startSeg < 0 || startSeg >= len(order) {
		startSeg = 0
		startBytes = 0
	}

	queueID := strings.TrimSpace(r.URL.Query().Get("queueId"))
	tracker := dlTrackBindOrStart(queueID, "old", title+ext, len(order), 0)
	tracker.setReqPath(r.URL.RequestURI())
	if startSeg > 0 {
		tracker.setResumeInit(startSeg, startBytes)
		w.Header().Set("X-YHKT-Resume-Seg", strconv.Itoa(startSeg))
	}
	tracker.setCancel(dlCancel)

	w.Header().Set("content-type", ctype)
	w.Header().Set("content-disposition",
		`attachment; filename="lecture`+ext+`"; filename*=UTF-8''`+encodeURIComponent(title+ext))
	w.Header().Set("cache-control", "no-store")
	w.Header().Set("X-YHKT-DL-ID", tracker.ID)
	w.WriteHeader(200)
	started = true

	var vidMu sync.Mutex
	curVid := vid
	var lastVidRefresh time.Time
	getVid := func(force bool) string {
		vidMu.Lock()
		defer vidMu.Unlock()
		if force && time.Since(lastVidRefresh) > 15*time.Second {
			vtMu.Lock()
			delete(vtCache, token)
			vtMu.Unlock()
			if nv, err := videoToken(token, user.Badge); err == nil && nv != "" {
				curVid = nv
				lastVidRefresh = time.Now()
			}
		}
		return curVid
	}

	// 自适应双路预取窗口：默认 2 并发加快提速；一旦检测到 403/429 自动降为单路并发防封限流
	var throttled atomic.Bool
	onThrottle := func() { throttled.Store(true) }

	flusher, _ := w.(http.Flusher)
	doneSegs, failedSegs, consecutiveFails := startSeg, 0, 0
	doneBytes := startBytes
	gone := false
	fatalHalf := false

	type segResult struct {
		idx  int
		buf  []byte
		code int
	}
	const winSize = 4
	results := make(map[int][]byte, winSize+2)
	var nextAssign = startSeg
	inFlight := 0
	resCh := make(chan segResult, winSize+2)

	maxWorkers := func() int {
		if throttled.Load() {
			return 1
		}
		return 2
	}
	nextWrite := startSeg
	dispatchNext := func() {
		for nextAssign < len(order) && inFlight < maxWorkers() && nextAssign-nextWrite < winSize {
			idx := nextAssign
			nextAssign++
			inFlight++
			go func(segIdx int, segURL string) {
				b, code := fetchSegmentCtx(dlCtx, segURL, getVid, onThrottle, 6)
				resCh <- segResult{idx: segIdx, buf: b, code: code}
			}(idx, order[idx])
		}
	}

	for nextWrite < len(order) {
		if gone || dlCtx.Err() != nil || tracker.isCancelled() {
			gone = true
			break
		}
		dispatchNext()
		if inFlight == 0 {
			break
		}
		res := <-resCh
		inFlight--
		if res.buf == nil {
			// 记录 nil 哨兵以便按序检查连续失败
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
				failedSegs++
				consecutiveFails++
				if nextWrite == startSeg {
					fatalHalf = true
					gone = true
					break
				}
				if consecutiveFails >= 8 {
					fatalHalf = true
					gone = true
					break
				}
				nextWrite++
				continue
			}
			consecutiveFails = 0
			doneSegs++
			if _, err := w.Write(buf); err != nil {
				gone = true
				break
			}
			doneBytes += int64(len(buf))
			nextWrite++
			tracker.update(nextWrite, doneBytes)
			if flusher != nil {
				flusher.Flush()
			}
			if doneSegs%100 == 0 {
				fmt.Printf("  下载进度 %d/%d\n", doneSegs, len(order))
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
	} else if doneSegs == 0 {
		tracker.finish("error", "分片拉取失败")
	} else {
		tracker.finish("done", "")
	}
	fmt.Printf("下载结束：成功 %d 片，失败 %d 片\n", doneSegs, failedSegs)
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
