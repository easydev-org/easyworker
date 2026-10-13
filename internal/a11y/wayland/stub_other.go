//go:build !linux

// Package wayland is Linux-only (it speaks the Wayland wire protocol over a
// unix socket with SCM_RIGHTS fd passing). This stub lets non-Linux builds of
// importing packages compile.
package wayland

import "fmt"

// DefaultKeymap is empty on non-Linux.
var DefaultKeymap = ""

// Conn is a stub on non-Linux.
type Conn struct{}

// Dial always fails on non-Linux.
func Dial() (*Conn, error) { return nil, fmt.Errorf("wayland is Linux-only") }

func (c *Conn) Close() error { return nil }
