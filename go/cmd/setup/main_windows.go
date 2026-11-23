//go:build windows && setup

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	ole32                      = windows.NewLazySystemDLL("ole32.dll")
	ole32CoInitializeEx        = ole32.NewProc("CoInitializeEx")
	ole32CoUninitialize        = ole32.NewProc("CoUninitialize")
	ole32CoCreateInstance      = ole32.NewProc("CoCreateInstance")
	ole32CoTaskMemFree         = ole32.NewProc("CoTaskMemFree")
	shell32                    = windows.NewLazySystemDLL("shell32.dll")
	shell32SHGetKnownFolder    = shell32.NewProc("SHGetKnownFolderPath")
	shell32SHCreateItem        = shell32.NewProc("SHCreateItemFromParsingName")
	comctl32                   = windows.NewLazySystemDLL("comctl32.dll")
	comctl32InitCommonControls = comctl32.NewProc("InitCommonControlsEx")
	gdi32                      = windows.NewLazySystemDLL("gdi32.dll")
	gdi32CreateFontW           = gdi32.NewProc("CreateFontW")
	gdi32CreateSolidBrush      = gdi32.NewProc("CreateSolidBrush")
	gdi32DeleteObject          = gdi32.NewProc("DeleteObject")
	gdi32SetBkMode             = gdi32.NewProc("SetBkMode")
	gdi32SetTextColor          = gdi32.NewProc("SetTextColor")
	user32                     = windows.NewLazySystemDLL("user32.dll")
	user32RegisterClassExW     = user32.NewProc("RegisterClassExW")
	user32CreateWindowExW      = user32.NewProc("CreateWindowExW")
	user32DefWindowProcW       = user32.NewProc("DefWindowProcW")
	user32DestroyWindow        = user32.NewProc("DestroyWindow")
	user32GetMessageW          = user32.NewProc("GetMessageW")
	user32TranslateMessage     = user32.NewProc("TranslateMessage")
	user32DispatchMessageW     = user32.NewProc("DispatchMessageW")
	user32IsDialogMessageW     = user32.NewProc("IsDialogMessageW")
	user32PostQuitMessage      = user32.NewProc("PostQuitMessage")
	user32SendMessageW         = user32.NewProc("SendMessageW")
	user32GetWindowTextLengthW = user32.NewProc("GetWindowTextLengthW")
	user32GetWindowTextW       = user32.NewProc("GetWindowTextW")
	user32SetWindowTextW       = user32.NewProc("SetWindowTextW")
	user32EnableWindow         = user32.NewProc("EnableWindow")
	user32ShowWindow           = user32.NewProc("ShowWindow")
	user32UpdateWindow         = user32.NewProc("UpdateWindow")
	user32GetSystemMetrics     = user32.NewProc("GetSystemMetrics")
	user32GetDpiForWindow      = user32.NewProc("GetDpiForWindow")
	user32LoadImageW           = user32.NewProc("LoadImageW")
	user32MessageBoxW          = user32.NewProc("MessageBoxW")
	user32SetDpiContext        = user32.NewProc("SetProcessDpiAwarenessContext")
	user32GetDlgCtrlID         = user32.NewProc("GetDlgCtrlID")
)

const (
	idEditInstall    = 101
	idBtnInstallDir  = 102
	idEditDownload   = 103
	idBtnDownloadDir = 104
	idChkDesktop     = 105
	idChkLaunch      = 106
	idLblStatus      = 107
	idBtnGo          = 108
	idBtnCancel      = 109
	idLblTitle       = 110
	idLblSub         = 111
	idLblNote        = 112

	wmDestroy       = 0x0002
	wmSetFont       = 0x0030
	wmClose         = 0x0010
	wmCommand       = 0x0111
	wmCtlColorStat  = 0x0138
	bmGetCheck      = 0x00F0
	bmSetCheck      = 0x00F1
	wsChild         = 0x40000000
	wsVisible       = 0x10000000
	wsTabStop       = 0x00010000
	wsBorder        = 0x00800000
	wsOverlapped    = 0x00CA0000
	esAutoHScroll   = 0x0080
	bsPushButton    = 0x0000
	bsDefPushButton = 0x0001
	bsAutoCheckBox  = 0x0003
)

