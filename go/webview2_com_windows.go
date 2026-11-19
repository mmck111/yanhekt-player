//go:build windows

package main

import (
	"fmt"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

type wvColor struct {
	A, R, G, B uint8
}

type wvHost struct {
	hwnd       uintptr
	crInited   uintptr
	env        *wvEnvironment
	controller *wvController
	webview    *wvCore
	fatalErr   error

	hEnv    *wvResObjHandler
	hCtrl   *wvResObjHandler
	hMsg    *wvEventHandler
	hPerm   *wvEventHandler
	hRes    *wvEventHandler
	hNav    *wvEventHandler
	hFull   *wvEventHandler
	hAccel  *wvEventHandler
	hNewWin *wvEventHandler

	onMessage    func(msg string, source string)
	onNavDone    func(source string)
	onWebReq     func(uri string, headers map[string]string)
	onFullscreen func(on bool)
	onAccelKey   func(vk uint32) bool
	onNewWindow  func(uri string)
}

type wvIUnknownVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
}

type wvHandlerVtbl struct {
	wvIUnknownVtbl
	Invoke uintptr
}

// wvResObjHandler 用于 Invoke(this, HRESULT, *IUnknown) 类型回调（Env/Ctrl/GetCookies）
type wvResObjHandler struct {
	vtbl *wvHandlerVtbl
	fn   func(res uintptr, obj unsafe.Pointer)
}

var wvResObjVtbl = wvHandlerVtbl{
	wvIUnknownVtbl: wvIUnknownVtbl{
		QueryInterface: windows.NewCallback(func(h *wvResObjHandler, refiid, obj uintptr) uintptr { return 0 }),
		AddRef:         windows.NewCallback(func(h *wvResObjHandler) uintptr { return 1 }),
		Release:        windows.NewCallback(func(h *wvResObjHandler) uintptr { return 1 }),
	},
	Invoke: windows.NewCallback(func(h *wvResObjHandler, res uintptr, obj unsafe.Pointer) uintptr {
		if h != nil && h.fn != nil {
			h.fn(res, obj)
		}
		return 0
	}),
}

// wvEventHandler 用于 Invoke(this, senderPtr, argsPtr) 类型回调
type wvEventHandler struct {
	vtbl *wvHandlerVtbl
	fn   func(sender, args unsafe.Pointer)
}

var wvEventVtbl = wvHandlerVtbl{
	wvIUnknownVtbl: wvIUnknownVtbl{
		QueryInterface: windows.NewCallback(func(h *wvEventHandler, refiid, obj uintptr) uintptr { return 0 }),
		AddRef:         windows.NewCallback(func(h *wvEventHandler) uintptr { return 1 }),
		Release:        windows.NewCallback(func(h *wvEventHandler) uintptr { return 1 }),
	},
	Invoke: windows.NewCallback(func(h *wvEventHandler, sender, args unsafe.Pointer) uintptr {
		if h != nil && h.fn != nil {
			h.fn(sender, args)
		}
		return 0
	}),
}

