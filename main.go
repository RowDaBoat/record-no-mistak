// Rec: a screen recorder with two choices — a window or a whole display.
// Capture is Windows.Graphics.Capture (via ffmpeg's gfxcapture), the same API
// OBS uses, so GPU-drawn windows (browsers, games) don't come out black.
package main

import (
	"encoding/json"
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
)

func init() { runtime.LockOSThread() }

const (
	idFull = 101 + iota
	idWindow
	idSource
	idFps
	idScaleLbl
	idScale
	idScalePct
	idOut
	idRecord
	idStatus
	idOpen
	idDesktop
	idMic
)

const (
	wmRecDone = wmApp + 1
	wmEncDone = wmApp + 2
	hotkeyID  = 1
	timerID   = 1
)

// Layout in 96-DPI units; scaled at runtime for the window's monitor.
var layout = map[int][4]int{
	idFull:     {16, 14, 110, 24},
	idWindow:   {130, 14, 100, 24},
	idFps:      {324, 13, 80, 200},
	idSource:   {16, 48, 388, 400},
	idScaleLbl: {16, 92, 44, 22},
	idScale:    {58, 86, 272, 32},
	idScalePct: {332, 92, 72, 22},
	idDesktop:  {16, 124, 130, 24},
	idMic:      {150, 124, 130, 24},
	idOut:      {16, 156, 388, 22},
	idRecord:   {16, 186, 388, 48},
	idStatus:   {16, 250, 282, 24},
	idOpen:     {304, 248, 100, 28},
}

const clientW, clientH = 420, 290

type settings struct {
	Window bool `json:"window"`
	Scale  int  `json:"scale"`
	FPS    int  `json:"fps"`
	// Audio sources.
	Desktop bool `json:"desktopAudio"`
	Mic     bool `json:"mic"`
}

type app struct {
	hwnd      uintptr
	dpi       int
	font      uintptr
	bigFont   uintptr
	ctl       map[int]uintptr
	textCache map[uintptr]string

	cfg     settings
	sources []source
	ff      string
	enc     encoder
	encOK   bool
	outDir  string
	logPath string

	rec      *recorder
	recStart time.Time
	saving   bool
	closing  bool
	lastFile string
	results  chan result
	hotkey   bool
}

var a = &app{ctl: map[int]uintptr{}, textCache: map[uintptr]string{}}

