package internal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
	workerv1 "github.com/easydev-org/easyworker/gen/worker/v1"
	"github.com/easydev-org/easyworker/gen/worker/v1/workerv1connect"
	"github.com/easydev-org/easyworker/internal/filesvc"
	"github.com/easydev-org/easyworker/internal/jobsvc"
	"github.com/easydev-org/easyworker/internal/script"
	"github.com/easydev-org/easyworker/internal/toolchains"
)

// jobWaitMax caps JobWait. Configurable via WORKER_WAIT_MAX (go duration or
// bare seconds); the 600s default lets the common "wait for the build" call
// block in ONE rpc instead of client-side polling slices.
var jobWaitMax = waitMaxFromEnv()

func waitMaxFromEnv() time.Duration {
	d, err := time.ParseDuration(os.Getenv("WORKER_WAIT_MAX"))
	if err != nil || d <= 0 {
		return 600 * time.Second
	}
	return d
}

// WorkerService implements workerv1connect.WorkerServiceHandler.
type WorkerService struct {
	jobs       *jobsvc.Manager
	files      *filesvc.Service
	shell      *script.Runner
	toolchains *toolchains.Installer
	bootID     string
}

func NewService(jobs *jobsvc.Manager, files *filesvc.Service, shell *script.Runner, tc *toolchains.Installer) *WorkerService {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return &WorkerService{jobs: jobs, files: files, shell: shell, toolchains: tc, bootID: hex.EncodeToString(b[:])}
}

// Runner exposes the builtin shell runner (the ComputerService drives GUI
// tools through it).
func (s *WorkerService) Runner() *script.Runner { return s.shell }

func (s *WorkerService) Info(ctx context.Context, req *workerv1.InfoRequest) (*workerv1.InfoResponse, error) {
	return &workerv1.InfoResponse{
		Os:   goos(),
		Arch: goarch(),
		// The engine is easyscript: a program TEXT goes to an external interpreter,
		// never to a host shell and never to an in-process one. No language is
		// named here because there is no default one — a caller states it per job
		// (see ExecuteRequest.lang). Which interpreters exist is
		// Info.interpreters, which is the list a client should actually read.
		Shell: "easyscript",
		// Slash-form so a Windows worker reports C:/... consistently to the UI.
		Workspace:    filepath.ToSlash(s.files.Root()),
		BootId:       s.bootID,
		DroppedLines: s.jobs.Dropped(),
		Home:         filepath.ToSlash(homeDir()),
		Capabilities: probeCapabilities(),
		Interpreters: s.interpreters(ctx),
	}, nil
}

// interpreters reports which languages this worker can actually run, probed at
// request time. A client uses this to offer a language picker instead of
// assuming bash; the list is empty when nothing is installed, which is a real
// answer ("this image cannot run anything"), not an error.
func (s *WorkerService) interpreters(ctx context.Context) []*workerv1.Interpreter {
	eng := s.shell.Engine()
	if eng == nil {
		return nil
	}
	vers := eng.Languages(ctx)
	out := make([]*workerv1.Interpreter, 0, len(vers))
	for _, v := range vers {
		if !v.Found {
			continue
		}
		out = append(out, &workerv1.Interpreter{
			Id:      v.ID,
			Name:    v.Name,
			Path:    v.Path,
			Version: v.Version,
			Modes:   v.Modes,
		})
	}
	return out
}