type winPoint struct{ X, Y int32 }

type winMsg struct {
	Hwnd     windows.HWND
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       winPoint
	LPrivate uint32
}

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

type winIUnknownVtbl struct {
	QueryInterface, AddRef, Release uintptr
}

type winFileDialogVtbl struct {
	winIUnknownVtbl
	Show, SetFileTypes, SetFileTypeIndex, GetFileTypeIndex, Advise, Unadvise uintptr
	SetOptions, GetOptions, SetDefaultFolder, SetFolder, GetFolder             uintptr
	GetCurrentSelection, SetFileName, GetFileName, SetTitle                    uintptr
	SetOkButtonLabel, SetFileNameLabel, GetResult, AddPlace, SetDefaultExt     uintptr
}
type winFileDialog struct{ vtbl *winFileDialogVtbl }

type winShellItemVtbl struct {
	winIUnknownVtbl
	BindToHandler, GetParent, GetDisplayName, GetAttributes, Compare uintptr
}
type winShellItem struct{ vtbl *winShellItemVtbl }

type winShellLinkVtbl struct {
	winIUnknownVtbl
	GetPath, GetIDList, SetIDList, GetDescription, SetDescription            uintptr
	GetWorkingDirectory, SetWorkingDirectory, GetArguments, SetArguments     uintptr
	GetHotkey, SetHotkey, GetShowCmd, SetShowCmd, GetIconLocation            uintptr
	SetIconLocation, SetRelativePath, Resolve, SetPath                       uintptr
}
type winShellLink struct{ vtbl *winShellLinkVtbl }

type winPersistFileVtbl struct {
	winIUnknownVtbl
	GetClassID, IsDirty, Load, Save, SaveCompleted, GetCurFile uintptr
}
type winPersistFile struct{ vtbl *winPersistFileVtbl }

var (
	clsidFileOpenDialog = windows.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE, Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidFileOpenDialog   = windows.GUID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768, Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
	iidShellItem        = windows.GUID{Data1: 0x43826D1E, Data2: 0xE718, Data3: 0x42EE, Data4: [8]byte{0xBC, 0x55, 0xA1, 0xE2, 0x61, 0xC3, 0x7B, 0xFE}}
	clsidShellLink      = windows.GUID{Data1: 0x00021401, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	iidShellLinkW       = windows.GUID{Data1: 0x000214F9, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	iidPersistFile      = windows.GUID{Data1: 0x0000010B, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}

	folderIDDownloads = windows.GUID{Data1: 0x374DE290, Data2: 0x123F, Data3: 0x4565, Data4: [8]byte{0x91, 0x64, 0x39, 0xC4, 0x92, 0x5E, 0x46, 0x7B}}
	folderIDVideos    = windows.GUID{Data1: 0x18989B1D, Data2: 0x99B5, Data3: 0x455B, Data4: [8]byte{0x84, 0x1C, 0xAB, 0x7C, 0x74, 0xE4, 0xDD, 0xFC}}
	folderIDDesktop   = windows.GUID{Data1: 0xB4BFCC3A, Data2: 0xDB2C, Data3: 0x424C, Data4: [8]byte{0xB0, 0x29, 0x7F, 0xE9, 0x9A, 0x87, 0xC6, 0x41}}
	folderIDPrograms  = windows.GUID{Data1: 0xA77F5D77, Data2: 0x2E2B, Data3: 0x44C3, Data4: [8]byte{0xA6, 0xA2, 0xAB, 0xA6, 0x01, 0x05, 0x4A, 0x51}}
)

type setupState struct {
	hwnd         uintptr
	hinst        windows.Handle
	bgBrush      uintptr
	fontBody     uintptr
	fontTitle    uintptr
	fontNote     uintptr
	editInstall  uintptr
	editDownload uintptr
	chkDesktop   uintptr
	chkLaunch    uintptr
	lblStatus    uintptr
	btnGo        uintptr
	btnCancel    uintptr
	busy         bool
}

var appSetup *setupState

func msgBox(hwnd uintptr, title, text string, flags uintptr) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	_, _, _ = user32MessageBoxW.Call(hwnd, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), flags)
}

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
	return tml.Label.Sid.SubAuthority(uint32(cnt-1)) < 0x2000
}

