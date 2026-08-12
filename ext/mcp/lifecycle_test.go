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
	"strconv"
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

func readMarkerPID(t *testing.T, cfg ServerConfig) int {
	t.Helper()
	marker := cfg.Env["MCP_MARKER"]
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(marker)
		if err == nil {
			pid, err := strconv.Atoi(string(raw))
			if err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("marker pid not written")
	return 0
}

func TestRegisterServersReplacesClosesPriorProcess(t *testing.T) {
	setupMCPTest(t)
	cfg := helperConfig(t, "peer", "marker")

	bumpBeforeNewGen(t)
	if err := RegisterServers([]ServerConfig{cfg}); err != nil {
		t.Fatal(err)
	}
	oldPID := readMarkerPID(t, cfg)
	if !pidAlive(oldPID) {
		t.Fatalf("helper pid %d not alive", oldPID)
	}

	bumpBeforeNewGen(t)
	if err := RegisterServers([]ServerConfig{cfg}); err != nil {
		t.Fatal(err)
	}
	newPID := readMarkerPID(t, cfg)
	if newPID == oldPID {
		t.Fatalf("expected new helper process, still pid %d", oldPID)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !pidAlive(oldPID) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("replaced helper pid %d still alive; new pid %d", oldPID, newPID)
}

func TestRegisterServersKeepsPriorGenAliveWhileEngineOpen(t *testing.T) {
	setupMCPTest(t)
	cfg := helperConfig(t, "peer", "marker")

	gen1 := bumpBeforeNewGen(t)
	if err := RegisterServers([]ServerConfig{cfg}); err != nil {
		t.Fatal(err)
	}
	pid1 := readMarkerPID(t, cfg)
	ext.NoteEngineGeneration(gen1)

	bumpBeforeNewGen(t)
	if err := RegisterServers([]ServerConfig{cfg}); err != nil {
		t.Fatal(err)
	}
	pid2 := readMarkerPID(t, cfg)
	if pid2 == pid1 {
		t.Fatalf("expected new helper process, still pid %d", pid1)
	}
	if !pidAlive(pid1) {
		t.Fatalf("gen1 transport closed while engine still open (pid %d dead)", pid1)
	}
	ext.ReleaseEngineGeneration(gen1)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !pidAlive(pid1) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("gen1 release did not close pid %d", pid1)
}

func TestReleaseEngineGenerationClosesStdioServer(t *testing.T) {
	setupMCPTest(t)
	cfg := helperConfig(t, "peer", "marker")

	gen := bumpBeforeNewGen(t)
	if err := RegisterServers([]ServerConfig{cfg}); err != nil {
		t.Fatal(err)
	}
	pid := readMarkerPID(t, cfg)
	ext.NoteEngineGeneration(gen)
	ext.ReleaseEngineGeneration(gen)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("generation release left helper pid %d alive", pid)
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