func main() {
	a.ff = findFFmpeg()
	if vids, err := windows.KnownFolderPath(windows.FOLDERID_Videos, 0); err == nil {
		a.outDir = filepath.Join(vids, "Rec")
	} else {
		home, _ := os.UserHomeDir()
		a.outDir = filepath.Join(home, "Videos", "Rec")
	}
	cfgDir, _ := os.UserConfigDir()
	cfgDir = filepath.Join(cfgDir, "Rec")
	os.MkdirAll(cfgDir, 0o755)
	a.logPath = filepath.Join(cfgDir, "ffmpeg.log")
	a.cfg = loadSettings(filepath.Join(cfgDir, "settings.json"))

	icc := [2]uint32{8, iccBarClasses | iccStandard}
	pInitCommonControlsEx.Call(uintptr(unsafe.Pointer(&icc)))

	createMainWindow()

	if a.ff == "" {
		setStatus("ffmpeg not found")
		msgBox(a.hwnd, "Rec needs ffmpeg.exe (version 8 or newer).\n\nPut ffmpeg.exe next to rec.exe, or install it:\n\nwinget install Gyan.FFmpeg", "Rec", mbIconError)
	} else {
		setStatus("Checking encoder…")
		go func() {
			a.enc = probeEncoder(a.ff)
			postMsg(a.hwnd, wmEncDone, 0, 0)
		}()
	}

	var m msg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		if d, _, _ := pIsDialogMessageW.Call(a.hwnd, uintptr(unsafe.Pointer(&m))); d != 0 {
			continue
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	saveSettings(filepath.Join(cfgDir, "settings.json"), a.cfg)
}

func createMainWindow() {
	inst, _, _ := pGetModuleHandleW.Call(0)
	cursor, _, _ := pLoadCursorW.Call(0, 32512)
	icon, _, _ := pLoadIconW.Call(0, 32512)
	cls := u16("RecMainWindow")
	wc := wndClassEx{
		WndProc:    windows.NewCallback(wndProc),
		Instance:   inst,
		Icon:       icon,
		IconSm:     icon,
		Cursor:     cursor,
		Background: colorWindow + 1,
		ClassName:  cls,
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	style := uintptr(wsOverlapped | wsCaption | wsSysMenu | wsMinimizeBox)
	h, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(u16("Rec"))), style,
		0x80000000, 0x80000000, 400, 300, 0, 0, inst, 0)
	a.hwnd = h

	child := func(id int, class, text string, style uintptr) {
		c, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(u16(class))), uintptr(unsafe.Pointer(u16(text))),
			wsChild|wsVisible|style, 0, 0, 10, 10, h, uintptr(id), inst, 0)
		a.ctl[id] = c
	}
	child(idFull, "BUTTON", "Full screen", bsAutoRadio|wsTabStop|wsGroup)
	child(idWindow, "BUTTON", "Window", bsAutoRadio)
	child(idFps, "COMBOBOX", "", cbsDropDownList|wsTabStop|wsVScroll|wsGroup)
	child(idSource, "COMBOBOX", "", cbsDropDownList|wsTabStop|wsVScroll|wsGroup)
	child(idScaleLbl, "STATIC", "Scale", ssLeft|ssCenterImage)
	child(idScale, "msctls_trackbar32", "", tbsAutoTicks|tbsBottom|wsTabStop|wsGroup)
	child(idScalePct, "STATIC", "", ssRight|ssCenterImage)
	child(idDesktop, "BUTTON", "Desktop audio", bsAutoCheckbox|wsTabStop|wsGroup)
	child(idMic, "BUTTON", "Microphone", bsAutoCheckbox|wsTabStop)
	sendMsg(a.ctl[idDesktop], bmSetCheck, boolArg(a.cfg.Desktop), 0)
	sendMsg(a.ctl[idMic], bmSetCheck, boolArg(a.cfg.Mic), 0)
	child(idOut, "STATIC", "", ssLeft|ssCenterImage|ssNoPrefix|ssEndEllipsis)
	child(idRecord, "BUTTON", "", bsPushButton|wsTabStop|wsGroup)
	child(idStatus, "STATIC", "", ssLeft|ssCenterImage|ssNoPrefix|ssEndEllipsis)
	child(idOpen, "BUTTON", "Open folder", bsPushButton|wsTabStop)

	for _, s := range []string{"30 fps", "60 fps"} {
		sendMsg(a.ctl[idFps], cbAddString, 0, uintptr(unsafe.Pointer(u16(s))))
	}
	if a.cfg.FPS == 30 {
		sendMsg(a.ctl[idFps], cbSetCurSel, 0, 0)
	} else {
		sendMsg(a.ctl[idFps], cbSetCurSel, 1, 0)
	}

	tb := a.ctl[idScale]
	sendMsg(tb, tbmSetRange, 1, uintptr(10|100<<16))
	sendMsg(tb, tbmSetTicFreq, 10, 0)
	sendMsg(tb, tbmSetLineSize, 0, 5)
	sendMsg(tb, tbmSetPageSize, 0, 10)
	sendMsg(tb, tbmSetPos, 1, uintptr(a.cfg.Scale))

	// Keep Rec itself out of every recording (Windows 10 2004+; harmless if unsupported).
	// REC_SHOW_SELF=1 opts out (e.g. to screenshot Rec itself).
	if os.Getenv("REC_SHOW_SELF") == "" {
		pSetWindowDisplayAffinity.Call(h, wdaExcludeFromCapture)
	}

	if r, _, _ := pRegisterHotKey.Call(h, hotkeyID, modControl|modAlt|modNoRepeat, 'R'); r != 0 {
		a.hotkey = true
	}

	applyDPI(dpiFor(h))
	// Size the window to the client area we want, then show it.
	var rc = rect{0, 0, int32(px(clientW)), int32(px(clientH))}
	pAdjustWindowRectExForDpi.Call(uintptr(unsafe.Pointer(&rc)), style, 0, 0, uintptr(a.dpi))
	pSetWindowPos.Call(h, 0, 0, 0, uintptr(rc.R-rc.L), uintptr(rc.B-rc.T), swpNoMove|swpNoZOrder|swpNoActivate)

	setMode(a.cfg.Window)
	refreshUI()
	pSetTimer.Call(h, timerID, 250, 0)
	pShowWindow.Call(h, swShow)
	pUpdateWindow.Call(h)
}

