package lsp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCallRejectsOversizedFrame(t *testing.T) {
	huge := strings.Repeat("x", maxLSPFrameBytes+1)
	frames := lspFrames(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":%q}`, huge))
	c, _ := fakeClient(frames)
	_, err := c.call(context.Background(), "textDocument/hover", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "frame exceeds") {
		t.Fatalf("want frame cap error, got %v", err)
	}
}

func TestCallRejectsInvalidContentLength(t *testing.T) {
	c, _ := fakeClient("Content-Length: not-a-number\r\n\r\n")
	_, err := c.call(context.Background(), "textDocument/hover", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "invalid Content-Length") {
		t.Fatalf("want invalid length error, got %v", err)
	}
}

func TestDidOpenRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	secret := filepath.Join(target, "secret.go")
	if err := os.WriteFile(secret, []byte("package secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.go")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	c := &client{root: root}
	err := c.didOpen(context.Background(), link)
	if err == nil || !(strings.Contains(err.Error(), "symlink") || strings.Contains(err.Error(), "regular file")) {
		t.Fatalf("didOpen symlink: err=%v", err)
	}
}

func TestDidOpenRejectsHugeFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "big.go")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxLSPDidOpenBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &client{root: root}
	if err := c.didOpen(context.Background(), path); err == nil || !strings.Contains(err.Error(), "didOpen cap") {
		t.Fatalf("didOpen huge file: err=%v", err)
	}
}

func TestLSPToolsReadOnly(t *testing.T) {
	if !(hoverTool{}.ReadOnly() && defTool{}.ReadOnly()) {
		t.Fatal("lsp tools must be read-only")
	}
}

func TestReconnectingConcurrent(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(filePath, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestLSPHelperProcess", "--"},
		Root:    tmpDir,
	}
	t.Setenv("GO_WANT_LSP_HELPER", "1")
	rc := &reconnecting{cfg: cfg}
	defer rc.reset()

	const n = 12
	var wg sync.WaitGroup
	errCh := make(chan error, n*2)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			ctx := context.Background()
			if _, err := rc.hover(ctx, filePath, 0, 0); err != nil {
				errCh <- err
				return
			}
			if _, err := rc.definition(ctx, filePath, 0, 0); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestLocateRejectsPathOutsideServerRoot(t *testing.T) {
	c := &client{root: t.TempDir()}
	outside := filepath.Join(t.TempDir(), "other.go")
	if _, err := c.locate(context.Background(), outside); err == nil || !strings.Contains(err.Error(), "outside server root") {
		t.Fatalf("got %v", err)
	}
}

func TestRestartableErrors(t *testing.T) {
	if restartable(fmt.Errorf("lsp: path %q outside server root", "x")) {
		t.Fatal("path jail must not restart the server")
	}
	if restartable(fmt.Errorf("no hover")) {
		t.Fatal("RPC application error must not restart the server")
	}
	if !restartable(io.EOF) {
		t.Fatal("EOF should restart")
	}
	if !restartable(context.DeadlineExceeded) {
		t.Fatal("deadline should restart")
	}
}

func TestWithRetryDoesNotRetryWhenContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rc := &reconnecting{c: &client{root: t.TempDir()}}
	calls := 0
	_, err := rc.withRetry(ctx, func(*client) (string, error) {
		calls++
		return "", context.Canceled
	})
	if calls != 0 {
		t.Fatalf("canceled ctx must not enter RPC: calls=%d", calls)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestWithRetryDoesNotReconnectAfterCanceledRPC(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rc := &reconnecting{c: &client{root: t.TempDir()}}
	calls := 0
	_, err := rc.withRetry(ctx, func(*client) (string, error) {
		calls++
		cancel()
		return "", context.Canceled
	})
	if calls != 1 {
		t.Fatalf("retried canceled RPC: calls=%d", calls)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if rc.c != nil {
		t.Fatal("dead client should be reset")
	}
}
