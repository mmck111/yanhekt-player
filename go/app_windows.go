//go:build windows && !nogui

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

const appWindowClass = "yhkt_app_window"

var (
	ole32                        = windows.NewLazySystemDLL("ole32.dll")
	ole32CoInitializeEx          = ole32.NewProc("CoInitializeEx")
	ole32CoUninitialize          = ole32.NewProc("CoUninitialize")
	ole32CoTaskMemFree           = ole32.NewProc("CoTaskMemFree")
	ole32CoCreateInstance        = ole32.NewProc("CoCreateInstance")
	comdlg32                     = windows.NewLazySystemDLL("comdlg32.dll")
	comdlg32GetSaveFileNameW     = comdlg32.NewProc("GetSaveFileNameW")
	shell32                      = windows.NewLazySystemDLL("shell32.dll")
	shell32SHGetKnownFolderPath  = shell32.NewProc("SHGetKnownFolderPath")
	shell32SHCreateItemFromName  = shell32.NewProc("SHCreateItemFromParsingName")
	dwmapi                       = windows.NewLazySystemDLL("dwmapi.dll")
	dwmSetWindowAttribute        = dwmapi.NewProc("DwmSetWindowAttribute")
	shcore                       = windows.NewLazySystemDLL("shcore.dll")
	shcoreSetProcessDpiAwareness = shcore.NewProc("SetProcessDpiAwareness")
	user32                       = windows.NewLazySystemDLL("user32.dll")
	user32CreateWindowExW        = user32.NewProc("CreateWindowExW")
	user32DefWindowProcW         = user32.NewProc("DefWindowProcW")
	user32DestroyWindow          = user32.NewProc("DestroyWindow")
	user32DispatchMessageW       = user32.NewProc("DispatchMessageW")
	user32FindWindowW            = user32.NewProc("FindWindowW")
	user32GetClientRect          = user32.NewProc("GetClientRect")
	user32GetMessageW            = user32.NewProc("GetMessageW")
	user32GetMonitorInfoW        = user32.NewProc("GetMonitorInfoW")
	user32GetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	user32GetWindowLongPtrW      = user32.NewProc("GetWindowLongPtrW")
	user32GetWindowPlacement     = user32.NewProc("GetWindowPlacement")
	user32LoadImageW             = user32.NewProc("LoadImageW")
	user32MessageBoxW            = user32.NewProc("MessageBoxW")
	user32MonitorFromWindow      = user32.NewProc("MonitorFromWindow")
	user32PostMessageW           = user32.NewProc("PostMessageW")
	user32PostQuitMessage        = user32.NewProc("PostQuitMessage")
	user32PostThreadMessageW     = user32.NewProc("PostThreadMessageW")
	user32RegisterClassExW       = user32.NewProc("RegisterClassExW")
	user32SetFocus               = user32.NewProc("SetFocus")
	user32SetForegroundWindow    = user32.NewProc("SetForegroundWindow")
	user32SetDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	user32SetWindowLongPtrW      = user32.NewProc("SetWindowLongPtrW")
	user32SetWindowPlacement     = user32.NewProc("SetWindowPlacement")
	user32SetWindowPos           = user32.NewProc("SetWindowPos")
	user32ShowWindow             = user32.NewProc("ShowWindow")
	user32TranslateMessage       = user32.NewProc("TranslateMessage")
	user32UpdateWindow           = user32.NewProc("UpdateWindow")
	gdi32                        = windows.NewLazySystemDLL("gdi32.dll")
	gdi32CreateSolidBrush        = gdi32.NewProc("CreateSolidBrush")
	kernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	kernel32GetCurrentThreadID   = kernel32.NewProc("GetCurrentThreadId")
	kernel32AttachConsole        = kernel32.NewProc("AttachConsole")
	kernel32AllocConsole         = kernel32.NewProc("AllocConsole")
)

const (
	wmDestroy       = 0x0002
	wmMove          = 0x0003
	wmSize          = 0x0005
	wmActivate      = 0x0006
	wmClose         = 0x0010
	wmQuit          = 0x0012
	wmSetIcon       = 0x0080
	wmNCLButtonDown = 0x00A1
	wmMoving        = 0x0216
	wmApp           = 0x8000

	wsOverlappedWindow = 0x00CF0000
	wsPopup            = 0x80000000
	wsVisible          = 0x10000000

	swShow    = 5
	swRestore = 9

	swpNoZOrder     = 0x0004
	swpFrameChanged = 0x0020

	vkF11 = 0x7A
)

type winPoint struct{ X, Y int32 }
type winRect struct{ Left, Top, Right, Bottom int32 }

type winWndClassExW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     windows.Handle
	HIcon         windows.Handle
	HCursor       windows.Handle
	HbrBackground windows.Handle
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       windows.Handle
}

type winMsg struct {
	Hwnd     windows.HWND
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       winPoint
	LPrivate uint32
}

type winPlacement struct {
	Length           uint32
	Flags            uint32
	ShowCmd          uint32
	PtMinPosition    winPoint
	PtMaxPosition    winPoint
	RcNormalPosition winRect
}

type winMonitorInfo struct {
	CbSize    uint32
	RcMonitor winRect
	RcWork    winRect
	DwFlags   uint32
}

var (
	wvPinsMu sync.Mutex
	wvPins   = map[*wvResObjHandler]struct{}{}
)

func wvPinAdd(h *wvResObjHandler) {
	wvPinsMu.Lock()
	wvPins[h] = struct{}{}
	wvPinsMu.Unlock()
}

func wvPinDelete(h *wvResObjHandler) {
	wvPinsMu.Lock()
	delete(wvPins, h)
	wvPinsMu.Unlock()
}

func desktopSupported() bool { return true }

func isLowIntegrityProcess() bool {
	tok := windows.GetCurrentProcessToken()
	var need uint32
	_ = windows.GetTokenInformation(tok, windows.TokenIntegrityLevel, nil, 0, &need)
	if need == 0 {
		return false
	}
	buf := make([]byte, need)
	if err := windows.GetTokenInformation(tok, windows.TokenIntegrityLevel, &buf[0], need, &need); err != nil {
		return false
	}
	tml := (*windows.Tokenmandatorylabel)(unsafe.Pointer(&buf[0]))
	if tml.Label.Sid == nil {
		return false
	}
	cnt := int(tml.Label.Sid.SubAuthorityCount())
	if cnt <= 0 {
		return false
	}
	return tml.Label.Sid.SubAuthority(uint32(cnt-1)) < 0x2000 // < SECURITY_MANDATORY_MEDIUM_RID
}

func desktopInitHooks() {
	desktopCheckWritableDir = probeWritableDir
	desktopDefaultDownloadDir = func() string { return pickWritableSaveDir("") }
	desktopLowIntegrityFn = isLowIntegrityProcess
}

func attachParentConsoleIfNeeded() {
	if st, err := os.Stdout.Stat(); err == nil && st != nil {
		return
	}
	attached := false
	if err := kernel32AttachConsole.Find(); err == nil {
		r, _, _ := kernel32AttachConsole.Call(^uintptr(0))
		attached = r != 0
	}
	if !attached {
		if err := kernel32AllocConsole.Find(); err == nil {
			r, _, _ := kernel32AllocConsole.Call()
			attached = r != 0
		}
	}
	if !attached {
		return
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		_ = windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, windows.Handle(f.Fd()))
		_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd()))
		os.Stdout = f
		os.Stderr = f
	}
	if f, err := os.OpenFile("CONIN$", os.O_RDONLY, 0); err == nil {
		_ = windows.SetStdHandle(windows.STD_INPUT_HANDLE, windows.Handle(f.Fd()))
		os.Stdin = f
	}
	setUtf8Console()
}

func showDesktopError(msg string) {
	if err := user32MessageBoxW.Find(); err != nil {
		return
	}
	t, _ := windows.UTF16PtrFromString("延河课堂 · 在线播放器")
	m, _ := windows.UTF16PtrFromString(msg)
	_, _, _ = user32MessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x30)
}

func focusDesktopWindowHandle(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	_, _, _ = user32ShowWindow.Call(hwnd, swRestore)
	_, _, _ = user32SetForegroundWindow.Call(hwnd)
}

func focusRunningDesktopApp(p int) bool {
	var hwnd uintptr
	if err := user32FindWindowW.Find(); err == nil {
		cls, _ := windows.UTF16PtrFromString(appWindowClass)
		hwnd, _, _ = user32FindWindowW.Call(uintptr(unsafe.Pointer(cls)), 0)
		if hwnd != 0 {
			focusDesktopWindowHandle(hwnd)
		}
	}
	cl := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := cl.Get(fmt.Sprintf("http://127.0.0.1:%d/_app/focus", p))
	if err == nil {
		_ = resp.Body.Close()
		return resp.StatusCode == 200 || hwnd != 0
	}
	return hwnd != 0
}