func (s *WorkerService) Execute(ctx context.Context, req *workerv1.ExecuteRequest) (*workerv1.ExecuteResponse, error) {
	if req.Command == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "command required")
	}
	// lang is REQUIRED. See the proto comment: there is no safe default, because a
	// command means different things in different languages. Rejecting here (rather
	// than guessing) is what keeps "it ran" from meaning "it ran as the wrong
	// language and still exited 0".
	if strings.TrimSpace(req.GetLang()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			"lang required: name the interpreter the command is written in "+
				"(see Info.interpreters for this host's ids)")
	}
	// On-demand toolchains: install whatever the workspace declared BEFORE the
	// job runs. A failure fails the job loudly — never a silent fallback to the
	// bare base image (see abc-protocol/deploy/DEVELOP.md "Toolchain index").
	if err := s.ensureToolchains(ctx); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("toolchains: %w", err).Error())
	}
	ns := req.GetNamespace()
	if ns == "" {
		ns = jobsvc.NamespaceUser
	}
	// NOTE: req.Env is deliberately NOT forwarded. The engine has no per-job
	// environment: an "extra env" is either an overlay (a second concept for every
	// caller) or a replacement (which silently drops PATH and breaks subprocess).
	// A caller that needs a variable sets it inside its program.
	id, err := s.jobs.ExecuteNS(ctx, jobsvc.ExecOpts{
		Namespace: ns,
		Command:   req.Command,
		Workdir:   req.Workdir,
		Lang:      req.GetLang(),
		Mode:      req.GetMode(),
		TimeoutMs: req.TimeoutMs,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	return &workerv1.ExecuteResponse{JobId: id}, nil
}

// ensureToolchains installs the toolchains declared by WORKSPACE_TOOLCHAINS
// and/or the workspace's `.toolchains` file, then makes their bin dirs visible
// to subsequent jobs (two layers: the runner's job env AND the worker's own
// process env, so anything the worker spawns sees them too). No-op when nothing
// is declared or no installer is configured.
func (s *WorkerService) ensureToolchains(ctx context.Context) error {
	if s.toolchains == nil {
		return nil
	}
	specs, err := toolchains.SpecsFromEnv(os.Getenv("WORKSPACE_TOOLCHAINS"), s.shell.Root())
	if err != nil {
		return err
	}
	if len(specs) == 0 {
		return nil
	}
	bins, err := s.toolchains.Ensure(ctx, specs)
	if err != nil {
		return err
	}
	if len(bins) > 0 {
		prependProcessPath(bins)
	}
	return nil
}

// prependProcessPath merges dirs into the worker's OWN PATH (dedup), so a
// spawned process — not just a job — resolves the new toolchain.
func prependProcessPath(dirs []string) {
	_ = os.Setenv("PATH", toolchains.MergePath(os.Getenv("PATH"), dirs))
}

func (s *WorkerService) ListJobs(ctx context.Context, req *workerv1.ListJobsRequest) (*workerv1.ListJobsResponse, error) {
	limit := int(req.Limit)
	var out []*workerv1.JobEntry
	for _, r := range s.jobs.ListNS(limit, req.GetNamespace()) {
		out = append(out, &workerv1.JobEntry{
			Id:         r.ID,
			Command:    r.Command,
			Lang:       r.Lang,
			Namespace:  r.Namespace,
			State:      r.State,
			ExitCode:   r.ExitCode,
			StartedAt:  r.StartedAt,
			FinishedAt: r.FinishedAt,
		})
	}
	return &workerv1.ListJobsResponse{Jobs: out}, nil
}

// WatchJob replays persisted+live history as output events, then streams
// live output until completion (terminal Done event always sent last).
func (s *WorkerService) WatchJob(ctx context.Context, req *workerv1.WatchJobRequest, stream workerv1connect.WorkerServiceWatchJobServerStream) error {
	if req.JobId == "" {
		return connect.NewError(connect.CodeInvalidArgument, "job_id required")
	}
	job, memErr := s.jobs.Get(req.JobId)

	// Finished long ago (not in memory): replay from the store, send Done.
	if memErr != nil {
		row, ok, err := s.jobs.Store().QueryJob(req.JobId)
		if err != nil || !ok {
			return connect.NewError(connect.CodeNotFound, "job not found")
		}
		for _, line := range s.jobs.ReplayTail(req.JobId, replayCap) {
			if err := stream.Send(&workerv1.WatchJobResponse{Event: &workerv1.WatchJobResponse_Output{Output: line}}); err != nil {
				return err
			}
		}
		stdout := s.jobs.ReplayTailStream(req.JobId, jobsvc.StreamStdout, doneTailLines)
		stderr := s.jobs.ReplayTailStream(req.JobId, jobsvc.StreamStderr, doneTailLines)
		return stream.Send(&workerv1.WatchJobResponse{Event: &workerv1.WatchJobResponse_Done_{Done: &workerv1.WatchJobResponse_Done{
			ExitCode: row.ExitCode,
			Stdout:   joinLines(stdout),
			Stderr:   joinLines(stderr),
		}}})
	}

	// Live path: replay recent history, subscribe, stream until Done.
	replay, live := job.Subscribe()
	for _, line := range replay {
		if err := stream.Send(&workerv1.WatchJobResponse{Event: &workerv1.WatchJobResponse_Output{Output: line}}); err != nil {
			return err
		}
	}
	// Drain live output until the channel closes. Select on ctx.Done too, so a
	// client disconnect (or a channel that never closes) can never park this
	// handler forever.
	for live != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case line, ok := <-live:
			if !ok {
				live = nil
				break
			}
			if err := stream.Send(&workerv1.WatchJobResponse{Event: &workerv1.WatchJobResponse_Output{Output: line}}); err != nil {
				return err
			}
		}
	}
	res := job.Result()
	return stream.Send(&workerv1.WatchJobResponse{Event: &workerv1.WatchJobResponse_Done_{Done: &workerv1.WatchJobResponse_Done{
		ExitCode: res.ExitCode,
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
	}}})
}