func newWVHost(hwnd uintptr) *wvHost {
	h := &wvHost{hwnd: hwnd}
	h.hEnv = &wvResObjHandler{
		vtbl: &wvResObjVtbl,
		fn: func(res uintptr, obj unsafe.Pointer) {
			if int32(res) < 0 || obj == nil {
				h.fatalErr = fmt.Errorf("create WebView2 environment failed: 0x%08x", uint32(res))
				atomic.StoreUintptr(&h.crInited, 2)
				return
			}
			env := (*wvEnvironment)(obj)
			_, _, _ = syscall.SyscallN(env.vtbl.AddRef, uintptr(obj))
			h.env = env
			hr, _, _ := syscall.SyscallN(env.vtbl.CreateCoreWebView2Controller, uintptr(obj), h.hwnd, uintptr(unsafe.Pointer(h.hCtrl)))
			if hr != 0 {
				h.fatalErr = fmt.Errorf("CreateCoreWebView2Controller failed: 0x%08x", uint32(hr))
				atomic.StoreUintptr(&h.crInited, 2)
			}
		},
	}
	h.hCtrl = &wvResObjHandler{
		vtbl: &wvResObjVtbl,
		fn: func(res uintptr, obj unsafe.Pointer) {
			if int32(res) < 0 || obj == nil {
				h.fatalErr = fmt.Errorf("create WebView2 controller failed: 0x%08x", uint32(res))
				atomic.StoreUintptr(&h.crInited, 2)
				return
			}
			ctrl := (*wvController)(obj)
			_, _, _ = syscall.SyscallN(ctrl.vtbl.AddRef, uintptr(obj))
			h.controller = ctrl

			var wv *wvCore
			hr, _, _ := syscall.SyscallN(ctrl.vtbl.GetCoreWebView2, uintptr(obj), uintptr(unsafe.Pointer(&wv)))
			if hr != 0 || wv == nil {
				h.fatalErr = fmt.Errorf("GetCoreWebView2 failed: 0x%08x", uint32(hr))
				atomic.StoreUintptr(&h.crInited, 2)
				return
			}
			_, _, _ = syscall.SyscallN(wv.vtbl.AddRef, uintptr(unsafe.Pointer(wv)))
			h.webview = wv

			var tok int64
			wvPtr := uintptr(unsafe.Pointer(wv))
			_, _, _ = syscall.SyscallN(wv.vtbl.AddWebMessageReceived, wvPtr, uintptr(unsafe.Pointer(h.hMsg)), uintptr(unsafe.Pointer(&tok)))
			_, _, _ = syscall.SyscallN(wv.vtbl.AddPermissionRequested, wvPtr, uintptr(unsafe.Pointer(h.hPerm)), uintptr(unsafe.Pointer(&tok)))
			_, _, _ = syscall.SyscallN(wv.vtbl.AddWebResourceRequested, wvPtr, uintptr(unsafe.Pointer(h.hRes)), uintptr(unsafe.Pointer(&tok)))
			_, _, _ = syscall.SyscallN(wv.vtbl.AddNavigationCompleted, wvPtr, uintptr(unsafe.Pointer(h.hNav)), uintptr(unsafe.Pointer(&tok)))
			_, _, _ = syscall.SyscallN(wv.vtbl.AddContainsFullScreenElementChanged, wvPtr, uintptr(unsafe.Pointer(h.hFull)), uintptr(unsafe.Pointer(&tok)))
			_, _, _ = syscall.SyscallN(wv.vtbl.AddNewWindowRequested, wvPtr, uintptr(unsafe.Pointer(h.hNewWin)), uintptr(unsafe.Pointer(&tok)))
			_, _, _ = syscall.SyscallN(ctrl.vtbl.AddAcceleratorKeyPressed, uintptr(obj), uintptr(unsafe.Pointer(h.hAccel)), uintptr(unsafe.Pointer(&tok)))

			atomic.StoreUintptr(&h.crInited, 1)
		},
	}
	h.hMsg = &wvEventHandler{
		vtbl: &wvEventVtbl,
		fn: func(senderPtr, argsPtr unsafe.Pointer) {
			if argsPtr == nil || h.onMessage == nil {
				return
			}
			var _msg *uint16
			args := (*wvWebMsgArgs)(argsPtr)
			hr, _, _ := syscall.SyscallN(args.vtbl.TryGetWebMessageAsString, uintptr(argsPtr), uintptr(unsafe.Pointer(&_msg)))
			if hr != 0 || _msg == nil {
				return
			}
			msg := windows.UTF16PtrToString(_msg)
			windows.CoTaskMemFree(unsafe.Pointer(_msg))
			src := ""
			if senderPtr != nil {
				src = sourceOfCore((*wvCore)(senderPtr))
			}
			h.onMessage(msg, src)
		},
	}
	h.hPerm = &wvEventHandler{
		vtbl: &wvEventVtbl,
		fn: func(_, argsPtr unsafe.Pointer) {
			if argsPtr == nil {
				return
			}
			args := (*wvPermArgs)(argsPtr)
			var kind uint32
			hr, _, _ := syscall.SyscallN(args.vtbl.GetPermissionKind, uintptr(argsPtr), uintptr(unsafe.Pointer(&kind)))
			if hr == 0 && kind == 6 { // ClipboardRead
				_, _, _ = syscall.SyscallN(args.vtbl.PutState, uintptr(argsPtr), 1)
			}
		},
	}
	h.hRes = &wvEventHandler{
		vtbl: &wvEventVtbl,
		fn: func(_, argsPtr unsafe.Pointer) {
			if argsPtr == nil || h.onWebReq == nil {
				return
			}
			args := (*wvResReqArgs)(argsPtr)
			var req *wvWebRequest
			hr, _, _ := syscall.SyscallN(args.vtbl.GetRequest, uintptr(argsPtr), uintptr(unsafe.Pointer(&req)))
			if hr != 0 || req == nil {
				return
			}
			defer syscall.SyscallN(req.vtbl.Release, uintptr(unsafe.Pointer(req)))

			var _uri *uint16
			hr, _, _ = syscall.SyscallN(req.vtbl.GetUri, uintptr(unsafe.Pointer(req)), uintptr(unsafe.Pointer(&_uri)))
			if hr != 0 || _uri == nil {
				return
			}
			uri := windows.UTF16PtrToString(_uri)
			windows.CoTaskMemFree(unsafe.Pointer(_uri))

			h.onWebReq(uri, readRequestHeaders(req))
		},
	}
	h.hNav = &wvEventHandler{
		vtbl: &wvEventVtbl,
		fn: func(senderPtr, _ unsafe.Pointer) {
			if h.onNavDone != nil && senderPtr != nil {
				h.onNavDone(sourceOfCore((*wvCore)(senderPtr)))
			}
		},
	}
	h.hFull = &wvEventHandler{
		vtbl: &wvEventVtbl,
		fn: func(senderPtr, _ unsafe.Pointer) {
			if h.onFullscreen == nil || senderPtr == nil {
				return
			}
			sender := (*wvCore)(senderPtr)
			var fs int32
			hr, _, _ := syscall.SyscallN(sender.vtbl.GetContainsFullScreenElement, uintptr(senderPtr), uintptr(unsafe.Pointer(&fs)))
			if hr == 0 {
				h.onFullscreen(fs != 0)
			}
		},
	}
	h.hNewWin = &wvEventHandler{
		vtbl: &wvEventVtbl,
		fn: func(_, argsPtr unsafe.Pointer) {
			if argsPtr == nil {
				return
			}
			args := (*wvNewWinArgs)(argsPtr)
			var _uri *uint16
			hr, _, _ := syscall.SyscallN(args.vtbl.GetUri, uintptr(argsPtr), uintptr(unsafe.Pointer(&_uri)))
			uri := ""
			if hr == 0 && _uri != nil {
				uri = windows.UTF16PtrToString(_uri)
				windows.CoTaskMemFree(unsafe.Pointer(_uri))
			}
			_, _, _ = syscall.SyscallN(args.vtbl.PutHandled, uintptr(argsPtr), 1)
			if h.onNewWindow != nil && uri != "" {
				h.onNewWindow(uri)
			}
		},
	}
	h.hAccel = &wvEventHandler{
		vtbl: &wvEventVtbl,
		fn: func(_, argsPtr unsafe.Pointer) {
			if argsPtr == nil || h.onAccelKey == nil {
				return
			}
			args := (*wvAccelArgs)(argsPtr)
			var kind uint32
			hr, _, _ := syscall.SyscallN(args.vtbl.GetKeyEventKind, uintptr(argsPtr), uintptr(unsafe.Pointer(&kind)))
			if hr != 0 || (kind != 0 && kind != 2) {
				return
			}
			var vk uint32
			hr, _, _ = syscall.SyscallN(args.vtbl.GetVirtualKey, uintptr(argsPtr), uintptr(unsafe.Pointer(&vk)))
			if hr != 0 {
				return
			}
			type physStatus struct {
				RepeatCount   uint32
				ScanCode      uint32
				IsExtendedKey int32
				IsMenuKeyDown int32
				WasKeyDown    int32
				IsKeyReleased int32
			}
			var st physStatus
			hr, _, _ = syscall.SyscallN(args.vtbl.GetPhysicalKeyStatus, uintptr(argsPtr), uintptr(unsafe.Pointer(&st)))
			if hr == 0 && st.WasKeyDown == 0 && h.onAccelKey(vk) {
				_, _, _ = syscall.SyscallN(args.vtbl.PutHandled, uintptr(argsPtr), 1)
			}
		},
	}
	return h
}

