package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestY2ResolvePlaylistRef(t *testing.T) {
	orig := "https://aita.yanhekt.cn/play/2026/100/playlist.m3u8?foo=bar"
	cdnURL, _ := url.Parse("https://oss-cdn.example.net/bucket/xyz/playlist.m3u8?Expires=123&Signature=a/b/c")
	sameHostURL, _ := url.Parse("https://aita.yanhekt.cn/play/2026/100/720p/index.m3u8?t=old")

	// 跨域 302 跳转到对象存储 CDN 时，必须回落到 orig 解析，避免丢失签名或跳到 cvideo
	if got := y2ResolvePlaylistRef("seg001.ts", orig, cdnURL); got != "https://aita.yanhekt.cn/play/2026/100/seg001.ts" {
		t.Fatalf("cross-host relative ref = %q", got)
	}
	if got := y2ResolvePlaylistRef("/play/2026/seg001.ts", orig, cdnURL); got != "https://aita.yanhekt.cn/play/2026/seg001.ts" {
		t.Fatalf("root-relative ref = %q", got)
	}
	// 同域跳转则跟随新路径目录
	if got := y2ResolvePlaylistRef("seg001.ts", orig, sameHostURL); got != "https://aita.yanhekt.cn/play/2026/100/720p/seg001.ts" {
		t.Fatalf("same-host ref = %q", got)
	}
	// 绝对 URL 保持不变
	absU := "https://other.cdn.example.com/seg.ts?auth=1"
	if got := y2ResolvePlaylistRef(absU, orig, cdnURL); got != absU {
		t.Fatalf("absolute ref = %q", got)
	}
}

func TestY2ProxyPlayExternalCDNRedirectNo403(t *testing.T) {
	var cdnSrv *httptest.Server
	var origSrv *httptest.Server

	cdnSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cdnauth") != "ok" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("missing cdnauth"))
			return
		}
		if r.URL.Path == "/cdn/index.m3u8" {
			w.Header().Set("content-type", "application/vnd.apple.mpegurl")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXTINF:10.0,\nseg_0.ts\n#EXT-X-ENDLIST\n"))
			return
		}
		if r.URL.Path == "/cdn/seg_0.ts" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("TS_DATA_OK"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer cdnSrv.Close()

	origSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/play/course1/index.m3u8":
			http.Redirect(w, r, cdnSrv.URL+"/cdn/index.m3u8?cdnauth=ok", http.StatusFound)
		case "/play/course1/seg_0.ts":
			http.Redirect(w, r, cdnSrv.URL+"/cdn/seg_0.ts?cdnauth=ok", http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer origSrv.Close()

	id := &y2Identity{userId: 12345}
	m3u8Orig := origSrv.URL + "/play/course1/index.m3u8"

	req := httptest.NewRequest("GET", "/y2play?u="+enc(m3u8Orig), nil)
	rec := httptest.NewRecorder()
	if err := y2ProxyPlay(rec, req, m3u8Orig, id, y2ProxyOpts{}); err != nil {
		t.Fatalf("y2ProxyPlay m3u8 error: %v", err)
	}
	if rec.Code != 200 {
		t.Fatalf("m3u8 status = %d", rec.Code)
	}
	body := rec.Body.String()
	wantEnc := enc(origSrv.URL + "/play/course1/seg_0.ts")
	if !strings.Contains(body, "/y2play?u="+wantEnc) {
		t.Fatalf("rewritten m3u8 did not stay on orig host; body=\n%s", body)
	}

	// 验证分片真实能穿过代理拿到 200，而不是 403
	segReq := httptest.NewRequest("GET", "/y2play?u="+wantEnc, nil)
	segRec := httptest.NewRecorder()
	if err := y2ProxyPlay(segRec, segReq, origSrv.URL+"/play/course1/seg_0.ts", id, y2ProxyOpts{}); err != nil {
		t.Fatalf("y2ProxyPlay seg error: %v", err)
	}
	if segRec.Code != 200 || segRec.Body.String() != "TS_DATA_OK" {
		t.Fatalf("seg response code=%d body=%q", segRec.Code, segRec.Body.String())
	}
}

func TestDualSinkWriterAndQuietTracker(t *testing.T) {
	dlTrackInit()
	before := len(dlTrackItems)

	var updates int
	qt := dlTrackStartQuiet(func(done, total int, bytes, _ int64) {
		updates++
	})
	qt.setStage("testing")
	qt.update(3, 512)
	qt.addBytes(128)
	qt.markDone()

	if len(dlTrackItems) != before {
		t.Fatalf("quiet tracker polluted global dlTrackItems: before=%d after=%d", before, len(dlTrackItems))
	}
	if updates < 2 {
		t.Fatalf("expected onUpdate callbacks, got %d", updates)
	}
	st, _ := qt.snapshotState()
	if st != "done" {
		t.Fatalf("quiet tracker state = %q", st)
	}

	tmpDir := t.TempDir()
	f, err := os.Create(filepath.Join(tmpDir, "out.bin"))
	if err != nil {
		t.Fatal(err)
	}
	sink := newFileSinkWriter(f)
	sink.WriteHeader(200)
	_, _ = sink.Write([]byte("hello"))
	sink.WriteHeader(500)
	_, _ = sink.Write([]byte("err-text"))
	_ = f.Close()

	got, _ := os.ReadFile(filepath.Join(tmpDir, "out.bin"))
	if string(got) != "hello" || sink.errBuf.String() != "err-text" {
		t.Fatalf("sink wrote %q, errBuf %q", string(got), sink.errBuf.String())
	}
}

func TestFFmpegProbeArgsAndProgress(t *testing.T) {
	p1 := parseFFmpegProbeOutput("  Duration: 01:02:03.50, start: 0.0\n  Stream #0:0: Video: h264\n  Stream #0:1: Audio: aac\n")
	if !p1.hasVideo || !p1.hasAudio || p1.duration < 3723 || p1.duration > 3724 {
		t.Fatalf("unexpected probe parse: %+v", p1)
	}
	p2 := parseFFmpegProbeOutput("  Duration: 00:05:10.00\n  Stream #0:0: Video: h264\n")
	if !p2.hasVideo || p2.hasAudio {
		t.Fatalf("expected video-only parse: %+v", p2)
	}

	args := buildFFmpegDualArgs("cam.ts", "vga.ts", "aud.m4a", "out.mp4", 2)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "hstack=inputs=2:shortest=0[vout]") || !strings.Contains(joined, "[2:a:0]asetpts=PTS-STARTPTS") || !strings.Contains(joined, "-i aud.m4a") {
		t.Fatalf("bad ffmpeg args: %v", args)
	}

	if got := parseFFmpegProgressLine("out_time_us=50000000", 100.0, 70); got != 84 {
		t.Fatalf("progress pct = %d, want 84", got)
	}
	if got := parseFFmpegProgressLine("out_time_us=120000000", 100.0, 70); got != 98 {
		t.Fatalf("clamped progress pct = %d, want 98", got)
	}
}