// ---------------- Win32 窗口 + WebView2 宿主 ----------------

var (
	winCtxMu sync.RWMutex
	winCtx   = map[uintptr]*desktopApp{}
)

type desktopApp struct {
	hwnd       uintptr
	mainThread uintptr
	host       *wvHost
	localBase  string
	port       int

	dqMu sync.Mutex
	dq   []func()

	fsActive bool
	fsStyle  uintptr
	fsPlace  winPlacement

	authMu        sync.Mutex
	pendingLogin  string // "old" | "y2" | ""
	lastPlayerURL string
	capturedOld   string
	capturedY2    string
	tried         map[string]bool
	completing    bool

	saveMu      sync.Mutex
	lastSaveDir string
}

func (a *desktopApp) dispatch(fn func()) {
	a.dqMu.Lock()
	a.dq = append(a.dq, fn)
	a.dqMu.Unlock()
	_, _, _ = user32PostThreadMessageW.Call(a.mainThread, wmApp, 0, 0)
}

func (a *desktopApp) drainDispatch() {
	a.dqMu.Lock()
	items := a.dq
	a.dq = nil
	a.dqMu.Unlock()
	for _, fn := range items {
		fn()
	}
}

func appWndProc(hwnd, msg, wp, lp uintptr) uintptr {
	winCtxMu.RLock()
	a := winCtx[hwnd]
	winCtxMu.RUnlock()
	if a == nil {
		r, _, _ := user32DefWindowProcW.Call(hwnd, msg, wp, lp)
		return r
	}
	switch msg {
	case wmMove, wmMoving:
		if a.host != nil {
			a.host.notifyMove()
		}
	case wmNCLButtonDown:
		_, _, _ = user32SetFocus.Call(a.hwnd)
		r, _, _ := user32DefWindowProcW.Call(hwnd, msg, wp, lp)
		return r
	case wmSize:
		if a.host != nil {
			a.host.resize()
		}
	case wmActivate:
		if wp != 0 && a.host != nil {
			a.host.focus()
		}
	case wmClose:
		_, _, _ = user32DestroyWindow.Call(hwnd)
	case wmDestroy:
		_, _, _ = user32PostQuitMessage.Call(0)
	default:
		r, _, _ := user32DefWindowProcW.Call(hwnd, msg, wp, lp)
		return r
	}
	return 0
}

func enableDPIAwareness() {
	if err := user32SetDpiAwarenessContext.Find(); err == nil {
		r, _, _ := user32SetDpiAwarenessContext.Call(^uintptr(3)) // PER_MONITOR_AWARE_V2
		if r != 0 {
			return
		}
	}
	if err := shcoreSetProcessDpiAwareness.Find(); err == nil {
		_, _, _ = shcoreSetProcessDpiAwareness.Call(2)
	}
}

func enableDarkTitleBar(hwnd uintptr) {
	if err := dwmSetWindowAttribute.Find(); err != nil {
		return
	}
	val := int32(1)
	hr, _, _ := dwmSetWindowAttribute.Call(hwnd, 20, uintptr(unsafe.Pointer(&val)), 4)
	if hr != 0 {
		_, _, _ = dwmSetWindowAttribute.Call(hwnd, 19, uintptr(unsafe.Pointer(&val)), 4)
	}
}