func (h *wvHost) embed(dataPath string) error {
	pData, err := windows.UTF16PtrFromString(dataPath)
	if err != nil {
		return err
	}
	res, err := webviewloader.CreateCoreWebView2EnvironmentWithOptions(nil, pData, 0, uintptr(unsafe.Pointer(h.hEnv)))
	if err != nil {
		return err
	}
	if int32(res) < 0 {
		return fmt.Errorf("CreateCoreWebView2EnvironmentWithOptions HRESULT 0x%08x", uint32(res))
	}
	var msg winMsg
	for {
		st := atomic.LoadUintptr(&h.crInited)
		if st == 1 {
			break
		}
		if st == 2 {
			if h.fatalErr != nil {
				return h.fatalErr
			}
			return fmt.Errorf("WebView2 initialization aborted")
		}
		r, _, _ := user32GetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		_, _, _ = user32TranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		_, _, _ = user32DispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
	return h.fatalErr
}

func (h *wvHost) resize() {
	if h.controller == nil || h.hwnd == 0 {
		return
	}
	var rc winRect
	_, _, _ = user32GetClientRect.Call(h.hwnd, uintptr(unsafe.Pointer(&rc)))
	_, _, _ = syscall.SyscallN(h.controller.vtbl.PutBounds, uintptr(unsafe.Pointer(h.controller)), uintptr(unsafe.Pointer(&rc)))
}

func (h *wvHost) focus() {
	if h.controller == nil {
		return
	}
	_, _, _ = syscall.SyscallN(h.controller.vtbl.MoveFocus, uintptr(unsafe.Pointer(h.controller)), 0)
}

func (h *wvHost) notifyMove() {
	if h.controller == nil {
		return
	}
	_, _, _ = syscall.SyscallN(h.controller.vtbl.NotifyParentWindowPositionChanged, uintptr(unsafe.Pointer(h.controller)))
}

func (h *wvHost) navigate(u string) {
	if h.webview == nil {
		return
	}
	if p, err := windows.UTF16PtrFromString(u); err == nil {
		_, _, _ = syscall.SyscallN(h.webview.vtbl.Navigate, uintptr(unsafe.Pointer(h.webview)), uintptr(unsafe.Pointer(p)))
	}
}

func (h *wvHost) initScript(js string) {
	if h.webview == nil {
		return
	}
	if p, err := windows.UTF16PtrFromString(js); err == nil {
		_, _, _ = syscall.SyscallN(h.webview.vtbl.AddScriptToExecuteOnDocumentCreated, uintptr(unsafe.Pointer(h.webview)), uintptr(unsafe.Pointer(p)), 0)
	}
}

func (h *wvHost) eval(js string) {
	if h.webview == nil {
		return
	}
	if p, err := windows.UTF16PtrFromString(js); err == nil {
		_, _, _ = syscall.SyscallN(h.webview.vtbl.ExecuteScript, uintptr(unsafe.Pointer(h.webview)), uintptr(unsafe.Pointer(p)), 0)
	}
}

func (h *wvHost) addWebResourceFilter(filter string, ctx uint32) {
	if h.webview == nil {
		return
	}
	if p, err := windows.UTF16PtrFromString(filter); err == nil {
		_, _, _ = syscall.SyscallN(h.webview.vtbl.AddWebResourceRequestedFilter, uintptr(unsafe.Pointer(h.webview)), uintptr(unsafe.Pointer(p)), uintptr(ctx))
	}
}

func sourceOfCore(wv *wvCore) string {
	if wv == nil {
		return ""
	}
	var _src *uint16
	hr, _, _ := syscall.SyscallN(wv.vtbl.GetSource, uintptr(unsafe.Pointer(wv)), uintptr(unsafe.Pointer(&_src)))
	if hr != 0 || _src == nil {
		return ""
	}
	s := windows.UTF16PtrToString(_src)
	windows.CoTaskMemFree(unsafe.Pointer(_src))
	return s
}

func (h *wvHost) configureVisuals(debug bool) {
	if h.controller != nil {
		iidCtrl2 := windows.GUID{Data1: 0xc979903e, Data2: 0xd4ca, Data3: 0x4228, Data4: [8]byte{0x92, 0xeb, 0x47, 0xee, 0x3f, 0xa9, 0x6e, 0xab}}
		var ctrl2 *wvController2
		hr, _, _ := syscall.SyscallN(h.controller.vtbl.QueryInterface, uintptr(unsafe.Pointer(h.controller)), uintptr(unsafe.Pointer(&iidCtrl2)), uintptr(unsafe.Pointer(&ctrl2)))
		if hr == 0 && ctrl2 != nil {
			col := wvColor{A: 255, R: 11, G: 13, B: 18}
			rawCol := *(*uint32)(unsafe.Pointer(&col))
			_, _, _ = syscall.SyscallN(ctrl2.vtbl.PutDefaultBackgroundColor, uintptr(unsafe.Pointer(ctrl2)), uintptr(rawCol))
			_, _, _ = syscall.SyscallN(ctrl2.vtbl.Release, uintptr(unsafe.Pointer(ctrl2)))
		}
	}
	if h.webview != nil {
		var st *wvSettings
		hr, _, _ := syscall.SyscallN(h.webview.vtbl.GetSettings, uintptr(unsafe.Pointer(h.webview)), uintptr(unsafe.Pointer(&st)))
		if hr == 0 && st != nil {
			stPtr := uintptr(unsafe.Pointer(st))
			_, _, _ = syscall.SyscallN(st.vtbl.PutIsStatusBarEnabled, stPtr, 0)
			dev := uintptr(0)
			if debug {
				dev = 1
			}
			_, _, _ = syscall.SyscallN(st.vtbl.PutAreDevToolsEnabled, stPtr, dev)
			_, _, _ = syscall.SyscallN(st.vtbl.PutAreDefaultContextMenusEnabled, stPtr, 1)
			_, _, _ = syscall.SyscallN(st.vtbl.PutIsBuiltInErrorPageEnabled, stPtr, 1)

			iidSet6 := windows.GUID{Data1: 0x11cb3acd, Data2: 0x9bc8, Data3: 0x43b8, Data4: [8]byte{0x83, 0xbf, 0xf4, 0x07, 0x53, 0x71, 0x4f, 0x87}}
			var st6 *wvSettings6
			hr2, _, _ := syscall.SyscallN(st.vtbl.QueryInterface, stPtr, uintptr(unsafe.Pointer(&iidSet6)), uintptr(unsafe.Pointer(&st6)))
			if hr2 == 0 && st6 != nil {
				_, _, _ = syscall.SyscallN(st6.vtbl.PutIsSwipeNavigationEnabled, uintptr(unsafe.Pointer(st6)), 0)
				_, _, _ = syscall.SyscallN(st6.vtbl.Release, uintptr(unsafe.Pointer(st6)))
			}
			_, _, _ = syscall.SyscallN(st.vtbl.Release, stPtr)
		}
	}
}

func (h *wvHost) getCookieManager() *wvCookieManager {
	if h.webview == nil {
		return nil
	}
	iid2 := windows.GUID{Data1: 0x9E8F0CF8, Data2: 0xE670, Data3: 0x4B5E, Data4: [8]byte{0xB2, 0xBC, 0x73, 0xE0, 0x61, 0xE3, 0x18, 0x4C}}
	var wv2 *wvCore2
	hr, _, _ := syscall.SyscallN(h.webview.vtbl.QueryInterface, uintptr(unsafe.Pointer(h.webview)), uintptr(unsafe.Pointer(&iid2)), uintptr(unsafe.Pointer(&wv2)))
	if hr != 0 || wv2 == nil {
		return nil
	}
	defer syscall.SyscallN(wv2.vtbl.Release, uintptr(unsafe.Pointer(wv2)))
	var cm *wvCookieManager
	hr, _, _ = syscall.SyscallN(wv2.vtbl.GetCookieManager, uintptr(unsafe.Pointer(wv2)), uintptr(unsafe.Pointer(&cm)))
	if hr != 0 || cm == nil {
		return nil
	}
	return cm
}

func (cm *wvCookieManager) release() {
	if cm != nil {
		_, _, _ = syscall.SyscallN(cm.vtbl.Release, uintptr(unsafe.Pointer(cm)))
	}
}

func (cm *wvCookieManager) deleteCookies(name, uri string) {
	if cm == nil {
		return
	}
	n, err1 := windows.UTF16PtrFromString(name)
	u, err2 := windows.UTF16PtrFromString(uri)
	if err1 != nil || err2 != nil {
		return
	}
	_, _, _ = syscall.SyscallN(cm.vtbl.DeleteCookies, uintptr(unsafe.Pointer(cm)), uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(u)))
}

