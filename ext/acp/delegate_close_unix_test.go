//go:build unix

package acp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/subosito/mow"
)

// TestCloseAllKillsProcessGroup puts a real subprocess tree in the delegate
// pool and asserts closeAll reaps the whole group (not just the parent).
// Without Setpgid + SIGTERM/SIGKILL, `sh -c 'sleep … & wait'` leaves sleep
// reparented to PID 1.
func TestCloseAllKillsProcessGroup(t *testing.T) {
	parent, child, c := startSleepingTree(t)
	tool := &delegateTool{
		peers: map[string]*peerSlot{
			"k": {client: c, sessionID: "s1", lastUsed: time.Now()},
		},
	}
	tool.closeAll()
	if len(tool.peers) != 0 {
		t.Fatalf("peers after closeAll: %v", tool.peers)
	}
	assertProcessGone(t, parent, "pooled peer")
	assertProcessGone(t, child, "peer child")
}

func TestDropPeersKillsProcessGroup(t *testing.T) {
	parent, child, c := startSleepingTree(t)
	tool := &delegateTool{
		peers: map[string]*peerSlot{
			"k": {client: c, sessionID: "s1", lastUsed: time.Now()},
		},
	}
	tool.dropPeers()
	if len(tool.peers) != 0 {
		t.Fatalf("peers after dropPeers: %v", tool.peers)
	}
	assertProcessGone(t, parent, "dropped peer")
	assertProcessGone(t, child, "dropped peer child")
}

func TestPromptEndDropsEnginePeer(t *testing.T) {
	eng, err := mow.New(mow.Options{
		Workspace: t.TempDir(),
		NoSession: true,
		Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "done"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	parent, child, c := startSleepingTree(t)
	tool := &delegateTool{
		peers: map[string]*peerSlot{
			"k": {client: c, sessionID: "s1", lastUsed: time.Now()},
		},
	}
	eng.AddOnEvent(func(ev mow.Event) {
		if ev.Type == mow.EventRunEnd {
			tool.dropPeers()
		}
	})
	if _, err := eng.Prompt(context.Background(), "hi"); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	assertProcessGone(t, parent, "peer after Prompt")
	assertProcessGone(t, child, "peer child after Prompt")
}

// TestEngineCloseKillsPooledPeer is the RegisterFromEngine path: cleanup
// registered on the Engine must kill the tree when the session ends.
func TestEngineCloseKillsPooledPeer(t *testing.T) {
	eng, err := mow.New(mow.Options{
		Workspace: t.TempDir(),
		NoSession: true,
		Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "unused"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	parent, child, c := startSleepingTree(t)
	tool := &delegateTool{
		peers: map[string]*peerSlot{
			"k": {client: c, sessionID: "s1", lastUsed: time.Now()},
		},
	}
	eng.RegisterCleanup(func() { tool.closeAll() })
	if err := eng.Close(); err != nil {
		t.Fatalf("Engine.Close: %v", err)
	}
	assertProcessGone(t, parent, "pooled peer")
	assertProcessGone(t, child, "peer child")
}

// TestReleaseSharedPeersKillsPooledClient covers the RegisterFromConfig /
// BeforeNew generation-release hook (mow acp/run exit).
func TestReleaseSharedPeersKillsPooledClient(t *testing.T) {
	parent, child, c := startSleepingTree(t)
	tool := &delegateTool{
		peers: map[string]*peerSlot{
			"k": {client: c, sessionID: "s1", lastUsed: time.Now()},
		},
	}
	sharedMu.Lock()
	prev, prevGen := sharedDelegate, sharedGen
	prevOrph := orphanedByGen
	sharedDelegate = tool
	sharedGen = 42
	orphanedByGen = map[int][]*delegateTool{}
	sharedMu.Unlock()
	t.Cleanup(func() {
		sharedMu.Lock()
		sharedDelegate, sharedGen, orphanedByGen = prev, prevGen, prevOrph
		sharedMu.Unlock()
	})
	releaseSharedPeers(42)
	assertProcessGone(t, parent, "shared peer")
	assertProcessGone(t, child, "shared peer child")
}

func TestPeerDiesWhenParentProcessExits(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Pdeathsig is Linux-only")
	}
	if os.Getenv("MOW_ACP_SPAWN_PEER") == "1" {
		c := &Client{Command: []string{"sleep", "60"}}
		if err := c.startProcess(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		path := os.Getenv("MOW_ACP_SPAWN_PEER_PID")
		if path == "" || os.WriteFile(path, []byte(strconv.Itoa(c.cmd.Process.Pid)), 0o600) != nil {
			os.Exit(1)
		}
		os.Exit(0) // skip Close: parent death must reap the peer
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "peer.pid")
	cmd := exec.Command(exe, "-test.run=^TestPeerDiesWhenParentProcessExits$")
	cmd.Env = append(os.Environ(), "MOW_ACP_SPAWN_PEER=1", "MOW_ACP_SPAWN_PEER_PID="+pidFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("spawner: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("peer pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		t.Fatalf("peer pid from spawner: %q (%v)", raw, err)
	}
	t.Cleanup(func() {
		if processAlive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("peer pid %d survived parent exit (Pdeathsig not delivered)", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func startSleepingTree(t *testing.T) (parent, child int, c *Client) {
	t.Helper()
	dir := t.TempDir()
	childFile := filepath.Join(dir, "child.pid")
	// sh stays alive waiting on a child sleep. Kill-parent-only would
	// reparent sleep to PID 1; process-group SIGTERM should reap both.
	script := fmt.Sprintf("sleep 120 & echo $! >%s; wait", childFile)
	c = &Client{Command: []string{"sh", "-c", script}}
	if err := c.startProcess(); err != nil {
		t.Fatalf("startProcess: %v", err)
	}
	if c.cmd == nil || c.cmd.Process == nil {
		t.Fatal("startProcess left cmd unset")
	}
	parent = c.cmd.Process.Pid
	t.Cleanup(func() {
		_ = c.Close()
		// Best-effort: never leave a leaked tree if the test fails mid-way.
		if processAlive(parent) {
			_ = syscall.Kill(-parent, syscall.SIGKILL)
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		if b, err := os.ReadFile(childFile); err == nil {
			if n, convErr := strconv.Atoi(strings.TrimSpace(string(b))); convErr == nil && n > 0 {
				child = n
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("child pid file not written")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !processAlive(parent) {
		t.Fatalf("parent pid %d died before close", parent)
	}
	if !processAlive(child) {
		t.Fatalf("child pid %d died before close", child)
	}
	return parent, child, c
}

func assertProcessGone(t *testing.T, pid int, label string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			ppid := parentPID(pid)
			t.Fatalf("%s pid %d still alive after teardown (ppid=%d; ppid=1 means leaked to init)", label, pid, ppid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func parentPID(pid int) int {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "PPid:") {
			n, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "PPid:")))
			return n
		}
	}
	return 0
}