func probeWritableDir(dir string) bool {
	if dir == "" || os.MkdirAll(dir, 0755) != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".wv-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

func desktopBaseDirCandidates() []string {
	var raw []string
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		raw = append(raw, filepath.Join(v, "yanhekt"))
	}
	if v := os.Getenv("APPDATA"); v != "" {
		raw = append(raw, filepath.Join(v, "yanhekt"))
	}
	if v := os.TempDir(); v != "" {
		raw = append(raw, filepath.Join(v, "yanhekt"))
	}
	if exe, err := os.Executable(); err == nil && exe != "" {
		raw = append(raw, filepath.Join(filepath.Dir(exe), ".yanhekt-cache"))
	}
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, d := range raw {
		c := filepath.Clean(d)
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

var (
	desktopBaseMu   sync.Mutex
	resolvedBaseDir string
)

func desktopAppBaseDir() string {
	desktopBaseMu.Lock()
	defer desktopBaseMu.Unlock()
	if resolvedBaseDir != "" {
		return resolvedBaseDir
	}
	cands := desktopBaseDirCandidates()
	for _, d := range cands {
		if probeWritableDir(d) {
			resolvedBaseDir = d
			return d
		}
	}
	if len(cands) > 0 {
		_ = os.MkdirAll(cands[0], 0755)
		resolvedBaseDir = cands[0]
		return cands[0]
	}
	resolvedBaseDir = filepath.Join(os.TempDir(), "yanhekt")
	return resolvedBaseDir
}

func desktopDataDirs() []string {
	var out []string
	for _, base := range desktopBaseDirCandidates() {
		sub := filepath.Join(base, "webview2")
		if probeWritableDir(sub) {
			out = append(out, sub)
		}
	}
	if len(out) == 0 {
		out = append(out, filepath.Join(desktopAppBaseDir(), "webview2"))
	}
	return out
}

var (
	desktopTraceMu sync.Mutex
	desktopTrace   []string
	desktopFailMsg string
)

func desktopLog(format string, args ...any) {
	line := fmt.Sprintf("%s "+format, append([]any{time.Now().Format("15:04:05.000")}, args...)...)
	desktopTraceMu.Lock()
	desktopTrace = append(desktopTrace, line)
	desktopTraceMu.Unlock()

	p := filepath.Join(desktopAppBaseDir(), "app.log")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintln(f, line)
	_ = f.Close()
}

func desktopFail(reason string) bool {
	desktopTraceMu.Lock()
	desktopFailMsg = reason
	desktopTraceMu.Unlock()
	desktopLog("DESKTOP FAIL: %s", reason)
	return false
}

func desktopFailTrace() string {
	desktopTraceMu.Lock()
	defer desktopTraceMu.Unlock()
	if desktopFailMsg == "" && len(desktopTrace) == 0 {
		return ""
	}
	return strings.Join(desktopTrace, "\n")
}

func ensureWebView2LoaderDLL() {
	if len(webviewloader.WebView2Loader) == 0 {
		return
	}
	dllPath := filepath.Join(desktopAppBaseDir(), "WebView2Loader.dll")
	needWrite := true
	if st, err := os.Stat(dllPath); err == nil && st.Size() == int64(len(webviewloader.WebView2Loader)) {
		needWrite = false
	}
	if needWrite {
		if err := os.WriteFile(dllPath, webviewloader.WebView2Loader, 0644); err != nil {
			desktopLog("write WebView2Loader.dll failed: %v", err)
			return
		}
	}
	if h, err := windows.LoadLibrary(dllPath); err != nil {
		desktopLog("LoadLibrary(%s) failed: %v", dllPath, err)
	} else {
		_ = h
	}
}

func resetDesktopLog() {
	desktopBaseMu.Lock()
	resolvedBaseDir = ""
	desktopBaseMu.Unlock()
	desktopTraceMu.Lock()
	desktopTrace = nil
	desktopFailMsg = ""
	desktopTraceMu.Unlock()
	_ = os.Remove(filepath.Join(desktopAppBaseDir(), "app.log"))
}

func runDesktopApp(p int) bool {
	desktopLog("starting desktop app on port %d", p)
	if err := ole32.Load(); err != nil {
		return desktopFail(fmt.Sprintf("ole32.Load failed: %v", err))
	}
	if err := user32.Load(); err != nil {
		return desktopFail(fmt.Sprintf("user32.Load failed: %v", err))
	}
	if err := gdi32.Load(); err != nil {
		return desktopFail(fmt.Sprintf("gdi32.Load failed: %v", err))
	}
	if err := ole32CoInitializeEx.Find(); err != nil {
		return desktopFail(fmt.Sprintf("CoInitializeEx.Find failed: %v", err))
	}

	runtime.LockOSThread()
	hrCo, _, _ := ole32CoInitializeEx.Call(0, 2) // COINIT_APARTMENTTHREADED
	if int32(hrCo) < 0 && uint32(hrCo) != 0x80010106 {
		return desktopFail(fmt.Sprintf("CoInitializeEx failed: 0x%08x", uint32(hrCo)))
	}
	defer ole32CoUninitialize.Call()

	ensureWebView2LoaderDLL()

	if ver, err := webviewloader.GetInstalledVersion(); err != nil || strings.TrimSpace(ver) == "" {
		desktopFail(fmt.Sprintf("GetInstalledVersion ver=%q err=%v", ver, err))
		u := fmt.Sprintf("http://127.0.0.1:%d/", p)
		_ = copyToClipboard(u)
		showDesktopError("未检测到 Microsoft Edge WebView2 运行环境，无法启动独立窗口。\n\n已将播放地址复制到剪贴板，请在浏览器打开：\n" + u + "\n\n（确认后本地服务仍会继续在后台运行）")
		return false
	} else {
		desktopLog("WebView2 runtime=%s", ver)
	}

	enableDPIAwareness()

	var hinst windows.Handle
	if err := windows.GetModuleHandleEx(windows.GET_MODULE_HANDLE_EX_FLAG_UNCHANGED_REFCOUNT, nil, &hinst); err != nil {
		desktopLog("GetModuleHandleEx failed: %v", err)
	}

	icon, _, _ := user32LoadImageW.Call(uintptr(hinst), 1, 1, 0, 0, 0x0040|0x8000)
	if icon == 0 {
		icon, _, _ = user32LoadImageW.Call(0, 32512, 1, 0, 0, 0x0040|0x8000)
	}
	cursor, _, _ := user32LoadImageW.Call(0, 32512, 2, 0, 0, 0x8000)
	brush, _, _ := gdi32CreateSolidBrush.Call(0x00120d0b)

	clsPtr, _ := windows.UTF16PtrFromString(appWindowClass)
	titlePtr, _ := windows.UTF16PtrFromString("延河课堂 · 在线播放器")
	wc := winWndClassExW{
		CbSize:        uint32(unsafe.Sizeof(winWndClassExW{})),
		LpfnWndProc:   windows.NewCallback(appWndProc),
		HInstance:     hinst,
		HIcon:         windows.Handle(icon),
		HIconSm:       windows.Handle(icon),
		HCursor:       windows.Handle(cursor),
		HbrBackground: windows.Handle(brush),
		LpszClassName: clsPtr,
	}
	atom, _, regErr := user32RegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 && regErr != windows.ERROR_CLASS_ALREADY_EXISTS {
		showDesktopError(fmt.Sprintf("注册桌面窗口类失败：%v", regErr))
		return desktopFail(fmt.Sprintf("RegisterClassExW failed: %v", regErr))
	}

	sw, _, _ := user32GetSystemMetrics.Call(0)
	sh, _, _ := user32GetSystemMetrics.Call(1)
	w := uintptr(1280)
	h := uintptr(820)
	if sw > 0 && sw < 1360 {
		w = sw * 92 / 100
	}
	if sh > 0 && sh < 880 {
		h = sh * 90 / 100
	}
	var x, y uintptr = 0x80000000, 0x80000000
	if sw > w && sh > h {
		x = (sw - w) / 2
		y = (sh - h) / 2
	}

	hwnd, _, createErr := user32CreateWindowExW.Call(
		0, uintptr(unsafe.Pointer(clsPtr)), uintptr(unsafe.Pointer(titlePtr)),
		wsOverlappedWindow, x, y, w, h, 0, 0, uintptr(hinst), 0,
	)
	if hwnd == 0 {
		showDesktopError(fmt.Sprintf("创建桌面窗口失败：%v", createErr))
		return desktopFail(fmt.Sprintf("CreateWindowExW failed: %v", createErr))
	}
	if icon != 0 {
		_, _, _ = user32PostMessageW.Call(hwnd, wmSetIcon, 1, icon)
		_, _, _ = user32PostMessageW.Call(hwnd, wmSetIcon, 0, icon)
	}
	enableDarkTitleBar(hwnd)

	tid, _, _ := kernel32GetCurrentThreadID.Call()
	app := &desktopApp{
		hwnd:       hwnd,
		mainThread: tid,
		localBase:  fmt.Sprintf("http://127.0.0.1:%d", p),
		port:       p,
		tried:      map[string]bool{},
	}

	winCtxMu.Lock()
	winCtx[hwnd] = app
	winCtxMu.Unlock()
	defer func() {
		setDesktopFocusHook(nil)
		winCtxMu.Lock()
		delete(winCtx, hwnd)
		winCtxMu.Unlock()
	}()

	_, _, _ = user32ShowWindow.Call(hwnd, swShow)
	_, _, _ = user32UpdateWindow.Call(hwnd)

	host := newWVHost(hwnd)
	host.onMessage = app.onWebMessage
	host.onNavDone = app.onNavCompleted
	host.onWebReq = app.onWebResource
	host.onFullscreen = app.setFullscreen
	host.onAccelKey = app.onAcceleratorKey
	host.onNewWindow = app.onNewWindow
	app.host = host

	var embedErr error
	var embeddedDir string
	for _, dataDir := range desktopDataDirs() {
		host.reset()
		desktopLog("trying WebView2 dataDir=%s", dataDir)
		if err := host.embed(dataDir); err == nil && host.webview != nil {
			embedErr = nil
			embeddedDir = dataDir
			break
		} else {
			embedErr = err
			desktopLog("host.embed(%s) failed: err=%v webview=%v", dataDir, err, host.webview != nil)
		}
	}
	if embedErr != nil || host.webview == nil {
		desktopFail(fmt.Sprintf("host.embed failed: err=%v webview=%v", embedErr, host.webview != nil))
		_, _, _ = user32DestroyWindow.Call(hwnd)
		u := fmt.Sprintf("http://127.0.0.1:%d/", p)
		_ = copyToClipboard(u)
		msg := "初始化 WebView2 窗口失败"
		if embedErr != nil {
			msg += "：" + embedErr.Error()
		}
		showDesktopError(msg + "\n\n已将地址复制到剪贴板：\n" + u)
		return false
	}
	desktopLog("WebView2 embedded successfully (dataDir=%s), navigating to %s", embeddedDir, app.localBase)

	host.configureVisuals(os.Getenv("YHKT_DEBUG") == "1")
	host.addWebResourceFilter("*://*.yanhekt.cn/*", 0) // COREWEBVIEW2_WEB_RESOURCE_CONTEXT_ALL
	host.initScript(desktopInitScript(app.localBase))
	host.navigate(app.localBase + "/")

	host.resize()
	host.focus()

	setDesktopFocusHook(func() {
		app.dispatch(func() { focusDesktopWindowHandle(app.hwnd) })
	})

	var msg winMsg
	for {
		r, _, _ := user32GetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 || msg.Message == wmQuit {
			break
		}
		if msg.Message == wmApp {
			app.drainDispatch()
			continue
		}
		_, _, _ = user32TranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		_, _, _ = user32DispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
	return true
}

func (a *desktopApp) setFullscreen(on bool) {
	if on == a.fsActive {
		return
	}
	a.fsActive = on
	if on {
		a.fsStyle, _, _ = user32GetWindowLongPtrW.Call(a.hwnd, ^uintptr(15))
		a.fsPlace = winPlacement{Length: uint32(unsafe.Sizeof(winPlacement{}))}
		_, _, _ = user32GetWindowPlacement.Call(a.hwnd, uintptr(unsafe.Pointer(&a.fsPlace)))

		mon, _, _ := user32MonitorFromWindow.Call(a.hwnd, 2)
		mi := winMonitorInfo{CbSize: uint32(unsafe.Sizeof(winMonitorInfo{}))}
		if r, _, _ := user32GetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); r != 0 {
			_, _, _ = user32SetWindowLongPtrW.Call(a.hwnd, ^uintptr(15), (a.fsStyle&^wsOverlappedWindow)|wsPopup|wsVisible)
			_, _, _ = user32SetWindowPos.Call(
				a.hwnd, 0,
				uintptr(mi.RcMonitor.Left), uintptr(mi.RcMonitor.Top),
				uintptr(mi.RcMonitor.Right-mi.RcMonitor.Left),
				uintptr(mi.RcMonitor.Bottom-mi.RcMonitor.Top),
				swpNoZOrder|swpFrameChanged,
			)
		}
	} else {
		_, _, _ = user32SetWindowLongPtrW.Call(a.hwnd, ^uintptr(15), a.fsStyle|wsVisible)
		_, _, _ = user32SetWindowPlacement.Call(a.hwnd, uintptr(unsafe.Pointer(&a.fsPlace)))
		_, _, _ = user32SetWindowPos.Call(a.hwnd, 0, 0, 0, 0, 0, 0x0001|0x0002|swpNoZOrder|swpFrameChanged)
	}
	a.host.resize()
}

func (a *desktopApp) onAcceleratorKey(vk uint32) bool {
	if vk == vkF11 {
		a.host.eval("(()=>{var b=document.getElementById('btnFs')||document.getElementById('fs');if(b)b.click();})()")
		return true
	}
	return false
}

func (a *desktopApp) onNewWindow(uri string) {
	if uri == "about:blank" {
		return
	}
	if strings.HasPrefix(uri, a.localBase+"/dl") || strings.HasPrefix(uri, a.localBase+"/y2dl") {
		a.startDesktopDownload(uri)
		return
	}
	a.host.navigate(uri)
}

// ---------------- 桌面原生另存为与直连下载 ----------------

type winOpenFileNameW struct {
	LStructSize       uint32
	HwndOwner         uintptr
	HInstance         uintptr
	LpstrFilter       *uint16
	LpstrCustomFilter *uint16
	NMaxCustFilter    uint32
	NFilterIndex      uint32
	LpstrFile         *uint16
	NMaxFile          uint32
	LpstrFileTitle    *uint16
	NMaxFileTitle     uint32
	LpstrInitialDir   *uint16
	LpstrTitle        *uint16
	Flags             uint32
	NFileOffset       uint16
	NFileExtension    uint16
	LpstrDefExt       *uint16
	LCustData         uintptr
	LpfnHook          uintptr
	LpTemplateName    *uint16
	PvReserved        uintptr
	DwReserved        uint32
	FlagsEx           uint32
}

var (
	folderIDVideos    = windows.GUID{Data1: 0x18989B1D, Data2: 0x99B5, Data3: 0x455B, Data4: [8]byte{0x84, 0x1C, 0xAB, 0x7C, 0x74, 0xE4, 0xDD, 0xFC}}
	folderIDDownloads = windows.GUID{Data1: 0x374DE290, Data2: 0x123F, Data3: 0x4565, Data4: [8]byte{0x91, 0x64, 0x39, 0xC4, 0x92, 0x5E, 0x46, 0x7B}}
)

func knownFolderPath(id windows.GUID) string {
	if err := shell32SHGetKnownFolderPath.Find(); err != nil {
		return ""
	}
	var p *uint16
	hr, _, _ := shell32SHGetKnownFolderPath.Call(uintptr(unsafe.Pointer(&id)), 0, 0, uintptr(unsafe.Pointer(&p)))
	if p != nil {
		defer ole32CoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
	}
	if hr != 0 || p == nil {
		return ""
	}
	return windows.UTF16PtrToString(p)
}

func pickWritableSaveDir(lastDir string) string {
	var cands []string
	if lastDir != "" {
		cands = append(cands, lastDir)
	}
	// If %LOCALAPPDATA% itself is blocked by system policy/AV, standard user libraries often reject Save As too;
	// prefer a verified writable folder next to the exe first in that case.
	localRestricted := false
	if v := os.Getenv("LOCALAPPDATA"); v != "" && !probeWritableDir(filepath.Join(v, "yanhekt")) {
		localRestricted = true
	}
	if localRestricted {
		if exe, err := os.Executable(); err == nil && exe != "" {
			cands = append(cands, filepath.Join(filepath.Dir(exe), "downloads"))
		}
	}
	if !localRestricted {
		if d := knownFolderPath(folderIDDownloads); d != "" {
			cands = append(cands, d)
		}
	}
	if v := knownFolderPath(folderIDVideos); v != "" {
		cands = append(cands, v)
	}
	if up := os.Getenv("USERPROFILE"); up != "" {
		cands = append(cands, filepath.Join(up, "Videos"), filepath.Join(up, "Desktop"))
	}
	if exe, err := os.Executable(); err == nil && exe != "" {
		cands = append(cands, filepath.Join(filepath.Dir(exe), "downloads"))
	}
	cands = append(cands, filepath.Join(desktopAppBaseDir(), "downloads"))
	for _, d := range cands {
		if probeWritableDir(d) {
			return d
		}
	}
	return os.TempDir()
}

func sanitizeWinFileName(name string) string {
	name = strings.TrimSpace(name)
	repl := strings.NewReplacer("<", "_", ">", "_", ":", "_", `"`, "_", "/", "_", `\`, "_", "|", "_", "?", "_", "*", "_")
	name = repl.Replace(name)
	name = strings.Trim(name, ". ")
	if name == "" {
		return "延河课堂视频.mp4"
	}
	return name
}

func parseDownloadFilename(cdHeader, rawURL string) string {
	if cdHeader != "" {
		low := strings.ToLower(cdHeader)
		if idx := strings.Index(low, "filename*=utf-8''"); idx >= 0 {
			rest := cdHeader[idx+len("filename*=utf-8''"):]
			if semi := strings.IndexByte(rest, ';'); semi >= 0 {
				rest = rest[:semi]
			}
			rest = strings.Trim(rest, `" `)
			if dec, err := url.QueryUnescape(rest); err == nil && strings.TrimSpace(dec) != "" {
				return sanitizeWinFileName(dec)
			}
		}
		if idx := strings.Index(low, "filename="); idx >= 0 {
			rest := cdHeader[idx+len("filename="):]
			if semi := strings.IndexByte(rest, ';'); semi >= 0 {
				rest = rest[:semi]
			}
			rest = strings.Trim(rest, `" `)
			if rest != "" {
				return sanitizeWinFileName(rest)
			}
		}
	}
	if u, err := url.Parse(rawURL); err == nil {
		if t := strings.TrimSpace(u.Query().Get("title")); t != "" {
			if filepath.Ext(t) == "" {
				t += ".mp4"
			}
			return sanitizeWinFileName(t)
		}
	}
	return "延河课堂视频.mp4"
}

type winFilterSpec struct {
	Name *uint16
	Spec *uint16
}

type winIUnknownVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
}