func (cm *wvCookieManager) getCookiesAsync(uri string, cb func(pairs [][2]string)) bool {
	if cm == nil {
		return false
	}
	u, err := windows.UTF16PtrFromString(uri)
	if err != nil {
		return false
	}
	var handler *wvResObjHandler
	handler = &wvResObjHandler{
		vtbl: &wvResObjVtbl,
		fn: func(res uintptr, listPtr unsafe.Pointer) {
			defer cm.release()
			wvPinDelete(handler)
			var out [][2]string
			if int32(res) >= 0 && listPtr != nil {
				list := (*wvCookieList)(listPtr)
				var cnt uint32
				hr, _, _ := syscall.SyscallN(list.vtbl.GetCount, uintptr(listPtr), uintptr(unsafe.Pointer(&cnt)))
				if hr == 0 {
					for i := uint32(0); i < cnt; i++ {
						var ck *wvCookie
						hr2, _, _ := syscall.SyscallN(list.vtbl.GetItem, uintptr(listPtr), uintptr(i), uintptr(unsafe.Pointer(&ck)))
						if hr2 != 0 || ck == nil {
							continue
						}
						ckPtr := uintptr(unsafe.Pointer(ck))
						var _n, _v *uint16
						hrn, _, _ := syscall.SyscallN(ck.vtbl.GetName, ckPtr, uintptr(unsafe.Pointer(&_n)))
						hrv, _, _ := syscall.SyscallN(ck.vtbl.GetValue, ckPtr, uintptr(unsafe.Pointer(&_v)))
						name, val := "", ""
						if hrn == 0 && _n != nil {
							name = windows.UTF16PtrToString(_n)
							windows.CoTaskMemFree(unsafe.Pointer(_n))
						}
						if hrv == 0 && _v != nil {
							val = windows.UTF16PtrToString(_v)
							windows.CoTaskMemFree(unsafe.Pointer(_v))
						}
						_, _, _ = syscall.SyscallN(ck.vtbl.Release, ckPtr)
						if name != "" && val != "" {
							out = append(out, [2]string{name, val})
						}
					}
				}
				_, _, _ = syscall.SyscallN(list.vtbl.Release, uintptr(listPtr))
			}
			cb(out)
		},
	}
	wvPinAdd(handler)
	hr, _, _ := syscall.SyscallN(cm.vtbl.GetCookies, uintptr(unsafe.Pointer(cm)), uintptr(unsafe.Pointer(u)), uintptr(unsafe.Pointer(handler)))
	if hr != 0 {
		wvPinDelete(handler)
		cm.release()
		return false
	}
	return true
}

