package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ffmpegFindMu    sync.Mutex
	ffmpegCached    string
	ffmpegCheckedAt time.Time

	// 限制同时只跑 1 路双画面 FFmpeg 合成，防止批量下载时多路转码抢占全部 CPU
	dualEncodeSem = make(chan struct{}, 1)

	reFFmpegDur   = regexp.MustCompile(`Duration:\s*(\d+):(\d+):(\d+(?:\.\d+)?)`)
	reFFmpegAudio = regexp.MustCompile(`Stream\s+#\d+:\d+.*:\s*Audio:`)
	reFFmpegVideo = regexp.MustCompile(`Stream\s+#\d+:\d+.*:\s*Video:`)
)

func findFFmpeg() string {
	ffmpegFindMu.Lock()
	defer ffmpegFindMu.Unlock()

	if ffmpegCached != "" {
		if fi, err := os.Stat(ffmpegCached); err == nil && !fi.IsDir() {
			return ffmpegCached
		}
		ffmpegCached = ""
	}
	if time.Since(ffmpegCheckedAt) < 15*time.Second {
		return ""
	}
	ffmpegCheckedAt = time.Now()

	exeName := "ffmpeg"
	if runtime.GOOS == "windows" {
		exeName = "ffmpeg.exe"
	}

	var cands []string
	if ep, err := os.Executable(); err == nil && ep != "" {
		dir := filepath.Dir(ep)
		cands = append(cands,
			filepath.Join(dir, exeName),
			filepath.Join(dir, "tools", exeName),
			filepath.Join(dir, "bin", exeName),
		)
	}
	if localApp := os.Getenv("LOCALAPPDATA"); localApp != "" {
		cands = append(cands,
			filepath.Join(localApp, "yanhekt", exeName),
			filepath.Join(localApp, "yanhekt", "bin", exeName),
		)
	}
	for _, p := range cands {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			ffmpegCached = p
			return p
		}
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil && p != "" {
		ffmpegCached = p
		return p
	}
	if runtime.GOOS == "windows" {
		var winCands []string
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			winCands = append(winCands, filepath.Join(la, "Microsoft", "WinGet", "Links", "ffmpeg.exe"))
			if matches, _ := filepath.Glob(filepath.Join(la, "Microsoft", "WinGet", "Packages", "Gyan.FFmpeg*", "*", "bin", "ffmpeg.exe")); len(matches) > 0 {
				winCands = append(winCands, matches...)
			}
		}
		if pd := os.Getenv("ProgramData"); pd != "" {
			winCands = append(winCands, filepath.Join(pd, "chocolatey", "bin", "ffmpeg.exe"))
		}
		if up := os.Getenv("USERPROFILE"); up != "" {
			winCands = append(winCands, filepath.Join(up, "scoop", "shims", "ffmpeg.exe"))
		}
		winCands = append(winCands,
			`C:\ffmpeg\bin\ffmpeg.exe`,
			`C:\Program Files\ffmpeg\bin\ffmpeg.exe`,
			`C:\Program Files (x86)\ffmpeg\bin\ffmpeg.exe`,
		)
		for _, p := range winCands {
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				ffmpegCached = p
				return p
			}
		}
	}
	return ""
}

const msgMissingFFmpeg = "未检测到 ffmpeg，无法在本地合成双画面视频；请将 ffmpeg.exe 放到安装目录或执行 winget install Gyan.FFmpeg 后重试"

// fileSinkWriter 复用现有下载器直接落盘到临时文件，零重复拉流/重签逻辑。
type fileSinkWriter struct {
	hdr    http.Header
	f      *os.File
	status int
	errBuf bytes.Buffer
}

func newFileSinkWriter(f *os.File) *fileSinkWriter {
	return &fileSinkWriter{hdr: make(http.Header), f: f, status: 200}
}

