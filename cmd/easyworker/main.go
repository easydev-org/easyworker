// Command easyworker is the sandbox worker: a single static binary exposing
// the WorkerService Connect API. It runs (a) as a plain host process on
// windows/macos/linux (the forgejo-runner desktop model: register and dial
// out), and (b) as the worker injected into cluster sandboxes (WORKER_PORT
// pins the listen port there; the legacy default was 8080, sandboxes pin
// 48080).
//
// Every command executes through the builtin easysh interpreter —
// no passthrough, no fallback — so bash semantics are identical everywhere.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	workerv1connect "github.com/easydev-org/easyworker/gen/worker/v1/workerv1connect"
	"github.com/easydev-org/easyworker/internal"
	"github.com/easydev-org/easyworker/internal/a11y/native"
	"github.com/easydev-org/easyworker/internal/auth"
	"github.com/easydev-org/easyworker/internal/computer"
	"github.com/easydev-org/easyworker/internal/desktopproxy"
	"github.com/easydev-org/easyworker/internal/filesvc"
	"github.com/easydev-org/easyworker/internal/jobsvc"
	"github.com/easydev-org/easyworker/internal/script"
	"github.com/easydev-org/easyworker/internal/stream"
	"github.com/easydev-org/easyworker/internal/toolchains"
	"github.com/easydev-org/easyworker/internal/webui"
)