func readRequestHeaders(req *wvWebRequest) map[string]string {
	var hdrs *wvHttpHeaders
	hr, _, _ := syscall.SyscallN(req.vtbl.GetHeaders, uintptr(unsafe.Pointer(req)), uintptr(unsafe.Pointer(&hdrs)))
	if hr != 0 || hdrs == nil {
		return nil
	}
	hdrPtr := uintptr(unsafe.Pointer(hdrs))
	defer syscall.SyscallN(hdrs.vtbl.Release, hdrPtr)

	var it *wvHeaderIter
	hr, _, _ = syscall.SyscallN(hdrs.vtbl.GetIterator, hdrPtr, uintptr(unsafe.Pointer(&it)))
	if hr != 0 || it == nil {
		return nil
	}
	itPtr := uintptr(unsafe.Pointer(it))
	defer syscall.SyscallN(it.vtbl.Release, itPtr)

	out := map[string]string{}
	for {
		var has int32
		hr, _, _ := syscall.SyscallN(it.vtbl.GetHasCurrentHeader, itPtr, uintptr(unsafe.Pointer(&has)))
		if hr != 0 || has == 0 {
			break
		}
		var _k, _v *uint16
		hr, _, _ = syscall.SyscallN(it.vtbl.GetCurrentHeader, itPtr, uintptr(unsafe.Pointer(&_k)), uintptr(unsafe.Pointer(&_v)))
		if hr == 0 {
			k, v := "", ""
			if _k != nil {
				k = windows.UTF16PtrToString(_k)
				windows.CoTaskMemFree(unsafe.Pointer(_k))
			}
			if _v != nil {
				v = windows.UTF16PtrToString(_v)
				windows.CoTaskMemFree(unsafe.Pointer(_v))
			}
			if k != "" && v != "" {
				out[k] = v
			}
		}
		var next int32
		hr, _, _ = syscall.SyscallN(it.vtbl.MoveNext, itPtr, uintptr(unsafe.Pointer(&next)))
		if hr != 0 || next == 0 {
			break
		}
	}
	return out
}

