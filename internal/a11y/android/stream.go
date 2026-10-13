package android

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	goa11y "github.com/easydev-org/easyworker/internal/a11y"
)

// ScreenrecordNAL streams H.264 Annex-B access units produced by Android's
// native `screenrecord --output-format=h264 -` (hardware encoder, stdout).
// This is the fastest path to a WebRTC-ready H.264 stream on Android.
type ScreenrecordNAL struct {
	fps     int
	width   int
	height  int
	bitrate int
	cmd     *exec.Cmd
	cancel  context.CancelFunc
}

// NewNALSource creates a screenrecord-backed H.264 NAL source.
func NewNALSource(fps, width, height, bitrate int) *ScreenrecordNAL {
	if fps <= 0 {
		fps = 10
	}
	return &ScreenrecordNAL{fps: fps, width: width, height: height, bitrate: bitrate}
}

// NewNALSourceOrNil returns the Android NAL source (always available).
func NewNALSourceOrNil() goa11y.NALSource { return NewNALSource(10, 0, 0, 8000000) }

func (s *ScreenrecordNAL) Codec() goa11y.VideoCodec { return goa11y.CodecH264 }
func (s *ScreenrecordNAL) FPS() int                 { return s.fps }

func (s *ScreenrecordNAL) NALs(ctx context.Context) (<-chan []byte, error) {
	cctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	args := []string{"--output-format=h264", "--time-limit", "180"}
	if s.width > 0 && s.height > 0 {
		args = append(args, "--size", fmt.Sprintf("%dx%d", s.width, s.height))
	}
	br := s.bitrate
	if br <= 0 {
		br = 8000000
	}
	args = append(args, "--bit-rate", fmt.Sprintf("%d", br))
	args = append(args, "-") // stdout

	cmd := exec.CommandContext(cctx, "screenrecord", args...)
	s.cmd = cmd
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, goa11y.PlatformErr(-1, "screenrecord stdout: "+err.Error())
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, goa11y.PlatformErr(-1, "screenrecord start: "+err.Error())
	}
	out := make(chan []byte, 8)
	go func() {
		defer close(out)
		SplitAnnexB(cctx, stdout, out)
		cmd.Wait()
	}()
	return out, nil
}

func (s *ScreenrecordNAL) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

// SplitAnnexB reads an Annex-B H.264 byte stream and emits access units. An AU
// starts at an AUD (type 9) or a SPS (type 7); everything up to the next
// boundary is one AU.
func SplitAnnexB(ctx context.Context, r io.Reader, out chan<- []byte) {
	br := bufio.NewReaderSize(r, 1<<20)
	var buf []byte
	tmp := make([]byte, 64*1024)
	// screenrecord is change-driven: on a static screen the LAST access unit
	// (often the first IDR) stays buffered because extractAUs only splits at the
	// NEXT NAL. A background ticker flushes the pending AU after a short idle so
	// the first frame always reaches the viewer.
	var mu sync.Mutex
	flush := func() {
		mu.Lock()
		au := buf
		buf = nil
		mu.Unlock()
		if len(au) == 0 {
			return
		}
		select {
		case out <- au:
		case <-ctx.Done():
		}
	}
	go func() {
		t := time.NewTicker(120 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				flush()
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		n, err := br.Read(tmp)
		if n > 0 {
			mu.Lock()
			buf = append(buf, tmp[:n]...)
			local := buf
			mu.Unlock()
			// Emit complete AUs: scan for start codes, split at AU boundaries.
			for {
				aus, rest := extractAUs(local)
				if len(aus) == 0 {
					break
				}
				for _, au := range aus {
					select {
					case out <- au:
					case <-ctx.Done():
						return
					}
				}
				local = rest
			}
			mu.Lock()
			buf = local
			// Avoid unbounded growth.
			if len(buf) > 8<<20 {
				buf = buf[len(buf)-1<<20:]
			}
			mu.Unlock()
		}
		if err != nil {
			flush()
			return
		}
	}
}

// extractAUs splits a buffer into complete access units, returning the leftover.
//
// An H.264 access unit is one coded picture: the leading parameter NALs
// (SPS/PPS/SEI/AUD) plus ONE VCL slice (type 1 = non-IDR, type 5 = IDR). The
// guest `screenrecord` emits exactly one slice per frame and only repeats
// SPS/PPS at keyframes, so we split at every VCL NAL — NOT at AUD/SPS (which
// appear only at the stream start). Splitting at AUD/SPS batched all the
// P-frames between two keyframes into one giant AU, which broke the per-frame
// RTP timing and rendered a black/frozen video.
func extractAUs(buf []byte) ([][]byte, []byte) {
	// Start-code offsets (3- or 4-byte).
	var starts []int
	i := 0
	for i+3 < len(buf) {
		if buf[i] == 0 && buf[i+1] == 0 && buf[i+2] == 1 {
			starts = append(starts, i)
			i += 3
			continue
		}
		if i+4 < len(buf) && buf[i] == 0 && buf[i+1] == 0 && buf[i+2] == 0 && buf[i+3] == 1 {
			starts = append(starts, i)
			i += 4
			continue
		}
		i++
	}
	if len(starts) < 2 {
		return nil, buf
	}
	nalType := func(k int) byte {
		off := starts[k] + 3
		if buf[starts[k]+2] == 0 {
			off = starts[k] + 4
		}
		if off >= len(buf) {
			return 0
		}
		return buf[off] & 0x1f
	}
	isVCL := func(t byte) bool { return t == 1 || t == 5 }
	// AU boundaries: NAL 0 always starts one; any NAL that FOLLOWS a VCL NAL
	// starts the next AU (one slice per frame).
	bounds := []int{0}
	for k := 1; k < len(starts); k++ {
		if isVCL(nalType(k - 1)) {
			bounds = append(bounds, k)
		}
	}
	var aus [][]byte
	for j := 0; j+1 < len(bounds); j++ {
		s := starts[bounds[j]]
		e := starts[bounds[j+1]]
		au := make([]byte, e-s)
		copy(au, buf[s:e])
		aus = append(aus, au)
	}
	return aus, buf[starts[bounds[len(bounds)-1]]:]
}