func main() {
	// Subcommand: `toolchain-install <name=version,...>` — the CLI half of the
	// on-demand toolchain installer. Detected before flag parsing so the worker's
	// own flags stay untouched.
	if len(os.Args) > 1 && os.Args[1] == "toolchain-install" {
		os.Exit(runToolchainInstall(os.Args[2:]))
	}

	addr := flag.String("addr", "", "listen address (default 0.0.0.0:${WORKER_PORT:-8080})")
	workspace := flag.String("workspace", "", "workspace root (default ${WORKER_WORKSPACE} or ~/workspace)")
	dbPath := flag.String("db", "", "job history sqlite path (default ${WORKER_DB} or ./easyworker.db)")
	flag.Parse()

	listen := *addr
	if listen == "" {
		port := envOr("WORKER_PORT", "8080")
		listen = "0.0.0.0:" + port
	}
	ws := *workspace
	if ws == "" {
		ws = envOr("WORKER_WORKSPACE", defaultWorkspace())
	}
	if err := os.MkdirAll(ws, 0o755); err != nil {
		log.Fatalf("workspace: %v", err)
	}
	db := *dbPath
	if db == "" {
		db = envOr("WORKER_DB", "easyworker.db")
	}

	// Capture the pre-authorized token, then drop it from the process
	// environment BEFORE building the job env: jobs inherit the worker's
	// environ (see ensureProcessEnv), and the token must not travel with them. The auth
	// gate below keeps the returned value in memory.
	bootToken := takeBootToken()

	// Hand the same token to the native-a11y client, so the worker can present
	// it to the easystream daemon (est Auth frame + HTTP ?token=). The sandbox
	// starts the daemon with EAZYSTREAM_CONTROL_TOKEN set to this value, so the
	// daemon is LOCKED (control needs the token, view stays open) and the worker
	// is the only client that can drive it. Empty when no token was supplied
	// (dev / auth off), in which case the daemon runs open as before.
	native.SetControlToken(bootToken)

	// Job env: inherit the worker's own environment (the sandbox image is the
	// source of truth for toolchain variables); the token is already gone.
	// Pre-existing on-demand toolchains are re-added to PATH first, so a
	// restarted worker keeps seeing them without reinstalling.
	tc := toolchains.New(os.Getenv("WORKER_TOOLCHAIN_ROOT"), os.Getenv("WORKER_TOOLCHAIN_INDEX"), nil, log.Printf)
	if bins := toolchains.ExistingBinDirs(tc.Root()); len(bins) > 0 {
		_ = os.Setenv("PATH", toolchains.MergePath(os.Getenv("PATH"), bins))
	}
	ensureProcessEnv()
	runner := script.New(ws)
	store, err := jobsvc.OpenStore(db)
	if err != nil {
		log.Fatalf("open store %s: %v", db, err)
	}
	jobs := jobsvc.NewManager(runner, store, 10000, 1000)
	files := filesvc.New(ws)
	svc := internal.NewService(jobs, files, runner, tc)

	// Fail-closed auth gate: a token must be supplied at boot (managed
	// sandbox) or claimed once from a startup-minted one-time code (external
	// sandbox / host runner). Enrollment state is persisted (WORKER_STATE_FILE)
	// so an already-claimed worker resumes with the SAME token after a restart
	// — no re-claim. WORKER_REQUIRE_AUTH=0 disables auth (dev only).
	var stateStore auth.StateStore
	if sf := workerStateFile(db); sf != "" {
		stateStore = auth.NewFileStore(sf)
	}
	gate, err := auth.New(auth.Options{
		PreAuthorizedToken: bootToken,
		Disabled:           os.Getenv("WORKER_REQUIRE_AUTH") == "0",
		BootID:             svc.BootID(),
		State:              stateStore,
	})
	if err != nil {
		log.Fatalf("auth: %v", err)
	}
	enroll := internal.NewEnrollService(gate)

	mux := http.NewServeMux()
	srv :=
		// WorkerService is bearer-gated; WorkerEnroll is mounted unprotected (it
		// closes itself after the single successful claim).
		connect.NewServer(auth.NewInterceptor(gate))
	workerv1connect.RegisterWorkerServiceHandler(srv, svc)
	connecthttp.Mount(mux, srv, connecthttp.WithReadMaxBytes(0))

	// ComputerService: the worker-native GUI-automation surface (bearer-gated
	// like WorkerService). The driver is resolved from this worker's platform.
	comp := internal.NewComputerService(computer.Resolve(), jobs)
	srv2 := connect.NewServer(auth.NewInterceptor(gate))
	workerv1connect.RegisterComputerServiceHandler(srv2, comp)
	connecthttp.Mount(mux, srv2, connecthttp.WithReadMaxBytes(0))
	srv3 := connect.NewServer()

	workerv1connect.RegisterWorkerEnrollHandler(srv3, enroll)
	connecthttp.
		// Plain health endpoint for probes (Connect has its own but a 200 GET /
		// is the cheapest readiness signal).
		Mount(mux, srv3, connecthttp.WithReadMaxBytes(0))

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	// The easystream daemon's own UI, proxied under our origin at /desktop/.
	// The worker and the daemon are always in the SAME pod (the daemon listens
	// on 127.0.0.1:48078), so this needs no Service, no second host and no
	// CORS: the panel just opens /desktop/ in a new window.
	// The daemon authorizes View by default; only a control token upgrades it to
	// Control (keyboard/mouse injection). Inject the worker's own token so the
	// proxied desktop is actually drivable, without handing the token to the page.
	// Use the gate's ACTIVE token, not bootToken: bootToken is only what the
	// image passed in, and it is empty on an image that relies on persisted
	// enrollment (kiosk RESUMED with no WORKER_TOKEN in its env). Using the
	// gate's token means the proxy always matches what the daemon was told.
	mux.Handle("/desktop/", desktopproxy.Handler("/desktop", os.Getenv("EASYSTREAM_HTTP"), gate.LocalToken()))
	mux.Handle("/desktop", http.RedirectHandler("/desktop/", http.StatusFound))
	// WebRTC screen stream (H.264), captured + encoded in-process by the
	// embedded a11y library. The panel's Desktop view prefers this and falls
	// Mounted only when the sandbox has a screen to capture.
	if hasScreen() {
		mux.Handle(stream.Prefix, stream.New(30, 12_000_000).Handler())
	}
	// Built-in static control panel at "/" (same-origin with the RPC above).
	// Registered LAST: the RPC handlers own their longer, more specific paths
	// ("/worker.v1.*"), which the ServeMux prefers over this catch-all.
	mux.Handle("/", webui.Handler())

	// Surface the enrollment state. A RESUMED worker keeps its prior token and
	// is already claimable/usable; an UNCLAIMED worker prints its one-time code
	// (stdout) for a launcher to capture.
	switch {
	case gate.Resumed():
		log.Printf("easyworker RESUMED — persisted enrollment (no re-claim needed)")
	case gate.Code() != "":
		log.Printf("easyworker UNCLAIMED — enrollment code: %s", gate.Code())
	}

	// Dual-stack h1 + h2c, mirroring easylab's listener shape so both Connect
	// over h1 (browsers/curl) and h2c-prior-knowledge clients work.
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	server := &http.Server{
		Addr:      listen,
		Handler:   mux,
		Protocols: protocols,
		// Disable Nagle: the /stream/ws frames are small and latency-sensitive;
		// Nagle would coalesce them and add up to ~40ms per frame.
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if tc, ok := c.(*net.TCPConn); ok {
				_ = tc.SetNoDelay(true)
			}
			return ctx
		},
	}

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("easyworker listening on %s (os=%s arch=%s workspace=%s shell=builtin)",
		listen, runtime.GOOS, runtime.GOARCH, ws)
	// Say whether `git` is usable, once, at boot: a job that needs a repo
	// fails confusingly later otherwise (and on Android there is no git at all,
	// which is expected, not a fault). Reported in Info.capabilities too.
	if c := internal.CapabilitiesProbe(); c.Git {
		log.Printf("git: %s", c.GitVersion)
	} else {
		log.Printf("git: not available (jobs cannot clone/commit on this platform)")
	}
	// The panel is embedded at build time from internal/webui/dist/, which is
	// git-ignored. A local `go build` in a tree that only has a placeholder
	// ships that placeholder (v0.2.0-v0.3.0 did exactly this), so say out loud
	// which one is in here.
	if webui.PanelLooksStubbed() {
		log.Printf("panel: STUB embedded (%d B) — / serves a placeholder, build the SPA first", webui.PanelSize())
	} else {
		log.Printf("panel: ok (%d B)", webui.PanelSize())
	}

	// Graceful shutdown on SIGINT/SIGTERM (k8s sends SIGTERM before killing
	// the pod): stop accepting, let in-flight RPCs finish, kill running jobs
	// (their finish records commit) and DRAIN the store writer so the last
	// ≤200ms output batch is not lost.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Printf("easyworker shutting down (signal received)")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("shutdown: %v", err)
		}
		if err := jobs.Close(); err != nil {
			log.Printf("jobs close: %v", err)
		}
		os.Exit(0)
	}()

	log.Fatal(server.Serve(ln))
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// workerStateFile resolves the enrollment state file path. Default: alongside
// the job DB (stable across restarts). WORKER_STATE_FILE overrides it; the
// special value "off" (or empty) disables persistence.
func workerStateFile(dbPath string) string {
	if v, ok := os.LookupEnv("WORKER_STATE_FILE"); ok {
		if v == "" || v == "off" {
			return ""
		}
		return v
	}
	return filepath.Join(filepath.Dir(dbPath), "worker.state")
}