// ---------------- WebView2 COM VTable 结构定义 ----------------

type wvEnvironmentVtbl struct {
	wvIUnknownVtbl
	CreateCoreWebView2Controller uintptr
}
type wvEnvironment struct{ vtbl *wvEnvironmentVtbl }

type wvControllerVtbl struct {
	wvIUnknownVtbl
	GetIsVisible                      uintptr
	PutIsVisible                      uintptr
	GetBounds                         uintptr
	PutBounds                         uintptr
	GetZoomFactor                     uintptr
	PutZoomFactor                     uintptr
	AddZoomFactorChanged              uintptr
	RemoveZoomFactorChanged           uintptr
	SetBoundsAndZoomFactor            uintptr
	MoveFocus                         uintptr
	AddMoveFocusRequested             uintptr
	RemoveMoveFocusRequested          uintptr
	AddGotFocus                       uintptr
	RemoveGotFocus                    uintptr
	AddLostFocus                      uintptr
	RemoveLostFocus                   uintptr
	AddAcceleratorKeyPressed          uintptr
	RemoveAcceleratorKeyPressed       uintptr
	GetParentWindow                   uintptr
	PutParentWindow                   uintptr
	NotifyParentWindowPositionChanged uintptr
	Close                             uintptr
	GetCoreWebView2                   uintptr
}
type wvController struct{ vtbl *wvControllerVtbl }

