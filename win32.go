package main

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	comctl32 = windows.NewLazySystemDLL("comctl32.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	pRegisterClassExW         = user32.NewProc("RegisterClassExW")
	pCreateWindowExW          = user32.NewProc("CreateWindowExW")
	pDefWindowProcW           = user32.NewProc("DefWindowProcW")
	pDestroyWindow            = user32.NewProc("DestroyWindow")
	pGetMessageW              = user32.NewProc("GetMessageW")
	pTranslateMessage         = user32.NewProc("TranslateMessage")
	pDispatchMessageW         = user32.NewProc("DispatchMessageW")
	pIsDialogMessageW         = user32.NewProc("IsDialogMessageW")
	pPostQuitMessage          = user32.NewProc("PostQuitMessage")
	pPostMessageW             = user32.NewProc("PostMessageW")
	pSendMessageW             = user32.NewProc("SendMessageW")
	pSetWindowTextW           = user32.NewProc("SetWindowTextW")
	pGetWindowTextW           = user32.NewProc("GetWindowTextW")
	pGetWindowTextLengthW     = user32.NewProc("GetWindowTextLengthW")
	pGetClassNameW            = user32.NewProc("GetClassNameW")
	pEnableWindow             = user32.NewProc("EnableWindow")
	pShowWindow               = user32.NewProc("ShowWindow")
	pUpdateWindow             = user32.NewProc("UpdateWindow")
	pSetWindowPos             = user32.NewProc("SetWindowPos")
	pMoveWindow               = user32.NewProc("MoveWindow")
	pEnumWindows              = user32.NewProc("EnumWindows")
	pIsWindow                 = user32.NewProc("IsWindow")
	pIsWindowVisible          = user32.NewProc("IsWindowVisible")
	pIsIconic                 = user32.NewProc("IsIconic")
	pGetWindowLongPtrW        = user32.NewProc("GetWindowLongPtrW")
	pGetWindow                = user32.NewProc("GetWindow")
	pGetClientRect            = user32.NewProc("GetClientRect")
	pGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
	pEnumDisplayMonitors      = user32.NewProc("EnumDisplayMonitors")
	pGetMonitorInfoW          = user32.NewProc("GetMonitorInfoW")
	pGetDpiForWindow          = user32.NewProc("GetDpiForWindow")
	pAdjustWindowRectExForDpi = user32.NewProc("AdjustWindowRectExForDpi")
	pSetTimer                 = user32.NewProc("SetTimer")
	pRegisterHotKey           = user32.NewProc("RegisterHotKey")
	pUnregisterHotKey         = user32.NewProc("UnregisterHotKey")
	pMessageBoxW              = user32.NewProc("MessageBoxW")
	pLoadCursorW              = user32.NewProc("LoadCursorW")
	pLoadIconW                = user32.NewProc("LoadIconW")
	pGetSysColor              = user32.NewProc("GetSysColor")
	pGetSysColorBrush         = user32.NewProc("GetSysColorBrush")
	pSetWindowDisplayAffinity = user32.NewProc("SetWindowDisplayAffinity")
	pCreateFontW              = gdi32.NewProc("CreateFontW")
	pDeleteObject             = gdi32.NewProc("DeleteObject")
	pSetBkColor               = gdi32.NewProc("SetBkColor")
	pSetTextColor             = gdi32.NewProc("SetTextColor")
	pInitCommonControlsEx     = comctl32.NewProc("InitCommonControlsEx")
	pDwmGetWindowAttribute    = dwmapi.NewProc("DwmGetWindowAttribute")
	pGetModuleHandleW         = kernel32.NewProc("GetModuleHandleW")
)