func (s *fileSinkWriter) Header() http.Header { return s.hdr }
func (s *fileSinkWriter) Flush()              {}
func (s *fileSinkWriter) WriteHeader(code int) {
	s.status = code
}
func (s *fileSinkWriter) Write(p []byte) (int, error) {
	if s.status >= 400 {
		if s.errBuf.Len() < 2048 {
			remain := 2048 - s.errBuf.Len()
			if len(p) > remain {
				s.errBuf.Write(p[:remain])
			} else {
				s.errBuf.Write(p)
			}
		}
		return len(p), nil
	}
	return s.f.Write(p)
}

func dualStageErr(queueID string, w http.ResponseWriter, code int, msg string) {
	if queueID != "" {
		dlTrackSetStatus(queueID, "error", msg)
	}
	w.Header().Set("content-type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(msg))
}

func y2FindStreamExact(info *y2SubInfo, wants ...string) (y2Stream, bool) {
	if info == nil {
		return y2Stream{}, false
	}
	for _, want := range wants {
		for _, s := range info.Streams {
			if s.Type == want && s.URL != "" {
				return s, true
			}
		}
	}
	return y2Stream{}, false
}

type dualProbeInfo struct {
	duration float64
	hasVideo bool
	hasAudio bool
}

func parseFFmpegProbeOutput(out string) dualProbeInfo {
	var info dualProbeInfo
	if m := reFFmpegDur.FindStringSubmatch(out); len(m) == 4 {
		hh, _ := strconv.ParseFloat(m[1], 64)
		mm, _ := strconv.ParseFloat(m[2], 64)
		ss, _ := strconv.ParseFloat(m[3], 64)
		info.duration = hh*3600 + mm*60 + ss
	}
	info.hasVideo = reFFmpegVideo.MatchString(out)
	info.hasAudio = reFFmpegAudio.MatchString(out)
	return info
}

func probeDualInput(ctx context.Context, ffmpegExe, path string) dualProbeInfo {
	pCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(pCtx, ffmpegExe, "-hide_banner", "-i", path)
	hideCmdWindow(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run()
	return parseFFmpegProbeOutput(stderr.String())
}

func buildFFmpegDualArgs(camPath, vgaPath, audPath, outPath string, audioIdx int) []string {
	args := []string{
		"-y",
		"-i", camPath,
		"-i", vgaPath,
	}
	filter := "[0:v]setpts=PTS-STARTPTS,fps=25,scale=-2:720:flags=bicubic,setsar=1,format=yuv420p[v0];" +
		"[1:v]setpts=PTS-STARTPTS,fps=25,scale=-2:720:flags=bicubic,setsar=1,format=yuv420p[v1];" +
		"[v0][v1]hstack=inputs=2:shortest=0[vout]"

	audioMapLabel := ""
	switch audioIdx {
	case 0:
		filter += ";[0:a:0]asetpts=PTS-STARTPTS,aresample=async=1[aout]"
		audioMapLabel = "[aout]"
	case 1:
		filter += ";[1:a:0]asetpts=PTS-STARTPTS,aresample=async=1[aout]"
		audioMapLabel = "[aout]"
	case 2:
		if audPath != "" {
			args = append(args, "-i", audPath)
			filter += ";[2:a:0]asetpts=PTS-STARTPTS,aresample=async=1[aout]"
			audioMapLabel = "[aout]"
		}
	}

	args = append(args, "-filter_complex", filter, "-map", "[vout]")
	if audioMapLabel != "" {
		args = append(args, "-map", audioMapLabel, "-c:a", "aac", "-b:a", "128k", "-ac", "2")
	}
	return append(args,
		"-c:v", "libx264", "-preset", "superfast", "-crf", "23", "-pix_fmt", "yuv420p",
		"-threads", "0", "-movflags", "+faststart",
		"-progress", "pipe:1", "-nostats", "-loglevel", "error",
		outPath,
	)
}

func parseFFmpegProgressLine(line string, totalDur float64, curPct int) int {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "out_time_us=") || totalDur <= 0 {
		return curPct
	}
	us, _ := strconv.ParseInt(strings.TrimPrefix(line, "out_time_us="), 10, 64)
	sec := float64(us) / 1e6
	ratio := sec / totalDur
	if ratio < 0 {
		ratio = 0
	} else if ratio > 1 {
		ratio = 1
	}
	pct := 70 + int(ratio*28)
	if pct > 98 {
		pct = 98
	}
	if pct > curPct {
		return pct
	}
	return curPct
}

func runFFmpegDualMerge(ctx context.Context, ffmpegExe, camPath, vgaPath, audPath, outPath string, tracker *dlTrackItem, baseBytes int64) error {
	camInfo := probeDualInput(ctx, ffmpegExe, camPath)
	vgaInfo := probeDualInput(ctx, ffmpegExe, vgaPath)
	if !camInfo.hasVideo || !vgaInfo.hasVideo {
		return fmt.Errorf("双画面源文件缺少有效视频轨")
	}

	totalDur := camInfo.duration
	if vgaInfo.duration > totalDur {
		totalDur = vgaInfo.duration
	}

	tracker.setStage("3/3 等待双画面合成队列")
	select {
	case dualEncodeSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-dualEncodeSem }()

	tracker.setStage("3/3 合成双画面")

	audioIdx := -1
	switch {
	case camInfo.hasAudio:
		audioIdx = 0
	case vgaInfo.hasAudio:
		audioIdx = 1
	case audPath != "":
		if audInfo := probeDualInput(ctx, ffmpegExe, audPath); audInfo.hasAudio {
			audioIdx = 2
		}
	}
	args := buildFFmpegDualArgs(camPath, vgaPath, audPath, outPath, audioIdx)

	cmd := exec.CommandContext(ctx, ffmpegExe, args...)
	hideCmdWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 ffmpeg 失败：%v", err)
	}

	sc := bufio.NewScanner(stdout)
	lastPct := 70
	tracker.update(lastPct, baseBytes)
	for sc.Scan() {
		if pct := parseFFmpegProgressLine(sc.Text(), totalDur, lastPct); pct > lastPct {
			lastPct = pct
			tracker.update(pct, baseBytes)
		}
	}

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		detail := strings.TrimSpace(errBuf.String())
		if len(detail) > 200 {
			detail = detail[len(detail)-200:]
		}
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("ffmpeg 合成失败：%s", detail)
	}
	return nil
}