// replayCap bounds WatchJob's replay from history (live window is separate).
const replayCap = 1000

// doneTailLines is the tail size carried in the terminal Done event.
const doneTailLines = 50

func joinLines(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}

func (s *WorkerService) JobOutput(ctx context.Context, req *workerv1.JobOutputRequest) (*workerv1.JobOutputResponse, error) {
	if req.JobId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "job_id required")
	}
	stream := streamOf(req.Stream)
	lines, total, start, end, done, err := s.jobs.OutputLines(req.JobId, stream, req.Start, req.End)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err.Error()).WithCause(err)
	}
	return &workerv1.JobOutputResponse{
		Lines:      lines,
		TotalLines: total,
		StartLine:  start,
		EndLine:    end,
		Done:       done,
	}, nil
}

func streamOf(s string) int {
	switch s {
	case "stdout":
		return jobsvc.StreamStdout
	case "stderr":
		return jobsvc.StreamStderr
	default:
		return -1 // all
	}
}

func (s *WorkerService) JobWait(ctx context.Context, req *workerv1.JobWaitRequest) (*workerv1.JobWaitResponse, error) {
	script.Debugf("JobWait rpc: job %s timeoutMs=%d mono=%d", req.JobId, req.TimeoutMs, script.MonoMS())
	job, err := s.jobs.Get(req.JobId)
	if err != nil {
		// Post-restart finished job: answer from the store.
		row, ok, serr := s.jobs.Store().QueryJob(req.JobId)
		if serr != nil || !ok {
			return nil, connect.NewError(connect.CodeNotFound, "job not found")
		}
		return &workerv1.JobWaitResponse{State: row.State, ExitCode: row.ExitCode}, nil
	}
	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	if timeout <= 0 || timeout > jobWaitMax {
		timeout = jobWaitMax
	}
	// BLOCKING wait — no poll loop: the goroutine parks until the job
	// completes, the caller disconnects or the deadline fires.
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-job.Done():
		script.Debugf("JobWait rpc: job %s -> done branch", req.JobId)
		res := job.Result()
		return &workerv1.JobWaitResponse{State: job.State, ExitCode: res.ExitCode}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		script.Debugf("JobWait rpc: job %s -> TIMEOUT branch (state=%s)", req.JobId, job.State)
		return &workerv1.JobWaitResponse{State: job.State, ExitCode: job.ExitCode}, nil
	}
}

func (s *WorkerService) JobStdin(ctx context.Context, req *workerv1.JobStdinRequest) (*workerv1.JobStdinResponse, error) {
	job, err := s.jobs.Get(req.JobId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err.Error()).WithCause(err)
	}
	if err := job.Stdin(req.Data, req.Close); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err.Error()).WithCause(err)
	}
	return &workerv1.JobStdinResponse{Ok: true}, nil
}