type winFileDialogVtbl struct {
	winIUnknownVtbl
	Show                uintptr
	SetFileTypes        uintptr
	SetFileTypeIndex    uintptr
	GetFileTypeIndex    uintptr
	Advise              uintptr
	Unadvise            uintptr
	SetOptions          uintptr
	GetOptions          uintptr
	SetDefaultFolder    uintptr
	SetFolder           uintptr
	GetFolder           uintptr
	GetCurrentSelection uintptr
	SetFileName         uintptr
	GetFileName         uintptr
	SetTitle            uintptr
	SetOkButtonLabel    uintptr
	SetFileNameLabel    uintptr
	GetResult           uintptr
	AddPlace            uintptr
	SetDefaultExtension uintptr
}

type winFileDialog struct{ vtbl *winFileDialogVtbl }

type winShellItemVtbl struct {
	winIUnknownVtbl
	BindToHandler  uintptr
	GetParent      uintptr
	GetDisplayName uintptr
	GetAttributes  uintptr
	Compare        uintptr
}

type winShellItem struct{ vtbl *winShellItemVtbl }

var (
	clsidFileSaveDialog = windows.GUID{Data1: 0xC0B4E2F3, Data2: 0xBA21, Data3: 0x4773, Data4: [8]byte{0x8D, 0xBA, 0x33, 0x5E, 0xC9, 0x46, 0xEB, 0x8B}}
	iidFileSaveDialog   = windows.GUID{Data1: 0x84BCCD23, Data2: 0x5FDE, Data3: 0x4CDB, Data4: [8]byte{0xAE, 0xA4, 0xAF, 0x64, 0xB8, 0x3D, 0x78, 0xAB}}
	clsidFileOpenDialog = windows.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE, Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidFileOpenDialog   = windows.GUID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768, Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
	iidShellItem        = windows.GUID{Data1: 0x43826D1E, Data2: 0xE718, Data3: 0x42EE, Data4: [8]byte{0xBC, 0x55, 0xA1, 0xE2, 0x61, 0xC3, 0x7B, 0xFE}}
)