func px(v int) int { return (v*a.dpi + 48) / 96 }

func applyDPI(dpi int) {
	a.dpi = dpi
	if a.font != 0 {
		pDeleteObject.Call(a.font)
		pDeleteObject.Call(a.bigFont)
	}
	mk := func(pt, weight int) uintptr {
		f, _, _ := pCreateFontW.Call(uintptr(-(pt*dpi+36)/72&0xffffffff), 0, 0, 0, uintptr(weight), 0, 0, 0,
			1, 0, 0, 5 /*CLEARTYPE*/, 0, uintptr(unsafe.Pointer(u16("Segoe UI"))))
		return f
	}
	a.font = mk(9, 400)
	a.bigFont = mk(11, 600)
	for id, c := range a.ctl {
		f := a.font
		if id == idRecord {
			f = a.bigFont
		}
		sendMsg(c, wmSetFont, f, 1)
		l := layout[id]
		pMoveWindow.Call(c, uintptr(px(l[0])), uintptr(px(l[1])), uintptr(px(l[2])), uintptr(px(l[3])), 1)
	}
	sendMsg(a.ctl[idSource], cbSetDroppedWidth, uintptr(px(560)), 0)
}

func wndProc(h, m, wp, lp uintptr) uintptr {
	switch uint32(m) {
	case wmCommand:
		id, code := int(wp&0xffff), int(wp>>16&0xffff)
		switch {
		case id == idFull && code == bnClicked:
			setMode(false)
		case id == idWindow && code == bnClicked:
			setMode(true)
		case id == idSource && code == cbnDropDown:
			fillSources()
		case id == idSource && code == cbnSelChange:
			refreshUI()
		case id == idFps && code == cbnSelChange:
			a.cfg.FPS = map[uintptr]int{0: 30, 1: 60}[sendMsg(a.ctl[idFps], cbGetCurSel, 0, 0)]
		case id == idDesktop && code == bnClicked:
			a.cfg.Desktop = sendMsg(a.ctl[idDesktop], bmGetCheck, 0, 0) == 1
		case id == idMic && code == bnClicked:
			a.cfg.Mic = sendMsg(a.ctl[idMic], bmGetCheck, 0, 0) == 1
		case id == idRecord && code == bnClicked:
			toggle()
		case id == idOpen && code == bnClicked:
			openFolder()
		}
		return 0

	case wmHScroll:
		if lp == a.ctl[idScale] {
			pos := int(sendMsg(lp, tbmGetPos, 0, 0))
			snapped := (pos + 2) / 5 * 5
			if wp&0xffff == tbEndTrack && snapped != pos {
				sendMsg(lp, tbmSetPos, 1, uintptr(snapped))
			}
			a.cfg.Scale = snapped
			refreshUI()
		}
		return 0

	case wmTimer:
		refreshUI()
		return 0

	case wmHotkey:
		if wp == hotkeyID {
			toggle()
		}
		return 0

	case wmEncDone:
		a.encOK = true
		refreshUI()
		return 0

	case wmRecDone:
		onRecordingDone()
		return 0

	case wmCtlColorStatic, wmCtlColorBtn:
		pSetBkColor.Call(wp, sysColor(colorWindow))
		if lp == a.ctl[idOut] {
			pSetTextColor.Call(wp, sysColor(colorGrayText))
		}
		return sysBrush(colorWindow)

	case wmDpiChanged:
		applyDPI(int(wp & 0xffff))
		r := (*rect)(unsafe.Pointer(lp))
		pSetWindowPos.Call(h, 0, uintptr(r.L), uintptr(r.T), uintptr(r.R-r.L), uintptr(r.B-r.T), swpNoZOrder|swpNoActivate)
		return 0

	case wmClose:
		if a.rec != nil || a.saving {
			a.closing = true
			if a.rec != nil && !a.saving {
				stop()
			}
			return 0
		}
		pDestroyWindow.Call(h)
		return 0

	case wmDestroy:
		if a.hotkey {
			pUnregisterHotKey.Call(h, hotkeyID)
		}
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, m, wp, lp)
	return r
}

