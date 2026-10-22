// /dl 下载：把一个讲次的 HLS 分片按顺序拼成单个 .ts 直接回写浏览器
//
// 对应 server.js 的 readAll / resolveMedia / fetchSegment / downloadSession
package main

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

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

// fetchSegment 拉一个分片，失败重试；实在不行就放弃（返回 nil），
// 不要因为一片就把整个下载中止。
func fetchSegment(url, vid string, tries int) []byte {
	if cached := cacheGet(url); cached != nil {
		return cached
	}
	for i := 0; i < tries; i++ {
		up, err := upstreamGet(signerSign(url, vid), 1)
		if err == nil && up != nil {
			if up.StatusCode == http.StatusOK {
				buf, rerr := readAll(up.Body)
				_ = up.Body.Close()
				if rerr == nil {
					cachePut(url, buf)
					return buf
				}
			} else if up.StatusCode == 404 {
				// 404 重试没意义
				_ = up.Body.Close()
				return nil
			} else if up.StatusCode == 403 || up.StatusCode == 429 {
				// 多半是 CDN 嫌请求太密 → 退得久一点再试
				_ = up.Body.Close()
				sleepMs(1200 * (i + 1))
				continue
			} else {
				_ = up.Body.Close()
			}
		}
		if i < tries-1 {
			sleepMs(300 * (i + 1))
		}
	}
	return nil
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

	w.Header().Set("content-type", ctype)
	w.Header().Set("content-disposition",
		`attachment; filename="lecture`+ext+`"; filename*=UTF-8''`+encodeURIComponent(title+ext))
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(200)
	started = true

	flusher, _ := w.(http.Flusher)
	doneSegs, failedSegs := 0, 0
	gone := false
	for i := 0; i < len(order); i++ {
		if gone || r.Context().Err() != nil {
			break
		}
		buf := fetchSegment(order[i], vid, 5)
		if buf == nil {
			failedSegs++
			if i == 0 {
				break // 第一片就取不到：多半是地址/权限问题，别硬下
			}
			continue // 中间断一片：跳过，接着下
		}
		doneSegs++
		if _, err := w.Write(buf); err != nil {
			gone = true
			break
		}
		if flusher != nil {
			flusher.Flush()
		}
		if (i+1)%100 == 0 {
			fmt.Printf("  下载进度 %d/%d\n", i+1, len(order))
		}
		if (i+1)%10 == 0 {
			sleepMs(200) // 稍微放慢，别把 CDN 惹急
		}
	}
	fmt.Printf("下载结束：成功 %d 片，失败 %d 片\n", doneSegs, failedSegs)
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