func knownFolder(id windows.GUID) string {
	if shell32SHGetKnownFolder.Find() != nil {
		return ""
	}
	var p *uint16
	hr, _, _ := shell32SHGetKnownFolder.Call(uintptr(unsafe.Pointer(&id)), 0, 0, uintptr(unsafe.Pointer(&p)))
	if p != nil {
		defer ole32CoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
	}
	if hr != 0 || p == nil {
		return ""
	}
	return windows.UTF16PtrToString(p)
}

func pickFolder(hwnd uintptr, title, initDir string) (string, bool) {
	var dlg *winFileDialog
	hr, _, _ := ole32CoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidFileOpenDialog)),
		0, 1,
		uintptr(unsafe.Pointer(&iidFileOpenDialog)),
		uintptr(unsafe.Pointer(&dlg)),
	)
	if hr != 0 || dlg == nil {
		return "", false
	}
	dp := uintptr(unsafe.Pointer(dlg))
	defer syscall.SyscallN(dlg.vtbl.Release, dp)

	var opts uint32
	_, _, _ = syscall.SyscallN(dlg.vtbl.GetOptions, dp, uintptr(unsafe.Pointer(&opts)))
	opts |= 0x00000020 | 0x00000008 | 0x00000040 | 0x00000800
	_, _, _ = syscall.SyscallN(dlg.vtbl.SetOptions, dp, uintptr(opts))

	tp, _ := windows.UTF16PtrFromString(title)
	_, _, _ = syscall.SyscallN(dlg.vtbl.SetTitle, dp, uintptr(unsafe.Pointer(tp)))

	if initDir != "" && shell32SHCreateItem.Find() == nil {
		if ip, err := windows.UTF16PtrFromString(initDir); err == nil {
			var item *winShellItem
			if hr2, _, _ := shell32SHCreateItem.Call(uintptr(unsafe.Pointer(ip)), 0, uintptr(unsafe.Pointer(&iidShellItem)), uintptr(unsafe.Pointer(&item))); hr2 == 0 && item != nil {
				_, _, _ = syscall.SyscallN(dlg.vtbl.SetFolder, dp, uintptr(unsafe.Pointer(item)))
				_, _, _ = syscall.SyscallN(item.vtbl.Release, uintptr(unsafe.Pointer(item)))
			}
		}
	}

	if hrShow, _, _ := syscall.SyscallN(dlg.vtbl.Show, dp, hwnd); hrShow != 0 {
		return "", false
	}
	var res *winShellItem
	if hrRes, _, _ := syscall.SyscallN(dlg.vtbl.GetResult, dp, uintptr(unsafe.Pointer(&res))); hrRes != 0 || res == nil {
		return "", false
	}
	rp := uintptr(unsafe.Pointer(res))
	defer syscall.SyscallN(res.vtbl.Release, rp)

	var psz *uint16
	if hrPath, _, _ := syscall.SyscallN(res.vtbl.GetDisplayName, rp, 0x80058000, uintptr(unsafe.Pointer(&psz))); hrPath != 0 || psz == nil {
		return "", false
	}
	defer ole32CoTaskMemFree.Call(uintptr(unsafe.Pointer(psz)))
	return windows.UTF16PtrToString(psz), true
}