func setMode(window bool) {
	a.cfg.Window = window
	sendMsg(a.ctl[idFull], bmSetCheck, boolArg(!window), 0)
	sendMsg(a.ctl[idWindow], bmSetCheck, boolArg(window), 0)
	fillSources()
	refreshUI()
}

// fillSources rebuilds the dropdown, keeping the current pick if it still exists.
// It runs every time the dropdown opens, so the list is never stale.
func fillSources() {
	var keep source
	if s, ok := selected(); ok {
		keep = s
	}
	if a.cfg.Window {
		a.sources = listWindows(a.hwnd)
	} else {
		a.sources = listMonitors()
	}
	cb := a.ctl[idSource]
	sendMsg(cb, cbResetContent, 0, 0)
	sel := 0
	for i, s := range a.sources {
		sendMsg(cb, cbAddString, 0, uintptr(unsafe.Pointer(u16(s.label))))
		if (keep.hwnd != 0 && s.hwnd == keep.hwnd) || (keep.hmon != 0 && s.hmon == keep.hmon) {
			sel = i
		}
	}
	if len(a.sources) > 0 {
		sendMsg(cb, cbSetCurSel, uintptr(sel), 0)
	}
}

func selected() (source, bool) {
	i := int(int32(sendMsg(a.ctl[idSource], cbGetCurSel, 0, 0)))
	if i < 0 || i >= len(a.sources) {
		return source{}, false
	}
	return a.sources[i], true
}

func set(id int, s string) {
	c := a.ctl[id]
	if a.textCache[c] != s {
		a.textCache[c] = s
		setText(c, s)
	}
}

func setStatus(s string) { set(idStatus, s) }

// refreshUI derives every label and enabled state from current state.
func refreshUI() {
	recording := a.rec != nil
	idle := !recording && !a.saving
	for _, id := range []int{idFull, idWindow, idSource, idFps, idScale, idDesktop, idMic} {
		enable(a.ctl[id], idle)
	}

	set(idScalePct, fmt.Sprintf("%d%%", a.cfg.Scale))
	src, ok := selected()
	switch {
	case !ok && a.cfg.Window:
		set(idOut, "No windows found — open the dropdown to refresh")
	case !ok:
		set(idOut, "No displays found")
	default:
		w, h := src.size()
		if w == 0 {
			set(idOut, "Window is minimized or closed — restore it to record")
		} else {
			ow, oh := evenScaled(w, h, a.cfg.Scale)
			set(idOut, fmt.Sprintf("Output  %d × %d   ·   source %d × %d", ow, oh, w, h))
		}
	}

	hk := ""
	if a.hotkey {
		hk = "      Ctrl+Alt+R"
	}
	switch {
	case a.saving:
		set(idRecord, "Saving…")
		setStatus("Finishing file…")
	case recording:
		d := time.Since(a.recStart).Round(time.Second)
		set(idRecord, fmt.Sprintf("■  Stop    %02d:%02d:%02d%s", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60, hk))
	default:
		set(idRecord, "●  Record"+hk)
	}
	enable(a.ctl[idRecord], !a.saving && a.ff != "" && a.encOK)
	if idle && a.encOK && a.textCache[a.ctl[idStatus]] == "Checking encoder…" {
		setStatus("Ready  ·  encoder: " + a.enc.label)
	}
}