type wvController2Vtbl struct {
	wvControllerVtbl
	GetDefaultBackgroundColor uintptr
	PutDefaultBackgroundColor uintptr
}
type wvController2 struct{ vtbl *wvController2Vtbl }

type wvCoreVtbl struct {
	wvIUnknownVtbl
	GetSettings                            uintptr
	GetSource                              uintptr
	Navigate                               uintptr
	NavigateToString                       uintptr
	AddNavigationStarting                  uintptr
	RemoveNavigationStarting               uintptr
	AddContentLoading                      uintptr
	RemoveContentLoading                   uintptr
	AddSourceChanged                       uintptr
	RemoveSourceChanged                    uintptr
	AddHistoryChanged                      uintptr
	RemoveHistoryChanged                   uintptr
	AddNavigationCompleted                 uintptr
	RemoveNavigationCompleted              uintptr
	AddFrameNavigationStarting             uintptr
	RemoveFrameNavigationStarting          uintptr
	AddFrameNavigationCompleted            uintptr
	RemoveFrameNavigationCompleted         uintptr
	AddScriptDialogOpening                 uintptr
	RemoveScriptDialogOpening              uintptr
	AddPermissionRequested                 uintptr
	RemovePermissionRequested              uintptr
	AddProcessFailed                       uintptr
	RemoveProcessFailed                    uintptr
	AddScriptToExecuteOnDocumentCreated    uintptr
	RemoveScriptToExecuteOnDocumentCreated uintptr
	ExecuteScript                          uintptr
	CapturePreview                         uintptr
	Reload                                 uintptr
	PostWebMessageAsJSON                   uintptr
	PostWebMessageAsString                 uintptr
	AddWebMessageReceived                  uintptr
	RemoveWebMessageReceived               uintptr
	CallDevToolsProtocolMethod             uintptr
	GetBrowserProcessID                    uintptr
	GetCanGoBack                           uintptr
	GetCanGoForward                        uintptr
	GoBack                                 uintptr
	GoForward                              uintptr
	GetDevToolsProtocolEventReceiver       uintptr
	Stop                                   uintptr
	AddNewWindowRequested                  uintptr
	RemoveNewWindowRequested               uintptr
	AddDocumentTitleChanged                uintptr
	RemoveDocumentTitleChanged             uintptr
	GetDocumentTitle                       uintptr
	AddHostObjectToScript                  uintptr
	RemoveHostObjectFromScript             uintptr
	OpenDevToolsWindow                     uintptr
	AddContainsFullScreenElementChanged    uintptr
	RemoveContainsFullScreenElementChanged uintptr
	GetContainsFullScreenElement           uintptr
	AddWebResourceRequested                uintptr
	RemoveWebResourceRequested             uintptr
	AddWebResourceRequestedFilter          uintptr
	RemoveWebResourceRequestedFilter       uintptr
	AddWindowCloseRequested                uintptr
	RemoveWindowCloseRequested             uintptr
}
type wvCore struct{ vtbl *wvCoreVtbl }

type wvCore2Vtbl struct {
	wvCoreVtbl
	AddWebResourceResponseReceived    uintptr
	RemoveWebResourceResponseReceived uintptr
	NavigateWithWebResourceRequest    uintptr
	AddDomContentLoaded               uintptr
	RemoveDomContentLoaded            uintptr
	GetCookieManager                  uintptr
	GetEnvironment                    uintptr
}
type wvCore2 struct{ vtbl *wvCore2Vtbl }

