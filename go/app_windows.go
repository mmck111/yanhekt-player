//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

const appWindowClass = "yhkt_app_window"

var (
	ole32                        = windows.NewLazySystemDLL("ole32.dll")
	ole32CoInitializeEx          = ole32.NewProc("CoInitializeEx")
	ole32CoUninitialize          = ole32.NewProc("CoUninitialize")
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

func desktopAppBaseDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.Getenv("APPDATA")
	}
	if base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "yanhekt")
	_ = os.MkdirAll(dir, 0755)
	return dir
}

func desktopDataDir() string {
	dir := filepath.Join(desktopAppBaseDir(), "webview2")
	_ = os.MkdirAll(dir, 0755)
	return dir
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

	if err := host.embed(desktopDataDir()); err != nil || host.webview == nil {
		desktopFail(fmt.Sprintf("host.embed failed: err=%v webview=%v", err, host.webview != nil))
		_, _, _ = user32DestroyWindow.Call(hwnd)
		u := fmt.Sprintf("http://127.0.0.1:%d/", p)
		_ = copyToClipboard(u)
		msg := "初始化 WebView2 窗口失败"
		if err != nil {
			msg += "：" + err.Error()
		}
		showDesktopError(msg + "\n\n已将地址复制到剪贴板：\n" + u)
		return false
	}
	desktopLog("WebView2 embedded successfully, navigating to %s", app.localBase)

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
		a.host.eval("(()=>{var a=document.createElement('a');a.href=" + jsStr(uri) + ";a.download='';document.body.appendChild(a);a.click();a.remove();})()")
		return
	}
	a.host.navigate(uri)
}

// ---------------- 登录截取与跳回流程 ----------------

type webMsgPayload struct {
	Type   string `json:"type"`
	Which  string `json:"which"`
	Host   string `json:"host"`
	Token  string `json:"token"`
	Cookie string `json:"cookie"`
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
		if fallbackReturn {
			a.handleCaptured(uri, extraOld+"\n"+extraY2, "", true)
		}
		return
	}
	ok := cm.getCookiesAsync(uri, func(cookies [][2]string) {
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

		code := issueAppAuthTicket(which, verified, a.returnTargetFor(which))
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
    };
    const origOpen = window.open;
    window.open = function(u, n, f) {
      try {
        if (u && /^\/(dl|y2dl)(\?|$)/.test(String(u))) {
          const a = document.createElement('a');
          a.href = new URL(String(u), location.href).href;
          a.download = '';
          document.body.appendChild(a);
          a.click();
          a.remove();
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