func toggle() {
	switch {
	case a.saving || a.ff == "" || !a.encOK:
	case a.rec != nil:
		stop()
	default:
		start()
	}
}

func start() {
	src, ok := selected()
	if !ok {
		setStatus("Pick something to record first")
		return
	}
	if src.hwnd != 0 && (!isWindow(src.hwnd) || isIconic(src.hwnd)) {
		setStatus("That window is minimized or gone")
		fillSources()
		refreshUI()
		return
	}
	rec, warnings, err := startRecording(a.ff, a.enc, recOpts{
		src: src, pct: a.cfg.Scale, fps: a.cfg.FPS,
		desktopAudio: a.cfg.Desktop, mic: a.cfg.Mic,
		dir: a.outDir, logPath: a.logPath,
	})
	if err != nil {
		popup("Couldn't start recording:\n\n"+err.Error(), mbIconError)
		return
	}
	if len(warnings) > 0 {
		popup("Recording without:\n\n"+strings.Join(warnings, "\n"), mbIconWarning)
	}
	a.rec, a.recStart = rec, time.Now()
	a.results = make(chan result, 1)
	w, h := src.size()
	ow, oh := evenScaled(w, h, a.cfg.Scale)
	sound := "no sound"
	if n := len(rec.audio); n == 2 {
		sound = "desktop + mic"
	} else if n == 1 {
		sound = strings.ToLower(rec.audio[0].label)
	}
	setStatus(fmt.Sprintf("●  %d × %d @ %d  ·  %s", ow, oh, a.cfg.FPS, sound))
	go func() {
		a.results <- rec.wait()
		postMsg(a.hwnd, wmRecDone, 0, 0)
	}()
	refreshUI()
}

func stop() {
	a.saving = true
	a.rec.requestStop()
	refreshUI()
}

func onRecordingDone() {
	res := <-a.results
	a.rec, a.saving = nil, false
	prefix := ""
	if res.note != "" {
		prefix = res.note + "  ·  "
	}
	if res.file != "" {
		a.lastFile = res.file
		setStatus(prefix + "Saved  " + filepath.Base(res.file))
	} else {
		setStatus(prefix + "Not saved")
	}
	refreshUI()
	if a.closing {
		pDestroyWindow.Call(a.hwnd)
		return
	}
	if res.err != nil {
		icon := uintptr(mbIconWarning)
		if res.file == "" {
			icon = mbIconError
		}
		popup(res.err.Error()+"\n\nFull log: "+a.logPath, icon)
	}
}

func openFolder() {
	os.MkdirAll(a.outDir, 0o755)
	cmd := exec.Command("explorer.exe")
	if a.lastFile != "" {
		if _, err := os.Stat(a.lastFile); err == nil {
			// explorer needs the path quoted after the comma, which exec's quoting won't do.
			cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + a.lastFile + `"`}
			cmd.Start()
			return
		}
	}
	cmd.Args = append(cmd.Args, a.outDir)
	cmd.Start()
}

func loadSettings(path string) settings {
	s := settings{Scale: 100, FPS: 60, Desktop: true}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &s)
	}
	if s.Scale < 10 || s.Scale > 100 {
		s.Scale = 100
	}
	s.Scale = (s.Scale + 2) / 5 * 5
	if s.FPS != 30 && s.FPS != 60 {
		s.FPS = 60
	}
	return s
}

func saveSettings(path string, s settings) {
	if b, err := json.MarshalIndent(s, "", "  "); err == nil {
		os.WriteFile(path, b, 0o644)
	}
}
