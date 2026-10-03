package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const createNoWindow = 0x08000000

type encoder struct {
	label string
	args  []string
}

var x264 = encoder{"CPU (x264)", []string{"-c:v", "libx264", "-preset", "veryfast", "-crf", "20"}}

// probeEncoder picks the first hardware H.264 encoder that actually works on this
// machine (listing in -encoders is not enough: the driver/GPU must be present).
func probeEncoder(ff string) encoder {
	cands := []encoder{
		{"NVIDIA NVENC", []string{"-c:v", "h264_nvenc", "-preset", "p5", "-tune", "hq", "-rc", "vbr", "-cq", "23", "-b:v", "0"}},
		{"AMD AMF", []string{"-c:v", "h264_amf", "-quality", "balanced", "-rc", "cqp", "-qp_i", "20", "-qp_p", "22"}},
		{"Intel Quick Sync", []string{"-c:v", "h264_qsv", "-preset", "medium", "-global_quality", "23"}},
	}
	for _, e := range cands {
		args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=black:s=1280x720:r=30",
			"-frames:v", "10", "-pix_fmt", "yuv420p"}
		args = append(args, e.args...)
		args = append(args, "-f", "null", "-")
		cmd := hidden(exec.Command(ff, args...))
		if cmd.Run() == nil {
			return e
		}
	}
	return x264
}

func hidden(c *exec.Cmd) *exec.Cmd {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return c
}

func findFFmpeg() string {
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "ffmpeg.exe")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	return ""
}

// killJob makes every ffmpeg we start die with us, so a crash can't leave a
// headless recorder running forever.
var killJob = func() windows.Handle {
	j, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(j, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(j)
		return 0
	}
	return j
}()

type recorder struct {
	ff      string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	mkv     string
	mp4     string
	logf    *os.File
	tail    *tailBuf
	audio   []*audioSource
	exited  chan struct{}
	exitErr error
	stopped bool // user asked to stop (vs. ffmpeg dying on its own)
	mu      sync.Mutex
}

type recOpts struct {
	src          source
	pct, fps     int
	desktopAudio bool
	mic          bool
	dir, logPath string
}

type result struct {
	file string // final file, "" if nothing saved
	note string // non-error reason it ended (shown in the status line)
	err  error  // real failure worth a popup
}

// evenScaled mirrors the ffmpeg expression trunc(iw*s/2)*2 exactly.
func evenScaled(w, h, pct int) (int, int) {
	if pct >= 100 {
		return w &^ 1, h &^ 1
	}
	s := float64(pct) / 100
	return int(float64(w)*s/2) * 2, int(float64(h)*s/2) * 2
}

// videoChain builds the capture filter chain, labelled [v].
//
// gfxcapture timestamps start at 0 on the first frame, at an unknown wall time.
// setpts re-bases them onto t0 (the instant audio sample 0 represents) using
// the wall clock at arrival; the running minimum discards frames that sat in a
// queue, so the estimate converges on true capture time within a few frames.
// fps start_time=0 then holds the first frame back to t0 so both tracks start at 0.
func videoChain(src source, pct, fps int, t0us int64) string {
	var in string
	if src.hwnd != 0 {
		// scale_aspect: if the window is resized mid-recording, fit it instead of cropping.
		in = fmt.Sprintf("gfxcapture=hwnd=%d:max_framerate=%d:resize_mode=scale_aspect", src.hwnd, fps)
	} else {
		in = fmt.Sprintf("gfxcapture=hmonitor=%d:max_framerate=%d", src.hmon, fps)
	}
	rebase := fmt.Sprintf("setpts='st(1,RTCTIME-PTS*TB*1000000);st(0,if(eq(N,0),ld(1),min(ld(0),ld(1))));PTS+(ld(0)-%d)/(TB*1000000)'", t0us)
	// BT.709 limited range, tagged below, so colors match in every player.
	conv := "out_color_matrix=bt709:out_range=tv"
	var sz string
	if pct >= 100 {
		// Pixel-exact: drop at most one odd row/column instead of resampling.
		sz = "crop=trunc(iw/2)*2:trunc(ih/2)*2,scale=" + conv
	} else {
		s := strconv.FormatFloat(float64(pct)/100, 'f', 2, 64)
		sz = fmt.Sprintf("scale=w=trunc(iw*%s/2)*2:h=trunc(ih*%s/2)*2:flags=bicubic:%s", s, s, conv)
	}
	return fmt.Sprintf("%s,hwdownload,format=bgra,%s,fps=fps=%d:start_time=0,%s,format=yuv420p[v]", in, rebase, fps, sz)
}