func pickFolderDialog(hwnd uintptr, initialDir string) (string, bool) {
	if err := ole32CoCreateInstance.Find(); err != nil {
		return "", false
	}
	var dlg *winFileDialog
	hr, _, _ := ole32CoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidFileOpenDialog)),
		0,
		1, // CLSCTX_INPROC_SERVER
		uintptr(unsafe.Pointer(&iidFileOpenDialog)),
		uintptr(unsafe.Pointer(&dlg)),
	)
	if hr != 0 || dlg == nil {
		desktopLog("CoCreateInstance(FileOpenDialog) failed: 0x%08x", uint32(hr))
		return "", false
	}
	dlgPtr := uintptr(unsafe.Pointer(dlg))
	defer syscall.SyscallN(dlg.vtbl.Release, dlgPtr)

	var opts uint32
	_, _, _ = syscall.SyscallN(dlg.vtbl.GetOptions, dlgPtr, uintptr(unsafe.Pointer(&opts)))
	opts |= 0x00000020 | 0x00000008 | 0x00000040 | 0x00000800 // FOS_PICKFOLDERS | NOCHANGEDIR | FORCEFILESYSTEM | PATHMUSTEXIST
	_, _, _ = syscall.SyscallN(dlg.vtbl.SetOptions, dlgPtr, uintptr(opts))

	titlePtr, _ := windows.UTF16PtrFromString("选择默认下载文件夹")
	_, _, _ = syscall.SyscallN(dlg.vtbl.SetTitle, dlgPtr, uintptr(unsafe.Pointer(titlePtr)))

	if initialDir != "" && shell32SHCreateItemFromName.Find() == nil {
		if dirPtr, err := windows.UTF16PtrFromString(initialDir); err == nil {
			var folderItem *winShellItem
			hrItem, _, _ := shell32SHCreateItemFromName.Call(
				uintptr(unsafe.Pointer(dirPtr)),
				0,
				uintptr(unsafe.Pointer(&iidShellItem)),
				uintptr(unsafe.Pointer(&folderItem)),
			)
			if hrItem == 0 && folderItem != nil {
				_, _, _ = syscall.SyscallN(dlg.vtbl.SetFolder, dlgPtr, uintptr(unsafe.Pointer(folderItem)))
				_, _, _ = syscall.SyscallN(folderItem.vtbl.Release, uintptr(unsafe.Pointer(folderItem)))
			}
		}
	}

	hrShow, _, _ := syscall.SyscallN(dlg.vtbl.Show, dlgPtr, hwnd)
	if hrShow != 0 {
		return "", false
	}
	var resItem *winShellItem
	hrRes, _, _ := syscall.SyscallN(dlg.vtbl.GetResult, dlgPtr, uintptr(unsafe.Pointer(&resItem)))
	if hrRes != 0 || resItem == nil {
		return "", false
	}
	resPtr := uintptr(unsafe.Pointer(resItem))
	defer syscall.SyscallN(resItem.vtbl.Release, resPtr)

	var pszPath *uint16
	hrPath, _, _ := syscall.SyscallN(resItem.vtbl.GetDisplayName, resPtr, uintptr(0x80058000), uintptr(unsafe.Pointer(&pszPath)))
	if hrPath != 0 || pszPath == nil {
		return "", false
	}
	defer ole32CoTaskMemFree.Call(uintptr(unsafe.Pointer(pszPath)))
	return windows.UTF16PtrToString(pszPath), true
}

func uniqueDestPath(dir, fname string) string {
	base := strings.TrimSuffix(fname, filepath.Ext(fname))
	ext := filepath.Ext(fname)
	p := filepath.Join(dir, fname)
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return p
	}
	for i := 1; i < 1000; i++ {
		cand := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s_%d%s", base, time.Now().Unix(), ext))
}

func openFolderInExplorer(dir string) {
	if dir == "" {
		dir = effectiveDownloadDirOf(loadAppSettings())
	}
	if dir == "" {
		return
	}
	_ = os.MkdirAll(dir, 0755)
	_ = exec.Command("explorer.exe", dir).Start()
}

func pickSaveFileDialogVista(hwnd uintptr, initialDir, defaultName, defExt string) (path string, ok bool, handled bool) {
	if err := ole32CoCreateInstance.Find(); err != nil {
		return "", false, false
	}
	var dlg *winFileDialog
	hr, _, _ := ole32CoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidFileSaveDialog)),
		0,
		1, // CLSCTX_INPROC_SERVER
		uintptr(unsafe.Pointer(&iidFileSaveDialog)),
		uintptr(unsafe.Pointer(&dlg)),
	)
	if hr != 0 || dlg == nil {
		desktopLog("CoCreateInstance(FileSaveDialog) failed: 0x%08x", uint32(hr))
		return "", false, false
	}
	dlgPtr := uintptr(unsafe.Pointer(dlg))
	defer syscall.SyscallN(dlg.vtbl.Release, dlgPtr)

	var opts uint32
	_, _, _ = syscall.SyscallN(dlg.vtbl.GetOptions, dlgPtr, uintptr(unsafe.Pointer(&opts)))
	opts |= 0x00000002 | 0x00000008 | 0x00000040 | 0x00000800 // OVERWRITEPROMPT | NOCHANGEDIR | FORCEFILESYSTEM | PATHMUSTEXIST
	_, _, _ = syscall.SyscallN(dlg.vtbl.SetOptions, dlgPtr, uintptr(opts))

	n1, _ := windows.UTF16PtrFromString("视频文件 (*.mp4;*.ts)")
	s1, _ := windows.UTF16PtrFromString("*.mp4;*.ts")
	n2, _ := windows.UTF16PtrFromString("所有文件 (*.*)")
	s2, _ := windows.UTF16PtrFromString("*.*")
	specs := [2]winFilterSpec{{Name: n1, Spec: s1}, {Name: n2, Spec: s2}}
	_, _, _ = syscall.SyscallN(dlg.vtbl.SetFileTypes, dlgPtr, 2, uintptr(unsafe.Pointer(&specs[0])))
	_, _, _ = syscall.SyscallN(dlg.vtbl.SetFileTypeIndex, dlgPtr, 1)

	titlePtr, _ := windows.UTF16PtrFromString("保存课程视频")
	_, _, _ = syscall.SyscallN(dlg.vtbl.SetTitle, dlgPtr, uintptr(unsafe.Pointer(titlePtr)))

	namePtr, _ := windows.UTF16PtrFromString(defaultName)
	_, _, _ = syscall.SyscallN(dlg.vtbl.SetFileName, dlgPtr, uintptr(unsafe.Pointer(namePtr)))
	if defExt != "" {
		extPtr, _ := windows.UTF16PtrFromString(defExt)
		_, _, _ = syscall.SyscallN(dlg.vtbl.SetDefaultExtension, dlgPtr, uintptr(unsafe.Pointer(extPtr)))
	}

	if initialDir != "" && shell32SHCreateItemFromName.Find() == nil {
		if dirPtr, err := windows.UTF16PtrFromString(initialDir); err == nil {
			var folderItem *winShellItem
			hrItem, _, _ := shell32SHCreateItemFromName.Call(
				uintptr(unsafe.Pointer(dirPtr)),
				0,
				uintptr(unsafe.Pointer(&iidShellItem)),
				uintptr(unsafe.Pointer(&folderItem)),
			)
			if hrItem == 0 && folderItem != nil {
				_, _, _ = syscall.SyscallN(dlg.vtbl.SetDefaultFolder, dlgPtr, uintptr(unsafe.Pointer(folderItem)))
				// SetFolder overrides ComDlg32's remembered MRU directory (e.g. D:\Downloads).
				_, _, _ = syscall.SyscallN(dlg.vtbl.SetFolder, dlgPtr, uintptr(unsafe.Pointer(folderItem)))
				_, _, _ = syscall.SyscallN(folderItem.vtbl.Release, uintptr(unsafe.Pointer(folderItem)))
			}
		}
	}

	hrShow, _, _ := syscall.SyscallN(dlg.vtbl.Show, dlgPtr, hwnd)
	if hrShow != 0 {
		return "", false, true
	}

	var resItem *winShellItem
	hrRes, _, _ := syscall.SyscallN(dlg.vtbl.GetResult, dlgPtr, uintptr(unsafe.Pointer(&resItem)))
	if hrRes != 0 || resItem == nil {
		return "", false, true
	}
	resPtr := uintptr(unsafe.Pointer(resItem))
	defer syscall.SyscallN(resItem.vtbl.Release, resPtr)

	var pszPath *uint16
	hrPath, _, _ := syscall.SyscallN(resItem.vtbl.GetDisplayName, resPtr, uintptr(0x80058000), uintptr(unsafe.Pointer(&pszPath))) // SIGDN_FILESYSPATH
	if hrPath != 0 || pszPath == nil {
		return "", false, true
	}
	defer ole32CoTaskMemFree.Call(uintptr(unsafe.Pointer(pszPath)))
	return windows.UTF16PtrToString(pszPath), true, true
}

