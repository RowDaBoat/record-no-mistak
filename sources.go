package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// source is one capturable thing: a top-level window (hwnd) or a display (hmon).
type source struct {
	label string
	hwnd  uintptr
	hmon  uintptr
	monW  int
	monH  int
}

// size returns the pixel size gfxcapture will produce for this source right now.
// For windows that's the client area (gfxcapture strips the border by default).
func (s source) size() (int, int) {
	if s.hwnd != 0 {
		if !isWindow(s.hwnd) || isIconic(s.hwnd) {
			return 0, 0
		}
		return clientSize(s.hwnd)
	}
	return s.monW, s.monH
}

// Callbacks are created once: windows.NewCallback slots are never freed.
var (
	enumWindowsCB  = windows.NewCallback(enumWindowsProc)
	enumMonitorsCB = windows.NewCallback(enumMonitorsProc)
	enumWinOut     []uintptr
	enumMonOut     []source
)

func enumWindowsProc(h, _ uintptr) uintptr {
	enumWinOut = append(enumWinOut, h)
	return 1
}

func enumMonitorsProc(hmon, _, _, _ uintptr) uintptr {
	var mi monitorInfoEx
	mi.Size = uint32(unsafe.Sizeof(mi))
	if r, _, _ := pGetMonitorInfoW.Call(hmon, uintptr(unsafe.Pointer(&mi))); r == 0 {
		return 1
	}
	s := source{
		hmon: hmon,
		monW: int(mi.Monitor.R - mi.Monitor.L),
		monH: int(mi.Monitor.B - mi.Monitor.T),
	}
	// Stash position + primary flag in label for sorting; rewritten below.
	s.label = fmt.Sprintf("%d|%d|%d", mi.Monitor.L, mi.Monitor.T, mi.Flags&monitorInfoPrimary)
	enumMonOut = append(enumMonOut, s)
	return 1
}

func listMonitors() []source {
	enumMonOut = nil
	pEnumDisplayMonitors.Call(0, 0, enumMonitorsCB, 0)
	type keyed struct {
		s       source
		x, y    int
		primary bool
	}
	ks := make([]keyed, 0, len(enumMonOut))
	for _, s := range enumMonOut {
		var k keyed
		var p int
		fmt.Sscanf(s.label, "%d|%d|%d", &k.x, &k.y, &p)
		k.s, k.primary = s, p != 0
		ks = append(ks, k)
	}
	// Left-to-right, top-to-bottom: matches how people point at their desks.
	sort.Slice(ks, func(i, j int) bool {
		if ks[i].x != ks[j].x {
			return ks[i].x < ks[j].x
		}
		return ks[i].y < ks[j].y
	})
	out := make([]source, len(ks))
	for i, k := range ks {
		k.s.label = fmt.Sprintf("Display %d  —  %d × %d", i+1, k.s.monW, k.s.monH)
		if k.primary {
			k.s.label += "  (main)"
		}
		out[i] = k.s
	}
	return out
}

var skipClasses = map[string]bool{
	"Progman": true, "WorkerW": true, "Shell_TrayWnd": true, "Shell_SecondaryTrayWnd": true,
	"Windows.UI.Core.CoreWindow": true,
}

func listWindows(self uintptr) []source {
	enumWinOut = nil
	pEnumWindows.Call(enumWindowsCB, 0)
	var out []source
	for _, h := range enumWinOut {
		if h == self || !isVisible(h) || isCloaked(h) {
			continue
		}
		if owner, _, _ := pGetWindow.Call(h, gwOwner); owner != 0 {
			continue
		}
		ex := exStyle(h)
		if ex&wsExToolWin != 0 && ex&wsExAppWin == 0 {
			continue
		}
		if skipClasses[className(h)] {
			continue
		}
		title := strings.TrimSpace(windowText(h))
		if title == "" {
			continue
		}
		if !isIconic(h) {
			if w, hh := clientSize(h); w < 2 || hh < 2 {
				continue
			}
		}
		if r := []rune(title); len(r) > 70 {
			title = string(r[:69]) + "…"
		}
		label := title
		if exe := exeName(h); exe != "" {
			label += "   [" + exe + "]"
		}
		if isIconic(h) {
			label += "   (minimized)"
		}
		out = append(out, source{label: label, hwnd: h})
	}
	return out
}

func exeName(h uintptr) string {
	var pid uint32
	pGetWindowThreadProcessId.Call(h, uintptr(unsafe.Pointer(&pid)))
	ph, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(ph)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(ph, 0, &buf[0], &n); err != nil {
		return ""
	}
	return filepath.Base(windows.UTF16ToString(buf[:n]))
}