type wvSettingsVtbl struct {
	wvIUnknownVtbl
	GetIsScriptEnabled                uintptr
	PutIsScriptEnabled                uintptr
	GetIsWebMessageEnabled            uintptr
	PutIsWebMessageEnabled            uintptr
	GetAreDefaultScriptDialogsEnabled uintptr
	PutAreDefaultScriptDialogsEnabled uintptr
	GetIsStatusBarEnabled             uintptr
	PutIsStatusBarEnabled             uintptr
	GetAreDevToolsEnabled             uintptr
	PutAreDevToolsEnabled             uintptr
	GetAreDefaultContextMenusEnabled  uintptr
	PutAreDefaultContextMenusEnabled  uintptr
	GetAreHostObjectsAllowed          uintptr
	PutAreHostObjectsAllowed          uintptr
	GetIsZoomControlEnabled           uintptr
	PutIsZoomControlEnabled           uintptr
	GetIsBuiltInErrorPageEnabled      uintptr
	PutIsBuiltInErrorPageEnabled      uintptr
}
type wvSettings struct{ vtbl *wvSettingsVtbl }

type wvSettings6Vtbl struct {
	wvSettingsVtbl
	GetUserAgent                        uintptr
	PutUserAgent                        uintptr
	GetAreBrowserAcceleratorKeysEnabled uintptr
	PutAreBrowserAcceleratorKeysEnabled uintptr
	GetIsPasswordAutosaveEnabled        uintptr
	PutIsPasswordAutosaveEnabled        uintptr
	GetIsGeneralAutofillEnabled         uintptr
	PutIsGeneralAutofillEnabled         uintptr
	GetIsPinchZoomEnabled               uintptr
	PutIsPinchZoomEnabled               uintptr
	GetIsSwipeNavigationEnabled         uintptr
	PutIsSwipeNavigationEnabled         uintptr
}
type wvSettings6 struct{ vtbl *wvSettings6Vtbl }

type wvWebMsgArgsVtbl struct {
	wvIUnknownVtbl
	GetSource, GetWebMessageAsJSON, TryGetWebMessageAsString uintptr
}
type wvWebMsgArgs struct{ vtbl *wvWebMsgArgsVtbl }

type wvPermArgsVtbl struct {
	wvIUnknownVtbl
	GetURI, GetPermissionKind, GetIsUserInitiated, GetState, PutState, GetDeferral uintptr
}
type wvPermArgs struct{ vtbl *wvPermArgsVtbl }

type wvResReqArgsVtbl struct {
	wvIUnknownVtbl
	GetRequest, GetResponse, PutResponse, GetDeferral, GetResourceContext uintptr
}
type wvResReqArgs struct{ vtbl *wvResReqArgsVtbl }

type wvWebRequestVtbl struct {
	wvIUnknownVtbl
	GetUri, PutUri, GetMethod, PutMethod, GetContent, PutContent, GetHeaders uintptr
}
type wvWebRequest struct{ vtbl *wvWebRequestVtbl }

type wvHttpHeadersVtbl struct {
	wvIUnknownVtbl
	GetHeader, GetHeaders, Contains, SetHeader, RemoveHeader, GetIterator uintptr
}
type wvHttpHeaders struct{ vtbl *wvHttpHeadersVtbl }

type wvHeaderIterVtbl struct {
	wvIUnknownVtbl
	GetCurrentHeader, GetHasCurrentHeader, MoveNext uintptr
}
type wvHeaderIter struct{ vtbl *wvHeaderIterVtbl }

type wvNewWinArgsVtbl struct {
	wvIUnknownVtbl
	GetUri, PutNewWindow, GetNewWindow, PutHandled uintptr
}
type wvNewWinArgs struct{ vtbl *wvNewWinArgsVtbl }

type wvAccelArgsVtbl struct {
	wvIUnknownVtbl
	GetKeyEventKind, GetVirtualKey, GetKeyEventLParam, GetPhysicalKeyStatus, GetHandled, PutHandled uintptr
}
type wvAccelArgs struct{ vtbl *wvAccelArgsVtbl }

type wvCookieManagerVtbl struct {
	wvIUnknownVtbl
	CreateCookie, CopyCookie, GetCookies, AddOrUpdateCookie, DeleteCookie, DeleteCookies, DeleteCookiesWithDomainAndPath, DeleteAllCookies uintptr
}
type wvCookieManager struct{ vtbl *wvCookieManagerVtbl }

type wvCookieListVtbl struct {
	wvIUnknownVtbl
	GetCount, GetItem uintptr
}
type wvCookieList struct{ vtbl *wvCookieListVtbl }

type wvCookieVtbl struct {
	wvIUnknownVtbl
	GetName, GetValue, PutValue, GetDomain, GetPath, GetExpires, PutExpires, GetIsHttpOnly, PutIsHttpOnly, GetSameSite, PutSameSite, GetIsSecure, PutIsSecure uintptr
}
type wvCookie struct{ vtbl *wvCookieVtbl }
