//go:build unix

package job

import (
	"os/exec"
	"syscall"
)

// setupProcessGroup puts the child in its own process group (so the group id
// equals the child pid). Everything the script spawns — subprocess, os.system,
// backticks, pipelines — inherits that group, which is what lets killTree reap
// the entire tree with one signal instead of chasing pids.
func setupProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killTree sends SIGKILL to the child's whole process group. The negative pid is
// the POSIX spelling of "this group".
func killTree(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// assignJobObject is a Windows concept. On Unix the process group created by
// setupProcessGroup already gives killTree one handle for the whole tree, so
// there is nothing to assign.
func assignJobObject(int) {}

// releaseJobObject has nothing to release on Unix: the process group dies with
// its leader.
func releaseJobObject(int) {}
