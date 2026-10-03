package main

// Audio capture via WASAPI (desktop = loopback of the default output device,
// mic = default input device). Each source is converted by Windows to
// 48 kHz stereo float and streamed to ffmpeg through a named pipe.
//
// Sync: every sample is placed on the timeline by its QPC capture time
// relative to t0, and gaps (loopback goes quiet when nothing plays) are filled
// with silence, so sample N always means t0 + N/48000 s.

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	audioRate     = 48000
	audioChannels = 2
	frameBytes    = 4 * audioChannels

	clsctxAll            = 0x17
	eRender              = 0
	eCapture             = 1
	eConsole             = 0
	streamLoopback       = 0x00020000
	streamAutoConvertPCM = 0x80000000
	streamSRCDefault     = 0x08000000
	bufferFlagSilent     = 0x2
	eNotFound            = 0x80070490
	eAccessDenied        = 0x80070005
)

var (
	ole32                 = windows.NewLazySystemDLL("ole32.dll")
	pCoInitializeEx       = ole32.NewProc("CoInitializeEx")
	pCoUninitialize       = ole32.NewProc("CoUninitialize")
	pCoCreateInstance     = ole32.NewProc("CoCreateInstance")
	pQPC                  = kernel32.NewProc("QueryPerformanceCounter")
	pQPF                  = kernel32.NewProc("QueryPerformanceFrequency")
	pGetSystemTimePrecise = kernel32.NewProc("GetSystemTimePreciseAsFileTime")

	clsidMMDeviceEnumerator = mustGUID("{BCDE0395-E52F-467C-8E3D-C4579291692E}")
	iidIMMDeviceEnumerator  = mustGUID("{A95664D2-9614-4F35-A746-DE8DB63617E6}")
	iidIAudioClient         = mustGUID("{1CB9AD4C-DBFA-4C32-B178-C2F568A703B2}")
	iidIAudioCaptureClient  = mustGUID("{C8ADBD64-E71E-48A0-A4DE-185C395CD317}")
)

func mustGUID(s string) windows.GUID {
	g, err := windows.GUIDFromString(s)
	if err != nil {
		panic(err)
	}
	return g
}

// comObj is a raw COM interface pointer; call(i) invokes vtable slot i.
type comObj uintptr

func (o comObj) call(i int, args ...uintptr) uint32 {
	vtbl := *(*uintptr)(unsafe.Pointer(o))
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(i)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(o)}, args...)...)
	return uint32(r)
}

func (o comObj) release() {
	if o != 0 {
		o.call(2)
	}
}

func failed(hr uint32) bool { return int32(hr) < 0 }

type waveFormatEx struct {
	FormatTag      uint16
	Channels       uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	Size           uint16
}

// clock is a moment expressed in both clocks we need: QPC in 100 ns units
// (what WASAPI stamps packets with) and Unix microseconds (ffmpeg's RTCTIME).
type clock struct {
	qpc100ns int64
	unixUs   int64
}

func now() clock {
	var c, f int64
	var ft windows.Filetime
	pQPC.Call(uintptr(unsafe.Pointer(&c)))
	pGetSystemTimePrecise.Call(uintptr(unsafe.Pointer(&ft)))
	pQPF.Call(uintptr(unsafe.Pointer(&f)))
	return clock{
		qpc100ns: c/f*10_000_000 + c%f*10_000_000/f,
		unixUs:   (ft.Nanoseconds() / 1000),
	}
}

// wasapi is one opened capture stream.
type wasapi struct {
	client  comObj
	capture comObj
}

func (w *wasapi) close() {
	if w.client != 0 {
		w.client.call(11) // Stop
	}
	w.capture.release()
	w.client.release()
	w.client, w.capture = 0, 0
}