func TestRunFFmpegDualMergeSynthetic(t *testing.T) {
	ffmpeg := findFFmpeg()
	if ffmpeg == "" {
		t.Skip("ffmpeg not available in environment")
	}
	if err := exec.Command(ffmpeg, "-version").Run(); err != nil && strings.Contains(err.Error(), "0xc0000142") {
		t.Skipf("sandbox restricts user32 DLL init for child processes of test binary: %v", err)
	}
	tmp := t.TempDir()
	cam := filepath.Join(tmp, "cam.ts")
	vga := filepath.Join(tmp, "vga.ts")
	out := filepath.Join(tmp, "dual.mp4")

	c1 := exec.Command(ffmpeg, "-y", "-f", "lavfi", "-i", "testsrc=size=640x360:rate=25:duration=1",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", cam, "-loglevel", "error")
	hideCmdWindow(c1)
	if b, err := c1.CombinedOutput(); err != nil {
		t.Fatalf("make cam failed: %v %s", err, string(b))
	}
	c2 := exec.Command(ffmpeg, "-y", "-f", "lavfi", "-i", "testsrc2=size=800x600:rate=10:duration=1",
		"-c:v", "libx264", "-preset", "ultrafast", vga, "-loglevel", "error")
	hideCmdWindow(c2)
	if b, err := c2.CombinedOutput(); err != nil {
		t.Fatalf("make vga failed: %v %s", err, string(b))
	}

	qt := dlTrackStartQuiet(nil)
	if err := runFFmpegDualMerge(context.Background(), ffmpeg, cam, vga, "", out, qt, 1024); err != nil {
		t.Fatalf("runFFmpegDualMerge failed: %v", err)
	}
	info := probeDualInput(context.Background(), ffmpeg, out)
	if !info.hasVideo || !info.hasAudio {
		t.Fatalf("merged mp4 missing streams: video=%v audio=%v", info.hasVideo, info.hasAudio)
	}
}