func pickSaveFileDialog(hwnd uintptr, initialDir, defaultName, defExt string) (string, bool) {
	if p, ok, handled := pickSaveFileDialogVista(hwnd, initialDir, defaultName, defExt); handled {
		return p, ok
	}
	if err := comdlg32GetSaveFileNameW.Find(); err != nil {
		return filepath.Join(initialDir, defaultName), true
	}
	buf := make([]uint16, 4096)
	// Windows ignores lpstrInitialDir whenever ComDlg32 MRU remembers an older folder (e.g. D:\Downloads);
	// seeding lpstrFile with the full initial path forces the dialog to start in initialDir.
	seedPath := defaultName
	if initialDir != "" {
		seedPath = filepath.Join(initialDir, defaultName)
	}
	initName := utf16.Encode([]rune(seedPath))
	if len(initName) >= len(buf) {
		initName = initName[:len(buf)-1]
	}
	copy(buf, initName)

	filter := utf16.Encode([]rune("视频文件 (*.mp4;*.ts)\x00*.mp4;*.ts\x00所有文件 (*.*)\x00*.*\x00\x00"))
	titlePtr, _ := windows.UTF16PtrFromString("保存课程视频")
	var dirPtr *uint16
	if initialDir != "" {
		dirPtr, _ = windows.UTF16PtrFromString(initialDir)
	}
	var extPtr *uint16
	if defExt != "" {
		extPtr, _ = windows.UTF16PtrFromString(defExt)
	}
	ofn := winOpenFileNameW{
		LStructSize:     uint32(unsafe.Sizeof(winOpenFileNameW{})),
		HwndOwner:       hwnd,
		LpstrFilter:     &filter[0],
		NFilterIndex:    1,
		LpstrFile:       &buf[0],
		NMaxFile:        uint32(len(buf)),
		LpstrInitialDir: dirPtr,
		LpstrTitle:      titlePtr,
		Flags:           0x00000002 | 0x00000800 | 0x00000008, // OVERWRITEPROMPT | PATHMUSTEXIST | NOCHANGEDIR
		LpstrDefExt:     extPtr,
	}
	r, _, _ := comdlg32GetSaveFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
	if r == 0 {
		return "", false
	}
	return windows.UTF16ToString(buf), true
}

func (a *desktopApp) uiToast(msg string) {
	a.dispatch(func() {
		a.host.eval("if(typeof toast==='function')toast(" + jsStr(msg) + ",4500)")
	})
}

func loadSavedDownloadDir() string {
	b, err := os.ReadFile(filepath.Join(desktopAppBaseDir(), "save_dir.txt"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func persistSavedDownloadDir(dir string) {
	if strings.TrimSpace(dir) == "" {
		return
	}
	_ = os.WriteFile(filepath.Join(desktopAppBaseDir(), "save_dir.txt"), []byte(strings.TrimSpace(dir)), 0644)
}

func (a *desktopApp) startDesktopDownload(rawURL string) {
	fullURL := strings.TrimSpace(rawURL)
	if strings.HasPrefix(fullURL, "/") {
		fullURL = a.localBase + fullURL
	}
	if !strings.HasPrefix(fullURL, a.localBase+"/dl") && !strings.HasPrefix(fullURL, a.localBase+"/y2dl") {
		return
	}
	runWithCookie := func(cookieHdr string) {
		go func() {
			req, err := http.NewRequest("GET", fullURL, nil)
			if err != nil {
				a.uiToast("下载请求失败：" + err.Error())
				return
			}
			if cookieHdr != "" {
				req.Header.Set("Cookie", cookieHdr)
			}
			resp, err := (&http.Client{}).Do(req)
			if err != nil {
				desktopLog("desktop download HTTP failed: %v", err)
				a.uiToast("下载失败：" + err.Error())
				return
			}
			if resp.StatusCode != 200 {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
				_ = resp.Body.Close()
				msg := strings.TrimSpace(string(b))
				if msg == "" {
					msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
				}
				a.uiToast("下载失败：" + msg)
				return
			}

			dlID := resp.Header.Get("X-YHKT-DL-ID")
			fname := parseDownloadFilename(resp.Header.Get("Content-Disposition"), fullURL)
			ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(fname)), ".")
			if ext == "" {
				ext = "mp4"
			}

			curSet := loadAppSettings()
			if curSet.DownloadDir == "" {
				a.saveMu.Lock()
				if a.lastSaveDir == "" {
					a.lastSaveDir = loadSavedDownloadDir()
				}
				curSet.DownloadDir = a.lastSaveDir
				a.saveMu.Unlock()
			}

			var outFile *os.File
			var savePath string

			if !curSet.AskSavePath {
				defDir := effectiveDownloadDirOf(curSet)
				if defDir != "" && probeWritableDir(defDir) {
					cand := uniqueDestPath(defDir, fname)
					if f, err := os.Create(cand); err == nil {
						outFile = f
						savePath = cand
						desktopLog("download direct path=%s", savePath)
					} else {
						desktopLog("download direct os.Create(%s) failed: %v", cand, err)
					}
				}
			}

			if outFile == nil {
				type saveChoice struct {
					path string
					ok   bool
				}
				pickOnce := func(fallbackInit bool) saveChoice {
					ch := make(chan saveChoice, 1)
					a.dispatch(func() {
						a.saveMu.Lock()
						if a.lastSaveDir == "" {
							a.lastSaveDir = curSet.DownloadDir
							if a.lastSaveDir == "" {
								a.lastSaveDir = loadSavedDownloadDir()
							}
						}
						lastDir := a.lastSaveDir
						a.saveMu.Unlock()
						if fallbackInit {
							lastDir = ""
						}
						initDir := pickWritableSaveDir(lastDir)
						desktopLog("download dialog fname=%s initDir=%s", fname, initDir)
						p, ok := pickSaveFileDialog(a.hwnd, initDir, fname, ext)
						desktopLog("download dialog result ok=%v path=%s", ok, p)
						ch <- saveChoice{p, ok}
					})
					return <-ch
				}

				for attempt := 0; attempt < 3; attempt++ {
					choice := pickOnce(attempt > 0)
					if !choice.ok || choice.path == "" {
						dlTrackCancel(dlID)
						_ = resp.Body.Close()
						a.uiToast("已取消下载")
						return
					}
					f, err := os.Create(choice.path)
					if err == nil {
						outFile = f
						savePath = choice.path
						dir := filepath.Dir(choice.path)
						a.saveMu.Lock()
						a.lastSaveDir = dir
						a.saveMu.Unlock()
						persistSavedDownloadDir(dir)
						break
					}
					desktopLog("download os.Create(%s) failed: %v", choice.path, err)
					a.dispatch(func() {
						showDesktopError("无法保存到该位置：\n" + choice.path + "\n\n" + err.Error() + "\n\n请重新选择一个有写入权限的文件夹。")
					})
				}
			}
			if outFile == nil {
				dlTrackCancel(dlID)
				_ = resp.Body.Close()
				return
			}

			a.uiToast("正在保存到 " + savePath + "（可在「下载管理」查看进度或取消）")
			written, copyErr := io.Copy(outFile, resp.Body)
			closeErr := outFile.Close()
			_ = resp.Body.Close()
			if dlTrackIsCancelled(dlID) {
				_ = os.Remove(savePath)
				desktopLog("download cancelled path=%s partialBytes=%d", savePath, written)
				a.uiToast("已取消下载：" + filepath.Base(savePath))
				return
			}
			if copyErr != nil || closeErr != nil {
				e := copyErr
				if e == nil {
					e = closeErr
				}
				desktopLog("download write failed path=%s bytes=%d err=%v", savePath, written, e)
				a.uiToast("下载中断：" + e.Error())
				return
			}
			desktopLog("download saved path=%s bytes=%d", savePath, written)
			a.uiToast("✓ 已保存：" + filepath.Base(savePath))
		}()
	}

	cm := a.host.getCookieManager()
	if cm == nil {
		runWithCookie("")
		return
	}
	ok := cm.getCookiesAsync(a.localBase, func(cookies [][2]string) {
		var parts []string
		for _, kv := range cookies {
			parts = append(parts, kv[0]+"="+kv[1])
		}
		runWithCookie(strings.Join(parts, "; "))
	})
	if !ok {
		runWithCookie("")
	}
}

// ---------------- 登录截取与跳回流程 ----------------

type webMsgPayload struct {
	Type   string `json:"type"`
	Which  string `json:"which"`
	Host   string `json:"host"`
	Token  string `json:"token"`
	Cookie string `json:"cookie"`
	URL    string `json:"url"`
	Dir    string `json:"dir"`
}

func (a *desktopApp) onWebMessage(raw, source string) {
	var msg webMsgPayload
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		return
	}
	switch msg.Type {
	case "login":
		which := "y2"
		if msg.Which == "old" {
			which = "old"
		}
		a.startLogin(which, source)
	case "clearAuth":
		a.clearWebCookies(msg.Which)
	case "download":
		a.startDesktopDownload(msg.URL)
	case "pickDownloadDir":
		// Defer modal IFileOpenDialog out of WebMessageReceived so WebView2 never deadlocks on COM re-entrancy.
		a.dispatch(func() {
			cur := effectiveDownloadDirOf(loadAppSettings())
			dir, ok := pickFolderDialog(a.hwnd, cur)
			if !ok || dir == "" {
				return
			}
			if !probeWritableDir(dir) {
				res, _ := json.Marshal(map[string]any{
					"ok":    false,
					"dir":   dir,
					"error": "该目录没有写入权限（如在工作区低完整性目录运行，或选择了受保护目录），请安装/复制到普通文件夹或换选可写目录",
				})
				a.host.eval("window.__yhktOnDownloadDirPicked&&window.__yhktOnDownloadDirPicked(" + string(res) + ")")
				return
			}
			s := loadAppSettings()
			s.DownloadDir = dir
			_ = saveAppSettings(s)
			a.saveMu.Lock()
			a.lastSaveDir = dir
			a.saveMu.Unlock()
			persistSavedDownloadDir(dir)
			res, _ := json.Marshal(map[string]any{"ok": true, "dir": dir})
			a.host.eval("window.__yhktOnDownloadDirPicked&&window.__yhktOnDownloadDirPicked(" + string(res) + ")")
		})
	case "openDir":
		openFolderInExplorer(msg.Dir)
	case "captured":
		a.handleCaptured(msg.Host, msg.Token, msg.Cookie, false)
	case "forceBack":
		a.forceFinishOrReturn()
	case "cancelLogin":
		a.cancelLogin()
	}
}

