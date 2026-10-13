//go:build linux

package wayland

import (
	"fmt"
	"os"
	"syscall"
)

const (
	scCaptureOutput = 0
	scFrameCopy     = 0
	scFrameDestroy  = 1
	scFrameBufferEvent = 0
	scFrameFlags       = 1
	scFrameReady       = 2
	scFrameFailed      = 3
	shmCreatePool       = 0
	shmPoolCreateBuffer = 0
	wlBufferDestroy     = 0
)

type ScreencopyFrame struct {
	Format uint32
	Width  uint32
	Height uint32
	Stride uint32
	Pixels []byte
}

type Screencopy struct {
	c       *Conn
	manager uint32
	output  uint32
	shm     uint32
}

func NewScreencopy() (*Screencopy, error) {
	c, err := Dial()
	if err != nil {
		return nil, err
	}
	if _, err := c.GetRegistry(); err != nil {
		return nil, err
	}
	if err := c.Roundtrip(); err != nil {
		return nil, err
	}
	mgrG, ok := c.FindGlobal("zwlr_screencopy_manager_v1")
	if !ok {
		return nil, fmt.Errorf("no screencopy manager")
	}
	outG, ok := c.FindGlobal("wl_output")
	if !ok {
		return nil, fmt.Errorf("no wl_output")
	}
	shmG, ok := c.FindGlobal("wl_shm")
	if !ok {
		return nil, fmt.Errorf("no wl_shm")
	}
	mgr, err := c.Bind(c.registryID, mgrG.Name, 1, "zwlr_screencopy_manager_v1")
	if err != nil {
		return nil, err
	}
	output, err := c.Bind(c.registryID, outG.Name, 3, "wl_output")
	if err != nil {
		return nil, err
	}
	shm, err := c.Bind(c.registryID, shmG.Name, 1, "wl_shm")
	if err != nil {
		return nil, err
	}
	// consume wl_output's initial events (geometry/mode/scale/done) and shm format
	if err := c.Roundtrip(); err != nil {
		return nil, err
	}
	return &Screencopy{c: c, manager: mgr, output: output, shm: shm}, nil
}

func (s *Screencopy) Close() error { return s.c.Close() }
func (s *Screencopy) Name() string { return "wayland-screencopy" }

func (s *Screencopy) Capture() (*ScreencopyFrame, error) {
	frame := s.c.allocID("zwlr_screencopy_frame_v1")
	var e encoder
	e.i32(0)         // overlay_cursor (int)
	e.u32(s.output)  // output (object)
	e.u32(frame)     // frame (new_id)
	if err := s.c.send(s.manager, scCaptureOutput, &e); err != nil {
		return nil, err
	}
	format, width, height, stride, err := s.waitBuffer(frame)
	if err != nil {
		return nil, err
	}
	size := int(stride) * int(height)
	mem, err := createShm(size)
	if err != nil {
		return nil, err
	}
	pool := s.c.allocID("wl_shm_pool")
	var ep encoder
	ep.fd(mem)
	ep.u32(uint32(size))
	ep.u32(pool)
	if err := s.c.send(s.shm, shmCreatePool, &ep); err != nil {
		syscall.Close(mem)
		return nil, err
	}
	bufObj := s.c.allocID("wl_buffer")
	var eb encoder
	eb.u32(bufObj)
	eb.i32(0)
	eb.i32(int32(width))
	eb.i32(int32(height))
	eb.i32(int32(stride))
	eb.u32(format)
	if err := s.c.send(pool, shmPoolCreateBuffer, &eb); err != nil {
		syscall.Close(mem)
		return nil, err
	}
	var ec encoder
	ec.u32(bufObj)
	if err := s.c.send(frame, scFrameCopy, &ec); err != nil {
		syscall.Close(mem)
		return nil, err
	}
	if err := s.waitReady(frame); err != nil {
		syscall.Close(mem)
		return nil, err
	}
	pixels := make([]byte, size)
	f := os.NewFile(uintptr(mem), "shm")
	if f != nil {
		f.ReadAt(pixels, 0)
		f.Close()
	} else {
		syscall.Close(mem)
	}
	var ed encoder
	s.c.send(bufObj, wlBufferDestroy, &ed)
	var ef encoder
	s.c.send(frame, scFrameDestroy, &ef)
	return &ScreencopyFrame{Format: format, Width: width, Height: height, Stride: stride, Pixels: pixels}, nil
}

func (s *Screencopy) waitBuffer(frame uint32) (uint32, uint32, uint32, uint32, error) {
	for {
		ev, e := s.c.recvOne()
		if e != nil {
			return 0, 0, 0, 0, e
		}
		if ev.objectID != frame {
			s.c.dispatch(ev)
			continue
		}
		switch ev.opcode {
		case scFrameBufferEvent:
			format, _ := decU32(ev.args, 0)
			w, off := decU32(ev.args, 4)
			h, off := decU32(ev.args, off)
			stride, _ := decU32(ev.args, off)
			return format, w, h, stride, nil
		case scFrameFailed:
			return 0, 0, 0, 0, fmt.Errorf("screencopy frame failed")
		}
	}
}

func (s *Screencopy) waitReady(frame uint32) error {
	for {
		ev, e := s.c.recvOne()
		if e != nil {
			return e
		}
		if ev.objectID != frame {
			s.c.dispatch(ev)
			continue
		}
		switch ev.opcode {
		case scFrameReady:
			return nil
		case scFrameFailed:
			return fmt.Errorf("screencopy copy failed")
		}
	}
}

func createShm(size int) (int, error) {
	fd, err := syscallMemfdCreate("goa11y-shm")
	if err != nil {
		return -1, err
	}
	if err := syscall.Ftruncate(fd, int64(size)); err != nil {
		syscall.Close(fd)
		return -1, err
	}
	return fd, nil
}
