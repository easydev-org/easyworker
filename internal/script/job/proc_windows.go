//go:build windows

package job

import (
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// setupProcessGroup creates the child in its own process group so a console
// Ctrl-C aimed at the daemon does not leak into scripts. The job is ALSO put in
// a Job Object (see assignJobObject), which is what makes a kill reliable.
func setupProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

// jobObjects holds one Job Object per running job, keyed by pid, so a kill can
// terminate the whole tree with one call.
var (
	jobMu   sync.Mutex
	jobObjs = map[int]windows.Handle{}
)

// assignJobObject puts the process in a Job Object configured to kill every
// process it ever spawns when the job is terminated.
//
// This exists because CI proved the two obvious approaches insufficient on
// Windows: TestKillStopsTheJob and TestTimeoutKills both timed out, i.e. a
// `kill` silently did nothing.
//
//   - taskkill /T /F walks the tree, but it can fail (and did) when the target
//     sits in the console/session that CREATE_NEW_PROCESS_GROUP gave it. Its error
//     was discarded, so the failure looked like success.
//   - os.Process.Kill() only terminates the ONE process; a script that spawned
//     children leaves them behind, and the job's pipes stay open, so Wait blocks.
//
// A Job Object with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE terminates the entire
// group by construction: no tree walking, no session games, no orphans.
func assignJobObject(pid int) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return // fall back to killTree's best effort
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(
		h,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(h)
		return
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		windows.CloseHandle(h)
		return
	}
	defer windows.CloseHandle(ph)
	if err := windows.AssignProcessToJobObject(h, ph); err != nil {
		windows.CloseHandle(h)
		return
	}
	jobMu.Lock()
	jobObjs[pid] = h
	jobMu.Unlock()
}

// releaseJobObject closes the Job Object once the process is gone. With
// KILL_ON_JOB_CLOSE this also reaps anything the script left behind — which is
// the behaviour we want when a job ends.
func releaseJobObject(pid int) {
	jobMu.Lock()
	h, ok := jobObjs[pid]
	delete(jobObjs, pid)
	jobMu.Unlock()
	if ok {
		windows.CloseHandle(h)
	}
}

// killTree terminates the job's whole process tree.
//
// TerminateJobObject is the primary mechanism (it kills every process in the job,
// including grandchildren). taskkill and a direct Kill remain as fallbacks for
// the cases where the Job Object could not be created (a host policy may forbid
// it), so a kill still does SOMETHING rather than nothing.
func killTree(pid int) {
	jobMu.Lock()
	h, ok := jobObjs[pid]
	jobMu.Unlock()
	if ok {
		_ = windows.TerminateJobObject(h, 1)
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