func (a *desktopApp) startLogin(which, curURL string) {
	a.authMu.Lock()
	if strings.HasPrefix(curURL, a.localBase) {
		a.lastPlayerURL = curURL
	}
	a.pendingLogin = which
	a.capturedOld = ""
	a.capturedY2 = ""
	a.tried = map[string]bool{}
	a.completing = false
	a.authMu.Unlock()

	target := "https://aita.yanhekt.cn/portal/"
	if which == "old" {
		target = "https://www.yanhekt.cn/"
	}
	desktopLog("startLogin which=%s target=%s", which, target)
	a.host.navigate(target)
}

func (a *desktopApp) returnTargetFor(which string) string {
	a.authMu.Lock()
	saved := a.lastPlayerURL
	a.authMu.Unlock()
	if strings.HasPrefix(saved, a.localBase) {
		rel := strings.TrimPrefix(saved, a.localBase)
		savedY2 := strings.HasPrefix(rel, "/y2")
		wantY2 := which == "y2"
		if savedY2 == wantY2 {
			return rel
		}
	}
	if which == "y2" {
		return "/y2"
	}
	return "/"
}

func (a *desktopApp) cancelLogin() {
	a.authMu.Lock()
	which := a.pendingLogin
	saved := a.lastPlayerURL
	a.pendingLogin = ""
	a.completing = false
	a.authMu.Unlock()
	target := "/"
	if strings.HasPrefix(saved, a.localBase) {
		target = strings.TrimPrefix(saved, a.localBase)
	} else if which == "y2" {
		target = "/y2"
	}
	a.host.navigate(a.localBase + normalizeAppReturnPath(target, "/"))
}

func (a *desktopApp) clearWebCookies(which string) {
	clearOld := which == "" || which == "old" || which == "all"
	clearY2 := which == "" || which == "y2" || which == "all"
	cm := a.host.getCookieManager()
	if cm == nil {
		return
	}
	defer cm.release()
	if clearOld {
		cm.deleteCookies(cookieName, a.localBase)
		cm.deleteCookies("yht", "https://www.yanhekt.cn")
		cm.deleteCookies("token", "https://www.yanhekt.cn")
	}
	if clearY2 {
		cm.deleteCookies(y2Cookie, a.localBase)
		cm.deleteCookies("_token", "https://aita.yanhekt.cn")
	}
}

func (a *desktopApp) onWebResource(uri string, hdrs map[string]string) {
	a.authMu.Lock()
	pending := a.pendingLogin
	a.authMu.Unlock()
	if pending == "" || !strings.Contains(uri, "yanhekt.cn") {
		return
	}
	var tokens []string
	ck := ""
	for k, v := range hdrs {
		if len(v) < 20 {
			continue
		}
		lk := strings.ToLower(k)
		if lk == "cookie" {
			ck = v
		} else if strings.Contains(lk, "authorization") || strings.Contains(lk, "blade") || strings.Contains(lk, "token") {
			t := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(v, "Bearer "), "bearer "))
			if len(t) >= 20 {
				tokens = append(tokens, t)
			}
		}
	}
	if len(tokens) > 0 || ck != "" {
		host := "www.yanhekt.cn"
		if strings.Contains(uri, "aita.yanhekt.cn") {
			host = "aita.yanhekt.cn"
		}
		a.handleCaptured(host, strings.Join(tokens, "\n"), ck, false)
	}
}

func (a *desktopApp) onNavCompleted(cur string) {
	if cur == "" {
		return
	}
	a.authMu.Lock()
	pending := a.pendingLogin
	a.authMu.Unlock()

	if strings.HasPrefix(cur, a.localBase) {
		if pending != "" && !strings.Contains(cur, "/_app/auth") {
			a.authMu.Lock()
			a.pendingLogin = ""
			a.completing = false
			a.authMu.Unlock()
		}
		return
	}
	if pending == "" {
		return
	}
	desktopLog("onNavCompleted pending=%s url=%s", pending, cur)
	a.host.eval(showBannerEvalJS(pending))
	if strings.Contains(cur, "yanhekt.cn") {
		a.pollWebViewCookies(cur, "", "", false)
		go func(u string) {
			for _, d := range []time.Duration{900 * time.Millisecond, 2200 * time.Millisecond} {
				time.Sleep(d)
				a.authMu.Lock()
				still := a.pendingLogin != "" && !a.completing
				a.authMu.Unlock()
				if !still {
					return
				}
				a.dispatch(func() { a.pollWebViewCookies(u, "", "", false) })
			}
		}(cur)
	}
}

func (a *desktopApp) forceFinishOrReturn() {
	a.authMu.Lock()
	pending := a.pendingLogin
	candOld := a.capturedOld
	candY2 := a.capturedY2
	a.authMu.Unlock()
	if pending == "" {
		a.cancelLogin()
		return
	}
	targetSite := "https://aita.yanhekt.cn"
	if pending == "old" {
		targetSite = "https://www.yanhekt.cn"
	}
	a.pollWebViewCookies(targetSite, candOld, candY2, true)
}

func (a *desktopApp) pollWebViewCookies(uri, extraOld, extraY2 string, fallbackReturn bool) {
	cm := a.host.getCookieManager()
	if cm == nil {
		desktopLog("pollWebViewCookies: CookieManager nil url=%s", uri)
		if fallbackReturn {
			a.handleCaptured(uri, extraOld+"\n"+extraY2, "", true)
		}
		return
	}
	ok := cm.getCookiesAsync(uri, func(cookies [][2]string) {
		desktopLog("getCookiesAsync ok url=%s count=%d", uri, len(cookies))
		var pairs, vals []string
		for _, kv := range cookies {
			n, v := kv[0], kv[1]
			pairs = append(pairs, n+"="+v)
			if n == "_token" || n == "yht" || n == "token" || strings.Contains(n, "token") {
				vals = append(vals, v)
			}
		}
		extra := extraY2
		if !strings.Contains(uri, "aita") {
			extra = extraOld
		}
		a.handleCaptured(uri, strings.Join(vals, "\n")+"\n"+extra, strings.Join(pairs, "; "), fallbackReturn)
	})
	if !ok && fallbackReturn {
		a.handleCaptured(uri, extraOld+"\n"+extraY2, "", true)
	}
}