func streamDualMergedResponse(w http.ResponseWriter, r *http.Request, dlCtx context.Context, outMp4Path, attachFilename string, tracker *dlTrackItem) {
	f, err := os.Open(outMp4Path)
	if err != nil {
		tracker.finish("error", "读取合成视频失败："+err.Error())
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() <= 0 {
		tracker.finish("error", "合成后的双画面文件为空")
		return
	}

	tracker.setStage("4/4 写入目标文件")
	tracker.setTotalBytes(fi.Size())
	w.Header().Set("content-type", "video/mp4")
	w.Header().Set("content-disposition",
		`attachment; filename="lecture.mp4"; filename*=UTF-8''`+encodeURIComponent(attachFilename))
	w.Header().Set("content-length", strconv.FormatInt(fi.Size(), 10))
	w.Header().Set("cache-control", "no-store")
	w.Header().Set("X-YHKT-DL-ID", tracker.ID)
	w.WriteHeader(200)

	buf := make([]byte, 64*1024)
	var sent int64
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				if tracker.isCancelled() {
					tracker.finish("cancelled", "用户已取消")
				} else {
					tracker.finish("interrupted", "写入输出中断")
				}
				return
			}
			sent += int64(n)
			tracker.update(99, sent)
		}
		if rerr != nil {
			if rerr == io.EOF {
				break
			}
			tracker.finish("error", "读取合成文件出错："+rerr.Error())
			return
		}
		if dlCtx.Err() != nil || tracker.isCancelled() {
			tracker.finish("cancelled", "用户已取消")
			return
		}
	}
	tracker.markDone()
}

