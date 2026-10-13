package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/easydev-org/easyworker/internal/toolchains"
)

// runToolchainInstall implements `agent-worker toolchain-install <spec>...`.
// Specs are `name=version` (space- or comma-separated). With no arguments it
// falls back to $WORKSPACE_TOOLCHAINS. It installs the requested toolchains
// (plus their `requires`) into $WORKER_TOOLCHAIN_ROOT from the index at
// $WORKER_TOOLCHAIN_INDEX, and prints the resulting PATH dirs (one per line).
func runToolchainInstall(args []string) int {
	raw := strings.Join(args, ",")
	if strings.TrimSpace(raw) == "" {
		raw = os.Getenv("WORKSPACE_TOOLCHAINS")
	}
	specs, err := toolchains.ParseSpecs(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "toolchain-install:", err)
		return 2
	}
	if len(specs) == 0 {
		fmt.Fprintln(os.Stderr, "toolchain-install: no toolchains requested (usage: toolchain-install go=1.27.1,node=26.9.0)")
		return 2
	}

	timeout := 30 * time.Minute
	if v := os.Getenv("WORKER_TOOLCHAIN_TIMEOUT"); v != "" {
		if d, derr := time.ParseDuration(v); derr == nil && d > 0 {
			timeout = d
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	in := toolchains.New(os.Getenv("WORKER_TOOLCHAIN_ROOT"), os.Getenv("WORKER_TOOLCHAIN_INDEX"), nil, log.Printf)
	bins, err := in.Ensure(ctx, specs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "toolchain-install:", err)
		return 1
	}
	for _, b := range bins {
		fmt.Println(b)
	}
	return 0
}