func (a *desktopApp) handleCaptured(host, tokenBlob, cookie string, fallbackReturn bool) {
	a.authMu.Lock()
	pending := a.pendingLogin
	if strings.TrimSpace(tokenBlob) != "" {
		if strings.Contains(host, "aita") {
			a.capturedY2 = mergeLines(a.capturedY2, tokenBlob)
		} else {
			a.capturedOld = mergeLines(a.capturedOld, tokenBlob)
		}
	}
	if pending == "" || a.completing {
		a.authMu.Unlock()
		return
	}
	tryKey := pending + "|" + tokenBlob + "|" + cookie
	if !fallbackReturn && a.tried[tryKey] {
		a.authMu.Unlock()
		return
	}
	if strings.TrimSpace(tokenBlob) == "" && strings.TrimSpace(cookie) == "" {
		a.authMu.Unlock()
		if fallbackReturn {
			a.cancelLogin()
		}
		return
	}
	a.tried[tryKey] = true
	a.authMu.Unlock()

	go func(which, blob, ck string, retIfFail bool) {
		verified, ok := verifyAppCandidate(which, blob, ck)
		if !ok && which == "old" {
			if jwt, ok2 := verifyAppCandidate("y2", blob, ck); ok2 {
				which = "y2"
				verified = jwt
				ok = true
			}
		}
		if !ok {
			if retIfFail {
				a.dispatch(func() {
					a.host.eval("window.__yhktShowLoginBar&&window.__yhktShowLoginBar(" + jsStr(which) + ",'暂未检测到有效登录态，请先完成登录后再点返回',false)")
				})
			}
			return
		}
		a.authMu.Lock()
		if a.pendingLogin == "" || a.completing {
			a.authMu.Unlock()
			return
		}
		a.completing = true
		a.pendingLogin = ""
		a.authMu.Unlock()

		retPath := a.returnTargetFor(which)
		desktopLog("login verified which=%s return=%s", which, retPath)
		code := issueAppAuthTicket(which, verified, retPath)
		a.dispatch(func() {
			a.host.eval("window.__yhktShowLoginBar&&window.__yhktShowLoginBar(" + jsStr(which) + ",'✓ 登录成功，正在返回播放器…',true)")
		})
		time.Sleep(250 * time.Millisecond)
		a.dispatch(func() {
			a.host.navigate(fmt.Sprintf("%s/_app/auth?code=%s", a.localBase, code))
		})
	}(pending, tokenBlob, cookie, fallbackReturn)
}

func mergeLines(a, b string) string {
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.Split(a+"\n"+b, "\n") {
		p := strings.TrimSpace(part)
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n")
}

func jsStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func showBannerEvalJS(which string) string {
	label := "新版平台"
	if which == "old" {
		label = "老平台"
	}
	txt := "延河课堂登录中（" + label + "）· 登录完成后将自动返回播放器"
	return "window.__yhktShowLoginBar&&window.__yhktShowLoginBar(" + jsStr(which) + "," + jsStr(txt) + ",false)"
}

func desktopInitScript(localBase string) string {
	return `(() => {
  if (window.__yhktDeskInit) return;
  window.__yhktDeskInit = 1;
  const LOCAL_BASE = ` + jsStr(localBase) + `;
  const post = obj => {
    try { window.chrome && window.chrome.webview && window.chrome.webview.postMessage(JSON.stringify(obj)); } catch (e) {}
  };

  if (location.origin === LOCAL_BASE) {
    window.yhktApp = {
      isDesktop: true,
      login(which) { post({ type: 'login', which: String(which || 'y2') }); },
      clearAuth(which) { post({ type: 'clearAuth', which: String(which || 'all') }); },
      pickDownloadDir() { post({ type: 'pickDownloadDir' }); },
      openDownloadDir(dir) { post({ type: 'openDir', dir: String(dir || '') }); },
    };
    const origOpen = window.open;
    window.open = function(u, n, f) {
      try {
        if (u && /^\/(dl|y2dl)(\?|$)/.test(String(u))) {
          post({ type: 'download', url: new URL(String(u), location.href).href });
          return null;
        }
      } catch (e) {}
      try { return origOpen ? origOpen.call(window, u, n, f) : null; } catch (e) { return null; }
    };
    return;
  }

  window.__yhktShowLoginBar = (which, msg, done) => {
    const mount = () => {
      if (!document.body) return;
      let bar = document.getElementById('__yhktLoginBar');
      if (!bar) {
        bar = document.createElement('div');
        bar.id = '__yhktLoginBar';
        bar.style.cssText = 'position:fixed;top:10px;left:50%;transform:translateX(-50%);z-index:2147483647;'
          + 'display:flex;align-items:center;gap:10px;padding:7px 14px;border-radius:999px;'
          + 'background:rgba(15,19,28,.95);backdrop-filter:blur(12px);border:1px solid rgba(91,156,255,.5);'
          + 'box-shadow:0 10px 30px rgba(0,0,0,.55);color:#eef2f7;'
          + 'font:12.5px/1.4 -apple-system,"Segoe UI",system-ui,"Microsoft YaHei",sans-serif;max-width:94vw;';
        bar.innerHTML = '<span id="__yhktBarDot" style="width:8px;height:8px;border-radius:50%;background:#5b9cff;flex:none"></span>'
          + '<span id="__yhktBarTxt" style="white-space:nowrap;overflow:hidden;text-overflow:ellipsis"></span>'
          + '<button id="__yhktBarGo" type="button" style="border:none;border-radius:999px;padding:4px 11px;background:#5b9cff;color:#051020;font-weight:700;font-size:12px;cursor:pointer;flex:none">我已登录，返回</button>'
          + '<button id="__yhktBarCancel" type="button" style="border:1px solid rgba(255,255,255,.16);border-radius:999px;padding:4px 10px;background:#1a2030;color:#c9d3e3;font-size:12px;cursor:pointer;flex:none">取消</button>';
        document.body.appendChild(bar);
        bar.querySelector('#__yhktBarGo').onclick = () => { if (window.__yhktScanCapture) window.__yhktScanCapture(true); post({ type: 'forceBack' }); };
        bar.querySelector('#__yhktBarCancel').onclick = () => post({ type: 'cancelLogin' });
      }
      bar.querySelector('#__yhktBarTxt').textContent = msg || '延河课堂登录中… 完成后自动返回播放器';
      bar.querySelector('#__yhktBarDot').style.background = done ? '#34d399' : '#5b9cff';
    };
    if (document.body) mount(); else window.addEventListener('DOMContentLoaded', mount, { once: true });
  };

  if (!/yanhekt\.cn$/i.test(location.hostname || '')) return;

  const capToken = val => {
    if (!val) return;
    const s = String(val).replace(/^Bearer\s+/i, '').trim();
    if (s.length >= 20) post({ type: 'captured', host: location.hostname, token: s, cookie: document.cookie || '' });
  };

  try {
    const oSet = XMLHttpRequest.prototype.setRequestHeader;
    XMLHttpRequest.prototype.setRequestHeader = function(k, v) {
      if (typeof k === 'string' && /authorization|blade|token/i.test(k)) capToken(v);
      return oSet.apply(this, arguments);
    };
  } catch (e) {}
  try {
    const oFetch = window.fetch;
    if (oFetch) {
      window.fetch = function(input, init) {
        try {
          const h = (init && init.headers) || (input && input.headers);
          if (h) {
            if (typeof h.forEach === 'function') h.forEach((v, k) => { if (/authorization|blade|token/i.test(k)) capToken(v); });
            else if (typeof h === 'object') Object.keys(h).forEach(k => { if (/authorization|blade|token/i.test(k)) capToken(h[k]); });
          }
        } catch (e) {}
        return oFetch.apply(this, arguments);
      };
    }
  } catch (e) {}

  let lastSent = '';
  const scanAndPost = force => {
    const out = [];
    const add = v => {
      if (!v) return;
      const s = String(v);
      const w = /\{i:\d+;s:\d+:"_token";i:\d+;s:\d+:"(.+?)";\}/.exec(s);
      if (w && out.indexOf(w[1]) < 0) out.push(w[1]);
      const j = /(eyJ[\w-]+\.[\w-]+\.[\w-]*)/.exec(s);
      if (j && out.indexOf(j[1]) < 0) out.push(j[1]);
      else if (s.length >= 20 && s.indexOf(' ') < 0 && out.indexOf(s) < 0) out.push(s);
    };
    const scanStore = st => {
      try {
        for (let i = 0; i < st.length; i++) {
          const k = st.key(i), v = st.getItem(k) || '';
          if (/token|auth|user/i.test(k)) {
            add(v);
            try {
              const o = JSON.parse(v);
              if (o) { add(o.token); add(o.accessToken); if (o.auth) add(o.auth.token); }
            } catch (e) {}
          }
        }
      } catch (e) {}
    };
    let ck = '';
    try { ck = document.cookie || ''; } catch (e) {}
    add(ck);
    scanStore(window.localStorage);
    scanStore(window.sessionStorage);
    const joined = out.join('\n');
    const sig = joined + '|' + ck;
    if (force || (joined && sig !== lastSent)) {
      lastSent = sig;
      post({ type: 'captured', host: location.hostname, token: joined, cookie: ck });
    }
  };
  window.__yhktScanCapture = scanAndPost;
  setInterval(() => scanAndPost(false), 800);
  window.addEventListener('DOMContentLoaded', () => setTimeout(() => scanAndPost(false), 300), { once: true });
})();`
}