const (
	wsOverlapped  = 0x00000000
	wsCaption     = 0x00C00000
	wsSysMenu     = 0x00080000
	wsMinimizeBox = 0x00020000
	wsChild       = 0x40000000
	wsVisible     = 0x10000000
	wsTabStop     = 0x00010000
	wsGroup       = 0x00020000
	wsVScroll     = 0x00200000
	wsExToolWin   = 0x00000080
	wsExAppWin    = 0x00040000

	bsPushButton    = 0x0
	bsAutoRadio     = 0x9
	bsAutoCheckbox  = 0x3
	cbsDropDownList = 0x3
	ssLeft          = 0x0
	ssRight         = 0x2
	ssNoPrefix      = 0x80
	ssCenterImage   = 0x200
	ssEndEllipsis   = 0x4000
	tbsAutoTicks    = 0x1
	tbsBottom       = 0x0

	wmDestroy        = 0x0002
	wmClose          = 0x0010
	wmSetFont        = 0x0030
	wmCommand        = 0x0111
	wmTimer          = 0x0113
	wmHScroll        = 0x0114
	wmCtlColorBtn    = 0x0135
	wmCtlColorStatic = 0x0138
	wmHotkey         = 0x0312
	wmDpiChanged     = 0x02E0
	wmApp            = 0x8000

	bnClicked         = 0
	cbnSelChange      = 1
	cbnDropDown       = 7
	cbAddString       = 0x0143
	cbGetCurSel       = 0x0147
	cbResetContent    = 0x014B
	cbSetCurSel       = 0x014E
	cbSetDroppedWidth = 0x0160
	bmGetCheck        = 0x00F0
	bmSetCheck        = 0x00F1
	tbmGetPos         = 0x0400
	tbmSetPos         = 0x0405
	tbmSetRange       = 0x0406
	tbmSetTicFreq     = 0x0414
	tbmSetPageSize    = 0x0415
	tbmSetLineSize    = 0x0417
	tbEndTrack        = 8

	swShow                = 5
	swpNoZOrder           = 0x0004
	swpNoActivate         = 0x0010
	swpNoMove             = 0x0002
	gwOwner               = 4
	colorWindow           = 5
	colorGrayText         = 17
	modAlt                = 0x1
	modControl            = 0x2
	modNoRepeat           = 0x4000
	mbOK                  = 0x0
	mbIconError           = 0x10
	mbIconWarning         = 0x30
	mbIconInfo            = 0x40
	mbSetForeground       = 0x10000
	mbTopmost             = 0x40000
	dwmwaCloaked          = 14
	wdaExcludeFromCapture = 0x11
	monitorInfoPrimary    = 1
	iccBarClasses         = 0x4
	iccStandard           = 0x4000
)

type rect struct{ L, T, R, B int32 }

type point struct{ X, Y int32 }

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type monitorInfoEx struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
	Device  [32]uint16
}

func u16(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
}

func sendMsg(h uintptr, m uint32, w, l uintptr) uintptr {
	r, _, _ := pSendMessageW.Call(h, uintptr(m), w, l)
	return r
}

func postMsg(h uintptr, m uint32, w, l uintptr) {
	pPostMessageW.Call(h, uintptr(m), w, l)
}

func setText(h uintptr, s string) {
	pSetWindowTextW.Call(h, uintptr(unsafe.Pointer(u16(s))))
}

func windowText(h uintptr) string {
	n, _, _ := pGetWindowTextLengthW.Call(h)
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	pGetWindowTextW.Call(h, uintptr(unsafe.Pointer(&buf[0])), n+1)
	return windows.UTF16ToString(buf)
}

func className(h uintptr) string {
	buf := make([]uint16, 256)
	pGetClassNameW.Call(h, uintptr(unsafe.Pointer(&buf[0])), 256)
	return windows.UTF16ToString(buf)
}

func enable(h uintptr, on bool) {
	pEnableWindow.Call(h, boolArg(on))
}

func boolArg(b bool) uintptr {
	if b {
		return 1
	}
	return 0
}

func isWindow(h uintptr) bool  { r, _, _ := pIsWindow.Call(h); return r != 0 }
func isIconic(h uintptr) bool  { r, _, _ := pIsIconic.Call(h); return r != 0 }
func isVisible(h uintptr) bool { r, _, _ := pIsWindowVisible.Call(h); return r != 0 }

func clientSize(h uintptr) (int, int) {
	var r rect
	pGetClientRect.Call(h, uintptr(unsafe.Pointer(&r)))
	return int(r.R - r.L), int(r.B - r.T)
}

func exStyle(h uintptr) uintptr {
	idx := -20 // GWL_EXSTYLE
	r, _, _ := pGetWindowLongPtrW.Call(h, uintptr(idx))
	return r
}

func isCloaked(h uintptr) bool {
	var v uint32
	r, _, _ := pDwmGetWindowAttribute.Call(h, dwmwaCloaked, uintptr(unsafe.Pointer(&v)), 4)
	return r == 0 && v != 0
}

func dpiFor(h uintptr) int {
	r, _, _ := pGetDpiForWindow.Call(h)
	if r == 0 {
		return 96
	}
	return int(r)
}

func msgBox(owner uintptr, text, title string, flags uintptr) {
	pMessageBoxW.Call(owner, uintptr(unsafe.Pointer(u16(text))), uintptr(unsafe.Pointer(u16(title))), flags)
}

// popup shows a message box on its own OS thread. A modal box opened from
// inside the window procedure would re-enter it and doesn't show reliably.
func popup(text string, icon uintptr) {
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		msgBox(0, text, "Rec", icon|mbTopmost|mbSetForeground)
	}()
}

func sysColor(i uintptr) uintptr { r, _, _ := pGetSysColor.Call(i); return r }

func sysBrush(i uintptr) uintptr { r, _, _ := pGetSysColorBrush.Call(i); return r }