// openWASAPI opens the default device. Must run on a COM-initialized thread.
func openWASAPI(loopback bool) (*wasapi, error) {
	var enum comObj
	hr, _, _ := pCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)), uintptr(unsafe.Pointer(&enum)))
	if failed(uint32(hr)) {
		return nil, fmt.Errorf("audio system unavailable (0x%08X)", uint32(hr))
	}
	defer enum.release()

	flow := uintptr(eCapture)
	if loopback {
		flow = eRender
	}
	var dev comObj
	if h := enum.call(4, flow, eConsole, uintptr(unsafe.Pointer(&dev))); failed(h) {
		if h == eNotFound {
			return nil, fmt.Errorf("no device found")
		}
		return nil, fmt.Errorf("no default device (0x%08X)", h)
	}
	defer dev.release()

	w := &wasapi{}
	if h := dev.call(3, uintptr(unsafe.Pointer(&iidIAudioClient)), clsctxAll, 0, uintptr(unsafe.Pointer(&w.client))); failed(h) {
		if h == eAccessDenied {
			return nil, fmt.Errorf("blocked by Windows privacy settings (Settings → Privacy → Microphone)")
		}
		return nil, fmt.Errorf("can't open device (0x%08X)", h)
	}
	wfx := waveFormatEx{
		FormatTag: 3, // IEEE float
		Channels:  audioChannels, SamplesPerSec: audioRate,
		AvgBytesPerSec: audioRate * frameBytes, BlockAlign: frameBytes, BitsPerSample: 32,
	}
	flags := uintptr(streamAutoConvertPCM | streamSRCDefault)
	if loopback {
		flags |= streamLoopback
	}
	const bufferDuration = 2_000_000 // 200 ms in 100 ns units
	if h := w.client.call(3, 0 /*shared*/, flags, bufferDuration, 0, uintptr(unsafe.Pointer(&wfx)), 0); failed(h) {
		if h == eAccessDenied {
			w.close()
			return nil, fmt.Errorf("blocked by Windows privacy settings (Settings → Privacy → Microphone)")
		}
		w.close()
		return nil, fmt.Errorf("can't start device (0x%08X)", h)
	}
	if h := w.client.call(14, uintptr(unsafe.Pointer(&iidIAudioCaptureClient)), uintptr(unsafe.Pointer(&w.capture))); failed(h) {
		w.close()
		return nil, fmt.Errorf("can't capture from device (0x%08X)", h)
	}
	if h := w.client.call(10); failed(h) { // Start
		w.close()
		return nil, fmt.Errorf("can't start capture (0x%08X)", h)
	}
	return w, nil
}

// audioSource captures one device into a queue that a pipe writer drains.
type audioSource struct {
	label    string
	loopback bool
	pipeName string
	t0       clock

	pipe      windows.Handle
	q         byteQueue
	written   int64 // frames placed on the timeline so far
	stop      chan struct{}
	done      sync.WaitGroup
	connected atomic.Bool
}

func newAudioSource(label string, loopback bool, t0 clock, n int) (*audioSource, error) {
	s := &audioSource{
		label:    label,
		loopback: loopback,
		t0:       t0,
		pipeName: fmt.Sprintf(`\\.\pipe\rec-%d-%d-%d`, windows.GetCurrentProcessId(), time.Now().UnixNano(), n),
		stop:     make(chan struct{}),
	}
	h, err := windows.CreateNamedPipe(u16(s.pipeName), windows.PIPE_ACCESS_OUTBOUND,
		windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT, 1, 1<<20, 0, 0, nil)
	if err != nil {
		return nil, err
	}
	s.pipe = h

	// Open the device synchronously so failures (no mic, privacy block) are
	// reported before ffmpeg starts.
	opened := make(chan error, 1)
	s.done.Add(2)
	go s.captureLoop(opened)
	if err := <-opened; err != nil {
		close(s.stop)
		s.done.Done() // writer never started
		s.done.Wait()
		windows.CloseHandle(s.pipe)
		return nil, err
	}
	go s.writeLoop()
	return s, nil
}

func (s *audioSource) frameAt(qpc100ns int64) int64 {
	d := qpc100ns - s.t0.qpc100ns
	return d/10_000_000*audioRate + d%10_000_000*audioRate/10_000_000
}

func (s *audioSource) silence(frames int64) {
	for frames > 0 {
		n := min(frames, audioRate/10)
		s.q.push(make([]byte, n*frameBytes))
		s.written += n
		frames -= n
	}
}

