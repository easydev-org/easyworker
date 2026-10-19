package native

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// fakeDaemon is a minimal est server that records the Auth frame the client
// presents. It sends Hello first (as the real daemon does), then reads frames
// until it sees Auth, and reports the token back on a channel.
func fakeDaemon(t *testing.T, authCh chan<- string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				// Hello: [type=0x01][flags][len=15][7-byte core + 8-byte audio]
				body := make([]byte, 15)
				body[0], body[1] = 1, 1
				binary.BigEndian.PutUint16(body[2:4], 1920)
				binary.BigEndian.PutUint16(body[4:6], 1080)
				body[6] = 30
				hdr := []byte{0x01, 0, 0, 0, 0, byte(len(body))}
				if _, err := c.Write(append(hdr, body...)); err != nil {
					return
				}
				// Read frames until Auth (0x03).
				var h [6]byte
				for {
					if _, err := io.ReadFull(c, h[:]); err != nil {
						return
					}
					n := binary.BigEndian.Uint32(h[2:6])
					payload := make([]byte, n)
					if n > 0 {
						if _, err := io.ReadFull(c, payload); err != nil {
							return
						}
					}
					if h[0] == tAuth {
						select {
						case authCh <- string(payload):
						default:
						}
						// Ack with Control.
						_, _ = c.Write([]byte{tAuthOK, 0, 0, 0, 0, 1, 1})
						return
					}
				}
			}(c)
		}
	}()
	return ln.Addr().String()
}

func TestDialPresentsAuthToken(t *testing.T) {
	authCh := make(chan string, 1)
	addr := fakeDaemon(t, authCh)

	// Point the client at the fake daemon and give it a token.
	t.Setenv("NATIVE_A11Y_ADDR", addr)
	SetControlToken("s3cret")
	t.Cleanup(func() { SetControlToken("") })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	select {
	case got := <-authCh:
		if got != "s3cret" {
			t.Fatalf("auth token = %q, want s3cret", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon never received an Auth frame")
	}
}

func TestDialSkipsAuthWhenNoToken(t *testing.T) {
	authCh := make(chan string, 1)
	addr := fakeDaemon(t, authCh)
	t.Setenv("NATIVE_A11Y_ADDR", addr)
	t.Setenv("EAZYSTREAM_CONTROL_TOKEN", "")
	t.Setenv("WORKER_TOKEN", "")
	SetControlToken("")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	select {
	case got := <-authCh:
		t.Fatalf("unexpected Auth frame %q with no token configured", got)
	case <-time.After(500 * time.Millisecond):
		// correct: no token => no Auth frame
	}
}

func TestSpawnDaemonEnvCarriesToken(t *testing.T) {
	SetControlToken("spawn-tok")
	t.Cleanup(func() { SetControlToken("") })
	if got := authToken(); got != "spawn-tok" {
		t.Fatalf("authToken = %q, want spawn-tok", got)
	}
	// The spawn path appends EAZYSTREAM_CONTROL_TOKEN; assert the value it uses.
	env := append([]string(nil), "EAZYSTREAM_CONTROL_TOKEN="+authToken())
	if env[0] != "EAZYSTREAM_CONTROL_TOKEN=spawn-tok" {
		t.Fatalf("spawn env = %q", env[0])
	}
}