func dualSafeBaseName(raw, fallback string) string {
	name := strings.TrimSpace(raw)
	if name == "" {
		name = fallback
	}
	name = strings.TrimSuffix(name, ".mp4")
	name = strings.TrimSuffix(name, ".ts")
	name = reIllegalName.ReplaceAllString(name, "_")
	if r := []rune(name); len(r) > 80 {
		name = string(r[:80])
	}
	return name
}

func downloadOldDual(w http.ResponseWriter, r *http.Request, opt dlOpt, d *courseData, s *sessionRow, token string, user *userInfo) {
	queueID := strings.TrimSpace(r.URL.Query().Get("queueId"))
	if s.mainURL() == "" || s.vgaURL() == "" {
		dualStageErr(queueID, w, 400, "这一讲缺少全景或课件画面，无法合成双画面，请改选单画面下载")
		return
	}
	ffmpegExe := findFFmpeg()
	if ffmpegExe == "" {
		dualStageErr(queueID, w, 422, msgMissingFFmpeg)
		return
	}

	courseName := "课程"
	if d.course != nil && jstr(d.course.Name) != "" {
		courseName = jstr(d.course.Name)
	}
	titleArg := strings.TrimSpace(r.URL.Query().Get("title"))
	if titleArg == "" {
		titleArg = courseName + "_" + s.Date + "_双画面"
	}
	fileName := dualSafeBaseName(titleArg, courseName+"_双画面") + ".mp4"

	tmpDir, err := os.MkdirTemp("", "yhkt-dual-*")
	if err != nil {
		dualStageErr(queueID, w, 500, "创建临时目录失败："+err.Error())
		return
	}
	defer os.RemoveAll(tmpDir)

	dlCtx, dlCancel := context.WithCancel(r.Context())
	defer dlCancel()

	tracker := dlTrackBindOrStart(queueID, "old", fileName, 100, 0)
	tracker.setReqPath(r.URL.RequestURI())
	tracker.setCancel(dlCancel)

	runPart := func(stage string, kind string, path string, basePct int, baseBytes int64) (int64, error) {
		tracker.setStage(stage)
		tracker.update(basePct, baseBytes)
		f, ferr := os.Create(path)
		if ferr != nil {
			return 0, ferr
		}
		sink := newFileSinkWriter(f)
		subTracker := dlTrackStartQuiet(func(done, total int, bytes, _ int64) {
			pct := basePct
			if total > 0 {
				pct = basePct + (done*33)/total
			}
			tracker.update(pct, baseBytes+bytes)
		})
		subReq := r.Clone(dlCtx)
		subOpt := dlOpt{
			course:     opt.course,
			session:    opt.session,
			kind:       kind,
			extTracker: subTracker,
		}
		downloadSession(sink, subReq, subOpt, token, user)
		_ = f.Close()

		if dlCtx.Err() != nil || tracker.isCancelled() {
			return 0, context.Canceled
		}
		if sink.status >= 400 {
			msg := strings.TrimSpace(sink.errBuf.String())
			if msg == "" {
				msg = fmt.Sprintf("HTTP %d", sink.status)
			}
			return 0, fmt.Errorf("%s", msg)
		}
		st, errMsg := subTracker.snapshotState()
		if st != "" && st != "done" && st != "running" {
			if errMsg == "" {
				errMsg = "分片下载未完成（" + st + "）"
			}
			return 0, fmt.Errorf("%s", errMsg)
		}
		fi, serr := os.Stat(path)
		if serr != nil || fi.Size() <= 0 {
			return 0, fmt.Errorf("未收到视频数据")
		}
		return fi.Size(), nil
	}

	camPath := filepath.Join(tmpDir, "cam.bin")
	camBytes, err := runPart("1/3 下载全景画面", "main", camPath, 0, 0)
	if err != nil {
		if tracker.isCancelled() || dlCtx.Err() != nil {
			tracker.finish("cancelled", "用户已取消")
		} else {
			tracker.finish("error", "全景画面下载失败："+err.Error())
		}
		dualStageErr("", w, 502, "下载全景画面失败："+err.Error())
		return
	}

	vgaPath := filepath.Join(tmpDir, "vga.bin")
	vgaBytes, err := runPart("2/3 下载课件画面", "vga", vgaPath, 34, camBytes)
	if err != nil {
		if tracker.isCancelled() || dlCtx.Err() != nil {
			tracker.finish("cancelled", "用户已取消")
		} else {
			tracker.finish("error", "课件画面下载失败："+err.Error())
		}
		dualStageErr("", w, 502, "下载课件画面失败："+err.Error())
		return
	}

	outMp4 := filepath.Join(tmpDir, "dual.mp4")
	totalRawBytes := camBytes + vgaBytes
	if err := runFFmpegDualMerge(dlCtx, ffmpegExe, camPath, vgaPath, "", outMp4, tracker, totalRawBytes); err != nil {
		if tracker.isCancelled() || dlCtx.Err() != nil {
			tracker.finish("cancelled", "用户已取消")
		} else {
			tracker.finish("error", err.Error())
		}
		dualStageErr("", w, 502, err.Error())
		return
	}
	_ = os.Remove(camPath)
	_ = os.Remove(vgaPath)

	streamDualMergedResponse(w, r, dlCtx, outMp4, fileName, tracker)
}