func (s *audioSource) captureLoop(opened chan<- error) {
	defer s.done.Done()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pCoInitializeEx.Call(0, 0 /*COINIT_MULTITHREADED*/)
	defer pCoUninitialize.Call()

	w, err := openWASAPI(s.loopback)
	opened <- err
	if err != nil {
		return
	}
	defer func() { w.close() }()

	const tolerance = audioRate / 50 // 20 ms: smaller drift is left alone
	lastData := time.Now()
	lastRetry := time.Now()
	for {
		select {
		case <-s.stop:
			return
		default:
		}

		if w.capture == 0 && time.Since(lastRetry) > time.Second {
			// Device went away (unplugged, default changed); follow the new default.
			lastRetry = time.Now()
			if nw, err := openWASAPI(s.loopback); err == nil {
				w = nw
			}
		}

		for w.capture != 0 {
			var n uint32
			if h := w.capture.call(5, uintptr(unsafe.Pointer(&n))); failed(h) {
				w.close()
				break
			}
			if n == 0 {
				break
			}
			var data uintptr
			var frames, flags uint32
			var devPos, qpcPos uint64
			if h := w.capture.call(3, uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&frames)),
				uintptr(unsafe.Pointer(&flags)), uintptr(unsafe.Pointer(&devPos)), uintptr(unsafe.Pointer(&qpcPos))); failed(h) {
				w.close()
				break
			}
			pos := s.frameAt(int64(qpcPos))
			skip := int64(0)
			switch {
			case pos > s.written+tolerance:
				s.silence(pos - s.written)
			case pos < s.written-tolerance:
				skip = min(s.written-pos, int64(frames))
			}
			if keep := int64(frames) - skip; keep > 0 {
				buf := make([]byte, keep*frameBytes)
				if flags&bufferFlagSilent == 0 && data != 0 {
					src := unsafe.Slice((*byte)(unsafe.Pointer(data)), int64(frames)*frameBytes)
					copy(buf, src[skip*frameBytes:])
				}
				s.q.push(buf)
				s.written += keep
			}
			w.capture.call(4, uintptr(frames)) // ReleaseBuffer
			lastData = time.Now()
		}

		// Nothing arriving (silence on loopback, or device gone): keep the
		// timeline moving, staying 100 ms behind real time so late packets still fit.
		if time.Since(lastData) > 100*time.Millisecond {
			if target := s.frameAt(now().qpc100ns) - audioRate/10; target > s.written {
				s.silence(target - s.written)
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (s *audioSource) writeLoop() {
	defer s.done.Done()
	err := windows.ConnectNamedPipe(s.pipe, nil)
	s.connected.Store(true)
	if err != nil && err != windows.ERROR_PIPE_CONNECTED {
		return
	}
	for {
		b, ok := s.q.pop()
		if !ok {
			return
		}
		for len(b) > 0 {
			var n uint32
			if err := windows.WriteFile(s.pipe, b, &n, nil); err != nil {
				s.q.close() // ffmpeg is gone
				return
			}
			b = b[n:]
		}
	}
}

// shutdown stops capture and unblocks the writer whatever state it is in.
func (s *audioSource) shutdown() {
	close(s.stop)
	s.q.close()
	if !s.connected.Load() {
		// Writer is parked in ConnectNamedPipe; satisfy it with a dummy client.
		if h, err := windows.CreateFile(u16(s.pipeName), windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, 0, 0); err == nil {
			windows.CloseHandle(h)
		}
	}
	windows.DisconnectNamedPipe(s.pipe) // breaks a blocked WriteFile
	s.done.Wait()
	windows.CloseHandle(s.pipe)
}

// byteQueue is an unbounded FIFO; push never blocks the capture thread.
type byteQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  [][]byte
	closed bool
}

func (q *byteQueue) init() {
	if q.cond == nil {
		q.cond = sync.NewCond(&q.mu)
	}
}

func (q *byteQueue) push(b []byte) {
	q.mu.Lock()
	q.init()
	if !q.closed {
		q.items = append(q.items, b)
		q.cond.Signal()
	}
	q.mu.Unlock()
}

func (q *byteQueue) pop() ([]byte, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.init()
	for len(q.items) == 0 && !q.closed {
		q.cond.Wait()
	}
	if q.closed {
		return nil, false
	}
	b := q.items[0]
	q.items[0] = nil
	q.items = q.items[1:]
	return b, true
}

func (q *byteQueue) close() {
	q.mu.Lock()
	q.init()
	q.closed = true
	q.items = nil
	q.cond.Broadcast()
	q.mu.Unlock()
}