func createShortcut(lnkPath, targetExe, workDir, desc string) error {
	_ = os.MkdirAll(filepath.Dir(lnkPath), 0755)
	var sl *winShellLink
	hr, _, _ := ole32CoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidShellLink)),
		0, 1,
		uintptr(unsafe.Pointer(&iidShellLinkW)),
		uintptr(unsafe.Pointer(&sl)),
	)
	if hr != 0 || sl == nil {
		return fmt.Errorf("CoCreateInstance(ShellLink) failed: 0x%08x", uint32(hr))
	}
	sp := uintptr(unsafe.Pointer(sl))
	defer syscall.SyscallN(sl.vtbl.Release, sp)

	pExe, _ := windows.UTF16PtrFromString(targetExe)
	pDir, _ := windows.UTF16PtrFromString(workDir)
	pDesc, _ := windows.UTF16PtrFromString(desc)
	_, _, _ = syscall.SyscallN(sl.vtbl.SetPath, sp, uintptr(unsafe.Pointer(pExe)))
	_, _, _ = syscall.SyscallN(sl.vtbl.SetWorkingDirectory, sp, uintptr(unsafe.Pointer(pDir)))
	_, _, _ = syscall.SyscallN(sl.vtbl.SetDescription, sp, uintptr(unsafe.Pointer(pDesc)))
	_, _, _ = syscall.SyscallN(sl.vtbl.SetIconLocation, sp, uintptr(unsafe.Pointer(pExe)), 0)

	var pf *winPersistFile
	hrQ, _, _ := syscall.SyscallN(sl.vtbl.QueryInterface, sp, uintptr(unsafe.Pointer(&iidPersistFile)), uintptr(unsafe.Pointer(&pf)))
	if hrQ != 0 || pf == nil {
		return fmt.Errorf("QueryInterface(IPersistFile) failed: 0x%08x", uint32(hrQ))
	}
	pp := uintptr(unsafe.Pointer(pf))
	defer syscall.SyscallN(pf.vtbl.Release, pp)

	pLnk, _ := windows.UTF16PtrFromString(lnkPath)
	hrS, _, _ := syscall.SyscallN(pf.vtbl.Save, pp, uintptr(unsafe.Pointer(pLnk)), 1)
	if hrS != 0 {
		return fmt.Errorf("IPersistFile.Save failed: 0x%08x", uint32(hrS))
	}
	return nil
}

func getText(hwnd uintptr) string {
	n, _, _ := user32GetWindowTextLengthW.Call(hwnd)
	buf := make([]uint16, n+2)
	_, _, _ = user32GetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), n+1)
	return strings.TrimSpace(windows.UTF16ToString(buf))
}

func setText(hwnd uintptr, s string) {
	p, _ := windows.UTF16PtrFromString(s)
	_, _, _ = user32SetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(p)))
}

func isChecked(hwnd uintptr) bool {
	r, _, _ := user32SendMessageW.Call(hwnd, bmGetCheck, 0, 0)
	return r == 1
}

func probeDirWritable(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".yhkt-write-test-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
}