func (s *WorkerService) JobKill(ctx context.Context, req *workerv1.JobKillRequest) (*workerv1.JobKillResponse, error) {
	script.Debugf("JobKill rpc: job %s", req.JobId)
	job, err := s.jobs.Get(req.JobId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err.Error()).WithCause(err)
	}
	job.Kill()
	script.Debugf("JobKill rpc: cancel() called for %s mono=%d", req.JobId, script.MonoMS())
	return &workerv1.JobKillResponse{Ok: true}, nil
}

func (s *WorkerService) FileRead(ctx context.Context, req *workerv1.FileReadRequest) (*workerv1.FileReadResponse, error) {
	if req.Path == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "path required")
	}
	if req.StartLine == 0 && req.EndLine == 0 {
		// Whole file (backward-compatible no-window call).
		data, err := s.files.Read(req.Path)
		if err != nil {
			return nil, connect.NewError(connect.CodeNotFound, err.Error()).WithCause(err)
		}
		return &workerv1.FileReadResponse{Content: data}, nil
	}
	data, total, start, end, err := s.files.ReadWindow(req.Path, req.StartLine, req.EndLine)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err.Error()).WithCause(err)
	}
	return &workerv1.FileReadResponse{
		Content:    data,
		TotalLines: total,
		StartLine:  start,
		EndLine:    end,
	}, nil
}

func (s *WorkerService) FileWrite(ctx context.Context, req *workerv1.FileWriteRequest) (*workerv1.FileWriteResponse, error) {
	if req.Path == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "path required")
	}
	if err := s.files.Write(req.Path, req.Content); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	return &workerv1.FileWriteResponse{Ok: true}, nil
}

func (s *WorkerService) FileDelete(ctx context.Context, req *workerv1.FileDeleteRequest) (*workerv1.FileDeleteResponse, error) {
	if req.Path == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "path required")
	}
	if err := s.files.Delete(req.Path); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err.Error()).WithCause(err)
	}
	return &workerv1.FileDeleteResponse{Ok: true}, nil
}

func (s *WorkerService) FileMove(ctx context.Context, req *workerv1.FileMoveRequest) (*workerv1.FileMoveResponse, error) {
	if req.From == "" || req.To == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "from and to required")
	}
	if err := s.files.Move(req.From, req.To); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err.Error()).WithCause(err)
	}
	return &workerv1.FileMoveResponse{Ok: true}, nil
}

func (s *WorkerService) FileCopy(ctx context.Context, req *workerv1.FileCopyRequest) (*workerv1.FileCopyResponse, error) {
	if req.From == "" || req.To == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "from and to required")
	}
	if err := s.files.Copy(req.From, req.To); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err.Error()).WithCause(err)
	}
	return &workerv1.FileCopyResponse{Ok: true}, nil
}

func (s *WorkerService) FileList(ctx context.Context, req *workerv1.FileListRequest) (*workerv1.FileListResponse, error) {
	isDir, entries, truncated, err := s.files.List(req.Path, int(req.Depth), int(req.Limit))
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err.Error()).WithCause(err)
	}
	var out []*workerv1.FileEntry
	for _, e := range entries {
		out = append(out, &workerv1.FileEntry{Path: e.Path, Size: e.Size, IsDir: e.IsDir})
	}
	return &workerv1.FileListResponse{IsDir: isDir, Files: out, Truncated: truncated}, nil
}

// SyncFolder unpacks a directory-tree tarball into the workspace (the
// easylab -> worker checkout path for sandbox/CI workspaces).
func (s *WorkerService) SyncFolder(ctx context.Context, req *workerv1.SyncFolderRequest) (*workerv1.SyncFolderResponse, error) {
	n, err := s.files.SyncFolder(req.Tarball, req.Dest, req.Clean)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error()).WithCause(err)
	}
	return &workerv1.SyncFolderResponse{Files: int32(n), Root: s.files.Root()}, nil
}

// BootID exposes this worker process's boot id (surfaced by Info; used as the
// enrollment gate's identity).
func (s *WorkerService) BootID() string { return s.bootID }