func handleY2DualDownload(w http.ResponseWriter, r *http.Request, name, queueID string, info *y2SubInfo, id *y2Identity) {
	camSt, okCam := y2FindStreamExact(info, "camera", "room")
	vgaSt, okVga := y2FindStreamExact(info, "screen")
	if !okCam || !okVga || camSt.URL == vgaSt.URL {
		dualStageErr(queueID, w, 400, "这一讲不同时包含教师和课件画面，请改选单画面下载")
		return
	}
	ffmpegExe := findFFmpeg()
	if ffmpegExe == "" {
		dualStageErr(queueID, w, 422, msgMissingFFmpeg)
		return
	}

	fileName := dualSafeBaseName(name, "y2_双画面") + ".mp4"
	tmpDir, err := os.MkdirTemp("", "yhkt-y2dual-*")
	if err != nil {
		dualStageErr(queueID, w, 500, "创建临时目录失败："+err.Error())
		return
	}
	defer os.RemoveAll(tmpDir)

	dlCtx, dlCancel := context.WithCancel(r.Context())
	defer dlCancel()

	tracker := dlTrackBindOrStart(queueID, "y2", fileName, 100, 0)
	tracker.setReqPath(r.URL.RequestURI())
	tracker.setCancel(dlCancel)

	fetchY2Stream := func(stage, rawURL, partName, path string, basePct, spanPct int, baseBytes int64) (int64, error) {
		tracker.setStage(stage)
		tracker.update(basePct, baseBytes)
		f, ferr := os.Create(path)
		if ferr != nil {
			return 0, ferr
		}
		sink := newFileSinkWriter(f)
		subTracker := dlTrackStartQuiet(func(done, total int, bytes, totalBytes int64) {
			pct := basePct
			if total > 1 {
				pct = basePct + (done*spanPct)/total
			} else if totalBytes > 0 {
				pct = basePct + int((bytes*int64(spanPct))/totalBytes)
			}
			tracker.update(pct, baseBytes+bytes)
		})
		subReq := r.Clone(dlCtx)
		var derr error
		if strings.HasSuffix(strings.ToLower(strings.Split(rawURL, "?")[0]), ".m3u8") {
			derr = y2DownloadHLSWithTracker(sink, subReq, rawURL, partName, id, "", subTracker)
		} else {
			derr = y2DownloadMP4(sink, subReq, rawURL, id, y2ProxyOpts{
				filename:   partName + ".mp4",
				ext:        ".mp4",
				reqPath:    r.URL.RequestURI(),
				extTracker: subTracker,
			})
		}
		_ = f.Close()

		if dlCtx.Err() != nil || tracker.isCancelled() {
			return 0, context.Canceled
		}
		if derr != nil {
			return 0, derr
		}
		if sink.status >= 400 {
			msg := strings.TrimSpace(sink.errBuf.String())
			if msg == "" {
				msg = fmt.Sprintf("HTTP %d", sink.status)
			}
			return 0, fmt.Errorf("%s", msg)
		}
		st, errMsg := subTracker.snapshotState()
		if st != "" && st != "done" && st != "running" {
			if errMsg == "" {
				errMsg = "流拉取未完成（" + st + "）"
			}
			return 0, fmt.Errorf("%s", errMsg)
		}
		fi, serr := os.Stat(path)
		if serr != nil || fi.Size() <= 0 {
			return 0, fmt.Errorf("未收到视频数据")
		}
		return fi.Size(), nil
	}

	camPath := filepath.Join(tmpDir, "cam.bin")
	camBytes, err := fetchY2Stream("1/3 下载教师画面", camSt.URL, "cam", camPath, 0, 32, 0)
	if err != nil {
		if tracker.isCancelled() || dlCtx.Err() != nil {
			tracker.finish("cancelled", "用户已取消")
		} else {
			tracker.finish("error", "教师画面下载失败："+err.Error())
		}
		dualStageErr("", w, 502, "下载教师画面失败："+err.Error())
		return
	}

	vgaPath := filepath.Join(tmpDir, "vga.bin")
	vgaBytes, err := fetchY2Stream("2/3 下载课件画面", vgaSt.URL, "vga", vgaPath, 33, 33, camBytes)
	if err != nil {
		if tracker.isCancelled() || dlCtx.Err() != nil {
			tracker.finish("cancelled", "用户已取消")
		} else {
			tracker.finish("error", "课件画面下载失败："+err.Error())
		}
		dualStageErr("", w, 502, "下载课件画面失败："+err.Error())
		return
	}

	totalRawBytes := camBytes + vgaBytes
	audPath := ""
	camMeta := probeDualInput(dlCtx, ffmpegExe, camPath)
	vgaMeta := probeDualInput(dlCtx, ffmpegExe, vgaPath)
	if !camMeta.hasAudio && !vgaMeta.hasAudio {
		audURL := strings.TrimSpace(info.Audio)
		if audURL == "" && len(info.Audios) > 0 {
			audURL = strings.TrimSpace(info.Audios[0].URL)
		}
		if audURL != "" {
			candAud := filepath.Join(tmpDir, "aud.bin")
			if ab, aerr := fetchY2Stream("2/3 下载独立音频", audURL, "aud", candAud, 66, 4, totalRawBytes); aerr == nil && ab > 0 {
				audPath = candAud
				totalRawBytes += ab
			}
		}
	}

	outMp4 := filepath.Join(tmpDir, "dual.mp4")
	if err := runFFmpegDualMerge(dlCtx, ffmpegExe, camPath, vgaPath, audPath, outMp4, tracker, totalRawBytes); err != nil {
		if tracker.isCancelled() || dlCtx.Err() != nil {
			tracker.finish("cancelled", "用户已取消")
		} else {
			tracker.finish("error", err.Error())
		}
		dualStageErr("", w, 502, err.Error())
		return
	}
	_ = os.Remove(camPath)
	_ = os.Remove(vgaPath)
	if audPath != "" {
		_ = os.Remove(audPath)
	}

	streamDualMergedResponse(w, r, dlCtx, outMp4, fileName, tracker)
}