func killRunningApp() {
	cmd := exec.Command("taskkill", "/F", "/IM", "yanhekt.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Run()
	time.Sleep(180 * time.Millisecond)
}

func registerUninstall(installDir, exePath string) {
	self, err := os.Executable()
	if err != nil || self == "" {
		return
	}
	uninst := filepath.Join(installDir, "uninstall.exe")
	if b, err := os.ReadFile(self); err == nil {
		_ = os.WriteFile(uninst, b, 0755)
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Uninstall\YanhektPlayer`, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	_ = k.SetStringValue("DisplayName", "延河课堂在线播放器")
	_ = k.SetStringValue("DisplayVersion", appVersion)
	_ = k.SetStringValue("Publisher", "Yanhekt Player")
	_ = k.SetStringValue("InstallLocation", installDir)
	_ = k.SetStringValue("DisplayIcon", exePath)
	_ = k.SetStringValue("UninstallString", fmt.Sprintf(`"%s" --uninstall "%s"`, uninst, installDir))
	_ = k.SetDWordValue("NoModify", 1)
	_ = k.SetDWordValue("NoRepair", 1)
}

func runUninstall(installDir string) {
	killRunningApp()
	if installDir == "" {
		if exe, err := os.Executable(); err == nil {
			installDir = filepath.Dir(exe)
		}
	}
	if d := knownFolder(folderIDDesktop); d != "" {
		_ = os.Remove(filepath.Join(d, "延河课堂在线播放器.lnk"))
	}
	if p := knownFolder(folderIDPrograms); p != "" {
		_ = os.Remove(filepath.Join(p, "Yanhekt Player.lnk"))
	}
	_ = registry.DeleteKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Uninstall\YanhektPlayer`)
	_ = os.Remove(filepath.Join(installDir, "yanhekt.exe"))
	_ = os.Remove(filepath.Join(installDir, "defaults.json"))
	if installDir != "" {
		cmd := exec.Command("cmd", "/c", "ping -n 2 127.0.0.1 >nul & rmdir /s /q "+`"`+installDir+`"`)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		_ = cmd.Start()
	}
	msgBox(0, "延河课堂在线播放器 · 卸载", "已卸载延河课堂在线播放器（已保留您已下载的视频文件）。", 0x40)
}

func (s *setupState) runInstall() {
	if s.busy {
		return
	}
	installDir := getText(s.editInstall)
	downloadDir := getText(s.editDownload)
	if installDir == "" {
		msgBox(s.hwnd, "提示", "请填写或选择软件安装路径。", 0x30)
		return
	}
	if downloadDir == "" {
		msgBox(s.hwnd, "提示", "请填写或选择默认视频下载路径。", 0x30)
		return
	}

	s.busy = true
	_, _, _ = user32EnableWindow.Call(s.btnGo, 0)
	_, _, _ = user32EnableWindow.Call(s.btnCancel, 0)
	setText(s.lblStatus, "正在检查目录权限...")

	if err := probeDirWritable(installDir); err != nil {
		s.busy = false
		_, _, _ = user32EnableWindow.Call(s.btnGo, 1)
		_, _, _ = user32EnableWindow.Call(s.btnCancel, 1)
		setText(s.lblStatus, "安装路径不可写")
		msgBox(s.hwnd, "安装失败", "安装路径没有写入权限：\n"+installDir+"\n\n"+err.Error()+"\n\n建议改选个人目录，如 %LOCALAPPDATA%\\Programs\\Yanhekt Player。", 0x10)
		return
	}
	if err := probeDirWritable(downloadDir); err != nil {
		s.busy = false
		_, _, _ = user32EnableWindow.Call(s.btnGo, 1)
		_, _, _ = user32EnableWindow.Call(s.btnCancel, 1)
		setText(s.lblStatus, "下载目录不可写")
		msgBox(s.hwnd, "下载目录不可写", "所选默认视频下载路径无法写入：\n"+downloadDir+"\n\n"+err.Error()+"\n\n请换一个你有权限的文件夹。", 0x10)
		return
	}

	setText(s.lblStatus, "正在释放程序文件...")
	killRunningApp()
	exePath := filepath.Join(installDir, "yanhekt.exe")
	if err := extractGzipPayload(embeddedExeGz, exePath); err != nil {
		s.busy = false
		_, _, _ = user32EnableWindow.Call(s.btnGo, 1)
		_, _, _ = user32EnableWindow.Call(s.btnCancel, 1)
		setText(s.lblStatus, "写入程序失败")
		msgBox(s.hwnd, "安装失败", "无法写出主程序：\n"+err.Error(), 0x10)
		return
	}

	setText(s.lblStatus, "正在保存默认下载配置...")
	_ = applyInstallSettings(installDir, downloadDir, os.Getenv("LOCALAPPDATA"))

	setText(s.lblStatus, "正在创建快捷方式...")
	if isChecked(s.chkDesktop) {
		if d := knownFolder(folderIDDesktop); d != "" {
			_ = createShortcut(filepath.Join(d, "延河课堂在线播放器.lnk"), exePath, installDir, "延河课堂在线播放器")
		}
		if p := knownFolder(folderIDPrograms); p != "" {
			_ = createShortcut(filepath.Join(p, "Yanhekt Player.lnk"), exePath, installDir, "延河课堂在线播放器")
		}
	}
	registerUninstall(installDir, exePath)

	launch := isChecked(s.chkLaunch)
	setText(s.lblStatus, "安装完成")
	if launch {
		cmd := exec.Command(exePath)
		cmd.Dir = installDir
		_ = cmd.Start()
	} else {
		msgBox(s.hwnd, "安装完成", "已安装到：\n"+installDir+"\n\n默认视频下载目录：\n"+downloadDir+"\n\n（注：随时可在播放器左侧「⚙ 设置」中修改）", 0x40)
	}
	_, _, _ = user32DestroyWindow.Call(s.hwnd)
}

func setupWndProc(hwnd, msg, wp, lp uintptr) uintptr {
	s := appSetup
	switch msg {
	case wmCtlColorStat:
		if s != nil {
			_, _, _ = gdi32SetBkMode.Call(wp, 1) // TRANSPARENT
			cid, _, _ := user32GetDlgCtrlID.Call(lp)
			switch cid {
			case idLblSub, idLblStatus:
				_, _, _ = gdi32SetTextColor.Call(wp, 0x006B5B52)
			case idLblNote:
				_, _, _ = gdi32SetTextColor.Call(wp, 0x00C85818)
			default:
				_, _, _ = gdi32SetTextColor.Call(wp, 0x00241B14)
			}
			return s.bgBrush
		}
	case wmCommand:
		if s != nil && (wp>>16)&0xFFFF == 0 {
			switch wp & 0xFFFF {
			case idBtnInstallDir:
				if p, ok := pickFolder(hwnd, "选择软件安装路径", getText(s.editInstall)); ok && p != "" {
					if !strings.EqualFold(filepath.Base(p), "Yanhekt Player") {
						p = filepath.Join(p, "Yanhekt Player")
					}
					setText(s.editInstall, p)
				}
			case idBtnDownloadDir:
				if p, ok := pickFolder(hwnd, "选择默认视频下载路径", getText(s.editDownload)); ok && p != "" {
					setText(s.editDownload, p)
				}
			case idBtnGo:
				s.runInstall()
			case idBtnCancel:
				_, _, _ = user32DestroyWindow.Call(hwnd)
			}
			return 0
		}
	case wmClose:
		if s == nil || !s.busy {
			_, _, _ = user32DestroyWindow.Call(hwnd)
		}
		return 0
	case wmDestroy:
		_, _, _ = user32PostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := user32DefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

func makeFont(px int32, bold bool) uintptr {
	w := uintptr(400)
	if bold {
		w = 700
	}
	face, _ := windows.UTF16PtrFromString("Microsoft YaHei UI")
	h, _, _ := gdi32CreateFontW.Call(
		uintptr(^uint32(px-1)), 0, 0, 0, w, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(face)),
	)
	return h
}

func ctrl(cls, text string, style, exStyle, x, y, w, h, parent, id, font uintptr, hinst windows.Handle) uintptr {
	cPtr, _ := windows.UTF16PtrFromString(cls)
	tPtr, _ := windows.UTF16PtrFromString(text)
	hw, _, _ := user32CreateWindowExW.Call(
		exStyle, uintptr(unsafe.Pointer(cPtr)), uintptr(unsafe.Pointer(tPtr)),
		wsChild|wsVisible|style, uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		parent, id, uintptr(hinst), 0,
	)
	if font != 0 && hw != 0 {
		_, _, _ = user32SendMessageW.Call(hw, wmSetFont, font, 1)
	}
	return hw
}

func main() {
	runtime.LockOSThread()
	_ = ole32.Load()
	_ = user32.Load()
	_ = gdi32.Load()
	_ = shell32.Load()
	_ = comctl32.Load()

	if user32SetDpiContext.Find() == nil {
		_, _, _ = user32SetDpiContext.Call(^uintptr(3))
	}
	_, _, _ = ole32CoInitializeEx.Call(0, 2)
	defer ole32CoUninitialize.Call()

	for i, a := range os.Args[1:] {
		if a == "--uninstall" {
			dir := ""
			if i+2 < len(os.Args) {
				dir = os.Args[i+2]
			}
			runUninstall(dir)
			return
		}
	}

	if isLowIntegrityProcess() {
		msgBox(0, "延河课堂在线播放器 · 安装向导",
			"检测到当前安装包位于低完整性受限目录（例如工作区 dist 目录），Windows 会阻止它写入程序安装目录与桌面。\n\n请先把本安装包「复制」到桌面或其他普通文件夹（勿同盘剪切），然后再双击运行。",
			0x30)
		return
	}

	var icc struct{ size, icc uint32 }
	icc.size = 8
	icc.icc = 0x00004000
	_, _, _ = comctl32InitCommonControls.Call(uintptr(unsafe.Pointer(&icc)))

	var hinst windows.Handle
	_ = windows.GetModuleHandleEx(windows.GET_MODULE_HANDLE_EX_FLAG_UNCHANGED_REFCOUNT, nil, &hinst)

	bgBrush, _, _ := gdi32CreateSolidBrush.Call(0x00F6F4F2)
	icon, _, _ := user32LoadImageW.Call(uintptr(hinst), 1, 1, 0, 0, 0x0040|0x8000)
	if icon == 0 {
		icon, _, _ = user32LoadImageW.Call(0, 32512, 1, 0, 0, 0x0040|0x8000)
	}
	cursor, _, _ := user32LoadImageW.Call(0, 32512, 2, 0, 0, 0x8000)

	clsName, _ := windows.UTF16PtrFromString("yhkt_setup_wnd")
	titleStr, _ := windows.UTF16PtrFromString("延河课堂在线播放器 · 安装向导 v" + appVersion)
	wc := winWndClassExW{
		CbSize:        uint32(unsafe.Sizeof(winWndClassExW{})),
		LpfnWndProc:   windows.NewCallback(setupWndProc),
		HInstance:     hinst,
		HIcon:         windows.Handle(icon),
		HIconSm:       windows.Handle(icon),
		HCursor:       windows.Handle(cursor),
		HbrBackground: windows.Handle(bgBrush),
		LpszClassName: clsName,
	}
	_, _, _ = user32RegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	dpi := int32(96)
	sw, _, _ := user32GetSystemMetrics.Call(0)
	sh, _, _ := user32GetSystemMetrics.Call(1)

	sc := func(v int32) uintptr { return uintptr(v * dpi / 96) }
	winW, winH := int32(600), int32(385)
	// Create initially at logical size then re-measure DPI if monitor > 96 DPI.
	x := (int32(sw) - winW) / 2
	y := (int32(sh) - winH) / 2
	if x < 0 {
		x = 100
	}
	if y < 0 {
		y = 100
	}

	s := &setupState{hinst: hinst, bgBrush: bgBrush}
	appSetup = s
	s.hwnd, _, _ = user32CreateWindowExW.Call(
		0, uintptr(unsafe.Pointer(clsName)), uintptr(unsafe.Pointer(titleStr)),
		wsOverlapped, uintptr(x), uintptr(y), uintptr(winW), uintptr(winH), 0, 0, uintptr(hinst), 0,
	)
	if s.hwnd == 0 {
		return
	}
	if user32GetDpiForWindow.Find() == nil {
		if d, _, _ := user32GetDpiForWindow.Call(s.hwnd); d >= 96 {
			dpi = int32(d)
		}
	}
	if dpi != 96 {
		winW = 600 * dpi / 96
		winH = 385 * dpi / 96
		x = (int32(sw) - winW) / 2
		y = (int32(sh) - winH) / 2
		_, _, _ = user32.NewProc("SetWindowPos").Call(s.hwnd, 0, uintptr(x), uintptr(y), uintptr(winW), uintptr(winH), 0x0004)
	}

	s.fontTitle = makeFont(20*dpi/96, true)
	s.fontBody = makeFont(15*dpi/96, false)
	s.fontNote = makeFont(13*dpi/96, false)
	defer gdi32DeleteObject.Call(s.fontTitle)
	defer gdi32DeleteObject.Call(s.fontBody)
	defer gdi32DeleteObject.Call(s.fontNote)
	defer gdi32DeleteObject.Call(bgBrush)

	home, _ := os.UserHomeDir()
	initInstall := defaultInstallDir(os.Getenv("LOCALAPPDATA"), home)
	initDL := defaultDownloadDir(knownFolder(folderIDDownloads), knownFolder(folderIDVideos), home, initInstall)

	ctrl("STATIC", "延河课堂在线播放器 · 安装向导", 0, 0, sc(24), sc(20), sc(520), sc(28), s.hwnd, idLblTitle, s.fontTitle, hinst)
	ctrl("STATIC", "独立桌面窗口版（v"+appVersion+"）· 窗内登录、双画同步、自由目录下载", 0, 0, sc(24), sc(50), sc(520), sc(20), s.hwnd, idLblSub, s.fontNote, hinst)

	ctrl("STATIC", "1. 软件安装路径：", 0, 0, sc(24), sc(84), sc(520), sc(20), s.hwnd, 0, s.fontBody, hinst)
	s.editInstall = ctrl("EDIT", initInstall, wsTabStop|wsBorder|esAutoHScroll, 0, sc(24), sc(108), sc(424), sc(26), s.hwnd, idEditInstall, s.fontBody, hinst)
	ctrl("BUTTON", "浏览...", wsTabStop|bsPushButton, 0, sc(456), sc(107), sc(92), sc(28), s.hwnd, idBtnInstallDir, s.fontBody, hinst)

	ctrl("STATIC", "2. 默认视频下载路径：", 0, 0, sc(24), sc(148), sc(520), sc(20), s.hwnd, 0, s.fontBody, hinst)
	s.editDownload = ctrl("EDIT", initDL, wsTabStop|wsBorder|esAutoHScroll, 0, sc(24), sc(172), sc(424), sc(26), s.hwnd, idEditDownload, s.fontBody, hinst)
	ctrl("BUTTON", "浏览...", wsTabStop|bsPushButton, 0, sc(456), sc(171), sc(92), sc(28), s.hwnd, idBtnDownloadDir, s.fontBody, hinst)
	ctrl("STATIC", "注：安装完成后，随时可在播放器左侧「⚙ 设置」中修改默认下载路径及其他播放偏好。", 0, 0, sc(24), sc(204), sc(526), sc(20), s.hwnd, idLblNote, s.fontNote, hinst)

	s.chkDesktop = ctrl("BUTTON", "创建桌面与开始菜单快捷方式", wsTabStop|bsAutoCheckBox, 0, sc(24), sc(234), sc(260), sc(22), s.hwnd, idChkDesktop, s.fontBody, hinst)
	_, _, _ = user32SendMessageW.Call(s.chkDesktop, bmSetCheck, 1, 0)
	s.chkLaunch = ctrl("BUTTON", "安装完成后立即启动 延河课堂在线播放器", wsTabStop|bsAutoCheckBox, 0, sc(24), sc(260), sc(310), sc(22), s.hwnd, idChkLaunch, s.fontBody, hinst)
	_, _, _ = user32SendMessageW.Call(s.chkLaunch, bmSetCheck, 1, 0)

	s.lblStatus = ctrl("STATIC", "就绪", 0, 0, sc(24), sc(298), sc(300), sc(20), s.hwnd, idLblStatus, s.fontNote, hinst)
	s.btnGo = ctrl("BUTTON", "立即安装", wsTabStop|bsDefPushButton, 0, sc(356), sc(292), sc(96), sc(30), s.hwnd, idBtnGo, s.fontBody, hinst)
	s.btnCancel = ctrl("BUTTON", "取消", wsTabStop|bsPushButton, 0, sc(460), sc(292), sc(88), sc(30), s.hwnd, idBtnCancel, s.fontBody, hinst)

	_, _, _ = user32ShowWindow.Call(s.hwnd, 5)
	_, _, _ = user32UpdateWindow.Call(s.hwnd)

	var m winMsg
	for {
		r, _, _ := user32GetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		if d, _, _ := user32IsDialogMessageW.Call(s.hwnd, uintptr(unsafe.Pointer(&m))); d != 0 {
			continue
		}
		_, _, _ = user32TranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		_, _, _ = user32DispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}
