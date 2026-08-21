package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/subosito/mow/ext"
)

func setupMCPTest(t *testing.T) {
	t.Helper()
	ext.Reset()
	resetRegistryForTest()
	t.Cleanup(func() {
		resetRegistryForTest()
		ext.Reset()
	})
	ext.RegisterBeforeNew(func(...string) error { return nil })
}

func bumpBeforeNewGen(t *testing.T) int {
	t.Helper()
	if err := ext.BeforeNew(); err != nil {
		t.Fatal(err)
	}
	return ext.BeforeNewGeneration()
}

func helperConfig(t *testing.T, name, mode string) ServerConfig {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "marker")
	return ServerConfig{
		Name:    name,
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess", "--"},
		Env: map[string]string{
			"GO_WANT_HELPER_PROCESS": "1",
			"MCP_HELPER_MODE":        mode,
			"MCP_MARKER":             marker,
		},
	}
}

func mustStdioPID(t *testing.T, name string) int {
	t.Helper()
	pid := registeredStdioPID(name)
	if pid <= 0 {
		t.Fatalf("no stdio pid registered for %q", name)
	}
	return pid
}

func waitPIDDead(t *testing.T, pid int, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

// closeProbe is a toolTransport that only records Close. Used to test
// generation retirement without spawning a process or racing a pid marker.
type closeProbe struct {
	closed atomic.Bool
}

func (p *closeProbe) listTools(context.Context) ([]toolInfo, error) { return nil, nil }
func (p *closeProbe) callTool(context.Context, string, json.RawMessage) (string, error) {
	return "", nil
}
func (p *closeProbe) Close() error {
	p.closed.Store(true)
	return nil
}

func TestRegisterTransportKeepsPriorGenAliveWhileEngineOpen(t *testing.T) {
	setupMCPTest(t)
	old, cur := &closeProbe{}, &closeProbe{}

	gen1 := bumpBeforeNewGen(t)
	registerTransport("peer", gen1, old)
	ext.NoteEngineGeneration(gen1)

	gen2 := bumpBeforeNewGen(t)
	registerTransport("peer", gen2, cur)

	if old.closed.Load() {
		t.Fatal("prior gen transport closed while engine still open")
	}
	if cur.closed.Load() {
		t.Fatal("current transport closed during replace")
	}

	ext.ReleaseEngineGeneration(gen1)
	if !old.closed.Load() {
		t.Fatal("prior gen transport not closed after last engine released")
	}
	if cur.closed.Load() {
		t.Fatal("current gen transport closed when a prior gen was released")
	}
}

func TestRegisterTransportClosesPriorWhenNoEngineRefs(t *testing.T) {
	setupMCPTest(t)
	old, cur := &closeProbe{}, &closeProbe{}

	gen1 := bumpBeforeNewGen(t)
	registerTransport("peer", gen1, old)

	gen2 := bumpBeforeNewGen(t)
	registerTransport("peer", gen2, cur)
	if !old.closed.Load() {
		t.Fatal("unreferenced prior transport not closed on replace")
	}
	if cur.closed.Load() {
		t.Fatal("current transport closed during replace")
	}
}

func TestRegisterServersReplacesClosesPriorProcess(t *testing.T) {
	setupMCPTest(t)
	cfg := helperConfig(t, "peer", "marker")

	bumpBeforeNewGen(t)
	if err := RegisterServers([]ServerConfig{cfg}); err != nil {
		t.Fatal(err)
	}
	oldPID := mustStdioPID(t, "peer")
	if !pidAlive(oldPID) {
		t.Fatalf("helper pid %d not alive", oldPID)
	}

	bumpBeforeNewGen(t)
	if err := RegisterServers([]ServerConfig{cfg}); err != nil {
		t.Fatal(err)
	}
	newPID := mustStdioPID(t, "peer")
	if newPID == oldPID {
		t.Fatalf("expected new helper process, still pid %d", oldPID)
	}
	waitPIDDead(t, oldPID, fmt.Sprintf("replaced helper pid %d still alive; new pid %d", oldPID, newPID))
}

func TestRegisterServersKeepsPriorGenAliveWhileEngineOpen(t *testing.T) {
	setupMCPTest(t)
	cfg := helperConfig(t, "peer", "marker")

	gen1 := bumpBeforeNewGen(t)
	if err := RegisterServers([]ServerConfig{cfg}); err != nil {
		t.Fatal(err)
	}
	pid1 := mustStdioPID(t, "peer")
	ext.NoteEngineGeneration(gen1)

	bumpBeforeNewGen(t)
	if err := RegisterServers([]ServerConfig{cfg}); err != nil {
		t.Fatal(err)
	}
	pid2 := mustStdioPID(t, "peer")
	if pid2 == pid1 {
		t.Fatalf("expected new helper process, still pid %d", pid1)
	}
	if !pidAlive(pid1) {
		t.Fatalf("gen1 transport closed while engine still open (pid %d dead)", pid1)
	}
	ext.ReleaseEngineGeneration(gen1)
	waitPIDDead(t, pid1, fmt.Sprintf("gen1 release did not close pid %d", pid1))
}

func TestReleaseEngineGenerationClosesStdioServer(t *testing.T) {
	setupMCPTest(t)
	cfg := helperConfig(t, "peer", "marker")

	gen := bumpBeforeNewGen(t)
	if err := RegisterServers([]ServerConfig{cfg}); err != nil {
		t.Fatal(err)
	}
	pid := mustStdioPID(t, "peer")
	ext.NoteEngineGeneration(gen)
	ext.ReleaseEngineGeneration(gen)

	waitPIDDead(t, pid, fmt.Sprintf("generation release left helper pid %d alive", pid))
}

func TestStdioCallCancelUnblocks(t *testing.T) {
	setupMCPTest(t)
	cfg := ServerConfig{
		Name:    "hang",
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess", "--"},
		Env: map[string]string{
			"GO_WANT_HELPER_PROCESS": "1",
			"MCP_HELPER_MODE":        "hang_call",
		},
	}
	bumpBeforeNewGen(t)
	rc := &reconnectingClient{cfg: cfg}
	defer rc.Close()
	if err := rc.ensure(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := rc.callTool(ctx, "slow", json.RawMessage(`{}`))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cancel took %v; read likely blocked on newline", elapsed)
	}
}

func TestHTTPTransportConcurrentSession(t *testing.T) {
	t.Parallel()

	var sessionSet atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		if sid := r.Header.Get("Mcp-Session-Id"); sid == "sess-1" {
			sessionSet.Add(1)
		}
		w.Header().Set("Mcp-Session-Id", "sess-1")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": 1,
			"result": map[string]any{"tools": []any{}},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tr, err := newHTTPTransport(ServerConfig{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, err := tr.listTools(context.Background())
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if sessionSet.Load() == 0 {
		t.Fatal("expected concurrent requests to carry session header")
	}
}

func TestHTTPBodyBound(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		huge := strings.Repeat("a", maxHTTPBodyBytes+1)
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%q}`, huge)
	}))
	defer srv.Close()

	tr, err := newHTTPTransport(ServerConfig{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tr.call(context.Background(), "tools/list", map[string]any{})
	if err == nil {
		t.Fatal("expected bounded read error")
	}
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