// startRecording returns warnings for audio sources that couldn't be opened;
// the recording still starts without them.
func startRecording(ff string, enc encoder, o recOpts) (*recorder, []string, error) {
	if err := os.MkdirAll(o.dir, 0o755); err != nil {
		return nil, nil, err
	}
	base := filepath.Join(o.dir, "Rec "+time.Now().Format("2006-01-02 15-04-05"))
	r := &recorder{ff: ff, mkv: base + ".mkv", mp4: base + ".mp4", tail: &tailBuf{}, exited: make(chan struct{})}

	t0 := now()
	var warnings []string
	if o.desktopAudio {
		if s, err := newAudioSource("Desktop audio", true, t0, 0); err == nil {
			r.audio = append(r.audio, s)
		} else {
			warnings = append(warnings, "Desktop audio: "+err.Error())
		}
	}
	if o.mic {
		if s, err := newAudioSource("Microphone", false, t0, 1); err == nil {
			r.audio = append(r.audio, s)
		} else {
			warnings = append(warnings, "Microphone: "+err.Error())
		}
	}

	args := []string{"-hide_banner", "-y"}
	for _, s := range r.audio {
		args = append(args, "-thread_queue_size", "4096", "-f", "f32le",
			"-ar", strconv.Itoa(audioRate), "-ac", strconv.Itoa(audioChannels), "-i", s.pipeName)
	}
	graph := videoChain(o.src, o.pct, o.fps, t0.unixUs)
	switch len(r.audio) {
	case 1:
		graph += ";[0:a]anull[a]"
	case 2:
		// normalize=0: keep each source at its own level instead of halving both.
		graph += ";[0:a][1:a]amix=inputs=2:duration=longest:normalize=0[a]"
	}
	args = append(args, "-filter_complex", graph, "-map", "[v]")
	args = append(args, enc.args...)
	args = append(args, "-g", strconv.Itoa(o.fps*2),
		"-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709", "-color_range", "tv")
	if len(r.audio) > 0 {
		// -shortest: if the recorded window closes, end instead of recording audio forever.
		args = append(args, "-map", "[a]", "-c:a", "aac", "-b:a", "192k", "-shortest")
	}
	args = append(args, "-f", "matroska", r.mkv)

	fail := func(err error) (*recorder, []string, error) {
		for _, s := range r.audio {
			s.shutdown()
		}
		r.closeLog()
		return nil, warnings, err
	}
	r.cmd = hidden(exec.Command(ff, args...))
	var err error
	if r.stdin, err = r.cmd.StdinPipe(); err != nil {
		return fail(err)
	}
	if r.logf, err = os.Create(o.logPath); err == nil {
		fmt.Fprintf(r.logf, "ffmpeg %s\n\n", strings.Join(args, " "))
		r.cmd.Stderr = io.MultiWriter(r.logf, r.tail)
	} else {
		r.cmd.Stderr = r.tail
	}
	if err := r.cmd.Start(); err != nil {
		return fail(err)
	}
	if killJob != 0 {
		if ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(r.cmd.Process.Pid)); err == nil {
			windows.AssignProcessToJobObject(killJob, ph)
			windows.CloseHandle(ph)
		}
	}
	go func() {
		r.exitErr = r.cmd.Wait()
		for _, s := range r.audio {
			s.shutdown()
		}
		close(r.exited)
	}()
	return r, warnings, nil
}

// requestStop asks ffmpeg to finish cleanly ('q'), and kills it if it hangs.
func (r *recorder) requestStop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	r.stopped = true
	io.WriteString(r.stdin, "q")
	go func() {
		select {
		case <-r.exited:
		case <-time.After(10 * time.Second):
			r.cmd.Process.Kill()
		}
	}()
}

// wait blocks until ffmpeg exits, then turns the MKV into an MP4.
// MKV is recorded first because it stays playable even if ffmpeg is killed.
func (r *recorder) wait() result {
	<-r.exited
	r.stdin.Close()
	r.mu.Lock()
	userStop := r.stopped
	r.mu.Unlock()

	var why error
	note := ""
	switch {
	case r.exitErr != nil && !userStop:
		why = fmt.Errorf("ffmpeg stopped with an error:\n\n%s", r.tail.lastLines(8))
	case !userStop:
		note = "Source closed" // e.g. the recorded window was closed
	}
	r.closeLog()

	if fi, err := os.Stat(r.mkv); err != nil || fi.Size() == 0 {
		os.Remove(r.mkv)
		if why == nil {
			why = errors.New("nothing was recorded")
		}
		if r.exitErr != nil {
			why = fmt.Errorf("ffmpeg failed:\n\n%s", r.tail.lastLines(8))
		}
		return result{err: why}
	}

	rm := hidden(exec.Command(r.ff, "-hide_banner", "-loglevel", "error", "-y", "-i", r.mkv,
		"-map", "0", "-c", "copy", "-movflags", "+faststart", r.mp4))
	out, err := rm.CombinedOutput()
	if fi, serr := os.Stat(r.mp4); err != nil || serr != nil || fi.Size() == 0 {
		os.Remove(r.mp4)
		// Keep the MKV; it's a perfectly good video.
		return result{file: r.mkv, note: note, err: fmt.Errorf("saved as MKV (MP4 conversion failed: %s)", strings.TrimSpace(string(out)))}
	}
	os.Remove(r.mkv)
	return result{file: r.mp4, note: note, err: why}
}

func (r *recorder) closeLog() {
	if r.logf != nil {
		r.logf.Close()
		r.logf = nil
	}
}

// tailBuf keeps the last few KB of ffmpeg's stderr for error messages.
type tailBuf struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > 8192 {
		t.b = append([]byte(nil), t.b[len(t.b)-8192:]...)
	}
	return len(p), nil
}

func (t *tailBuf) lastLines(n int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	// ffmpeg progress lines end in \r; treat them as line breaks.
	s := strings.ReplaceAll(string(t.b), "\r", "\n")
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "frame=") {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