// defaultWorkspace is per-platform: ~/workspace (the worker's own home). The
// deployment may still override it with WORKER_WORKSPACE.
func defaultWorkspace() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, "workspace")
}

// takeBootToken reads WORKER_TOKEN and then removes it from the process
// environment, so jobs (which inherit the workers environ — see ensureProcessEnv) can
// never see it. Callers keep the returned value in memory for the auth gate.
func takeBootToken() string {
	tok := os.Getenv("WORKER_TOKEN")
	if tok != "" {
		_ = os.Unsetenv("WORKER_TOKEN")
	}
	return tok
}

// ensureProcessEnv fills in variables the process is MISSING so interpreted jobs
// (which inherit this process's environment verbatim) still work on a minimal
// image.
//
// Why inherit at all: the sandbox image is the source of truth for toolchain
// variables, so a default-deny allowlist would have to enumerate every variable
// of every toolchain — an endless, brittle treadmill that breaks Windows
// (cmd.exe/ComSpec, MSVC, .NET, Gradle, pip…) and any newly added tool.
// Inheritance means a new toolchain works with no code change.
//
// Why fill gaps: a scratch image may have no PATH at all, and a baked-in egress
// CA may never have been exported. Synthesizing those turns "mysteriously broken"
// into "works". Nothing is ever OVERRIDDEN — only missing keys are added.
//
// The worker's own secret (WORKER_TOKEN) is removed from the environment before
// this runs (takeBootToken), so it is not inherited. This is NOT a security
// boundary — a job runs with the worker's privileges and could read
// /proc/<pid>/environ regardless — it only prevents ACCIDENTAL leakage via
// `env` or verbose build logs.
//
// It mutates the process environment in place, because the engine reads that: a
// per-job environment is deliberately not a thing.
func ensureProcessEnv() {
	out := os.Environ()

	// Synthesize platform defaults the process may be missing (e.g. a minimal
	// container with no PATH). Only fills gaps; never overrides.
	if runtime.GOOS == "windows" {
		defaults := [][2]string{
			{"SystemRoot", os.Getenv("SystemRoot")},
			{"SystemDrive", os.Getenv("SystemDrive")},
			{"ComSpec", os.Getenv("ComSpec")},
			{"windir", os.Getenv("windir")},
			{"ProgramData", os.Getenv("ProgramData")},
			{"ProgramFiles", os.Getenv("ProgramFiles")},
			{"ProgramFiles(x86)", os.Getenv("ProgramFiles(x86)")},
			{"LOCALAPPDATA", os.Getenv("LOCALAPPDATA")},
			{"USERPROFILE", os.Getenv("USERPROFILE")},
		}
		for _, kv := range defaults {
			if kv[1] != "" && !containsKey(out, kv[0]) {
				out = append(out, kv[0]+"="+kv[1])
			}
		}
	} else {
		if !containsKey(out, "PATH") {
			out = append(out, "PATH="+defaultUnixPath())
		}
		// CA/trust: preset images bake the egress CA but may not export it; the
		// system bundle path is a safe fallback so TLS clients keep working.
		if !containsKey(out, "SSL_CERT_FILE") {
			if f := firstExistingFile(
				"/etc/ssl/certs/ca-certificates.crt",
				"/etc/ssl/cert.pem",
				"/etc/pki/tls/certs/ca-bundle.crt",
			); f != "" {
				out = append(out, "SSL_CERT_FILE="+f)
			}
		}
	}
	// Write back only the keys we synthesized (never the whole environment, which
	// would be a no-op churn).
	for _, kv := range out {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		key, val := kv[:i], kv[i+1:]
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, val)
		}
	}
}

// firstExistingFile returns the first path that exists, else "".
func firstExistingFile(paths ...string) string {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func containsKey(env []string, key string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			return true
		}
	}
	return false
}

// defaultUnixPath: inside minimal containers PATH may be unset in the worker's
// own environ; give the common toolchain locations.
func defaultUnixPath() string {
	return strings.Join([]string{
		"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin",
		"/sbin", "/bin", "/root/.cargo/bin", "/root/go/bin",
	}, ":")
}

var _ = connect.NewError // keep import when flag set shrinks

// hasScreen reports whether the sandbox has a capturable screen.
func hasScreen() bool {
	if os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("DISPLAY") != "" {
		return true
	}
	switch internal.EnvOS() {
	case "darwin", "macos", "windows", "android":
		return true
	}
	return false
}
