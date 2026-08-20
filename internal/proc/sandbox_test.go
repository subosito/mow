package proc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/subosito/mow/internal/sandbox"
)

// bwrapBackend returns a usable bwrap backend for ws, or nil when bubblewrap
// is missing or unusable here (no unprivileged user namespaces).
func bwrapBackend(t *testing.T, ws string) sandbox.Backend {
	t.Helper()
	if _, err := exec.LookPath("bwrap"); err != nil {
		return nil
	}
	be, err := sandbox.New(sandbox.ModeBwrap, sandbox.Spec{Workspace: ws})
	if err != nil {
		return nil
	}
	probe, err := be.Wrap(exec.Command("bash", "-lc", "true"))
	if err != nil || probe.Run() != nil {
		return nil
	}
	return be
}

// A nil backend must behave exactly like the pre-sandbox call: Start's
// variadic argument is how the default path stays untouched.
func TestStartNilBackendIsUnsandboxed(t *testing.T) {
	dir := t.TempDir()
	info, err := Start(dir, "plain", "echo hello-plain; sleep 30", "", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = Stop(dir, "plain") })
	if !info.Alive {
		t.Fatal("process should be alive")
	}
	data, _ := os.ReadFile(info.Log)
	if !strings.Contains(string(data), "hello-plain") {
		t.Errorf("log = %q", data)
	}
}

func TestStartUnderBwrap(t *testing.T) {
	ws := t.TempDir()
	be := bwrapBackend(t, ws)
	if be == nil {
		t.Skip("bwrap not available/usable")
	}
	dir := filepath.Join(ws, "store")

	info, err := Start(dir, "sleeper", "echo up; sleep 30", "", ws, be)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = Stop(dir, "sleeper") })
	if !info.Alive {
		t.Fatalf("sandboxed process not alive: %+v", info)
	}

	// Status must track the pid we recorded (the parent-side setsid leader),
	// not something lost inside the sandbox's pid namespace.
	st, err := Status(dir, "sleeper")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Alive || st.PID != info.PID {
		t.Fatalf("status = %+v, want alive pid %d", st, info.PID)
	}
	data, _ := os.ReadFile(info.Log)
	if !strings.Contains(string(data), "up") {
		t.Errorf("sandboxed process log = %q", data)
	}

	// Stop must actually kill it (keep=false cleanup depends on this).
	if _, err := Stop(dir, "sleeper"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !reaped(info.PID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !reaped(info.PID) {
		t.Fatalf("pid %d still running after Stop", info.PID)
	}
	if _, err := Status(dir, "sleeper"); err == nil {
		t.Error("Stop should remove the pid file")
	}
}

// reaped reports that pid is gone or a zombie. Start detaches with Release, so
// the Go parent never waits: a killed child lingers as a zombie and
// pidAlive (kill(pid,0)) still succeeds for it. That is true on the plain path
// too — it is not sandbox-specific — so "did Stop work?" has to read the
// process state rather than trust the liveness probe.
func reaped(pid int) bool {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return true // gone entirely
	}
	// "pid (comm) STATE ..." — comm may contain spaces/parens, so scan past the last ')'.
	s := string(data)
	i := strings.LastIndex(s, ")")
	if i < 0 || i+2 >= len(s) {
		return false
	}
	return s[i+2] == 'Z'
}

func TestStartUnderBwrapJailsFilesystem(t *testing.T) {
	ws := t.TempDir()
	be := bwrapBackend(t, ws)
	if be == nil {
		t.Skip("bwrap not available/usable")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(cwd, "/tmp") {
		t.Skip("package dir under /tmp is shadowed by the sandbox tmpfs")
	}
	dir := filepath.Join(ws, "store")
	outside := filepath.Join(cwd, "proc.go")

	info, err := Start(dir, "peek", "cat "+outside+" 2>&1; echo done", "", ws, be)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = Stop(dir, "peek") })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, _ := os.ReadFile(info.Log); strings.Contains(string(data), "done") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, _ := os.ReadFile(info.Log)
	if strings.Contains(string(data), "package proc") {
		t.Fatalf("proc_start escaped the sandbox and read %s", outside)
	}
}

func TestStartSandboxErrorIsNotSilentFallback(t *testing.T) {
	dir := t.TempDir()
	// A backend that always fails must abort the start, not run raw bash.
	if _, err := Start(dir, "bad", "echo nope", "", dir, failBackend{}); err == nil {
		t.Fatal("Start must fail when the sandbox cannot wrap the command")
	}
	if _, err := Status(dir, "bad"); err == nil {
		t.Fatal("no pid file should be recorded for a failed sandbox start")
	}
}

type failBackend struct{}

func (failBackend) Wrap(*exec.Cmd) (*exec.Cmd, error) { return nil, os.ErrPermission }
func (failBackend) Mode() sandbox.Mode                { return sandbox.ModeBwrap }
