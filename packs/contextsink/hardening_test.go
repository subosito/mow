package contextsink

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/subosito/mow"
)

func TestContextSearchRejectsSymlinkStoredFile(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("super-secret-token=leaked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tdir := filepath.Join(root, "sess1.tools")
	if err := os.MkdirAll(tdir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tdir, "0001-bash-aabbccdd.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	tool := newContextSearchTool(root, "sess1")
	_, err := tool.Exec(context.Background(), json.RawMessage(`{"id":"0001-bash-aabbccdd.txt"}`))
	if err == nil || !strings.Contains(err.Error(), "expired or missing") {
		t.Fatalf("want missing error for symlink, got %v", err)
	}
}

func TestContextSearchRejectsSymlinkArchiveFile(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(secret, []byte("needle-outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adir := filepath.Join(root, "sess1.archive")
	if err := os.MkdirAll(adir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(adir, "0001-x.md")
	if err := os.Symlink(secret, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	tool := newContextSearchTool(root, "sess1")
	out, err := tool.Exec(context.Background(), json.RawMessage(`{"pattern":"needle-outside"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "needle-outside") {
		t.Fatalf("symlink archive must not be searched: %q", out)
	}
}

func TestContextSearchCancelDuringScan(t *testing.T) {
	root := t.TempDir()
	adir := filepath.Join(root, "sess1.archive")
	if err := os.MkdirAll(adir, 0o700); err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	body.WriteString("header\n")
	for i := 0; i < 5000; i++ {
		body.WriteString("noise line\n")
	}
	body.WriteString("marker-cancel-me\n")
	if err := os.WriteFile(filepath.Join(adir, "0001-x.md"), []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := newContextSearchTool(root, "sess1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tool.Exec(ctx, json.RawMessage(`{"pattern":"marker-cancel-me"}`))
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("want cancel error, got %v", err)
	}
}

func TestContextSearchConcurrentBudget(t *testing.T) {
	root := t.TempDir()
	adir := filepath.Join(root, "sess1.archive")
	if err := os.MkdirAll(adir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(adir, "0001-x.md"), []byte("hit marker-concurrent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := newContextSearchTool(root, "sess1")
	ctx := context.Background()
	const n = 8
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, err := tool.Exec(ctx, json.RawMessage(`{"pattern":"marker-concurrent"}`))
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
	if tool.retrieved[root+"/sess1"] > contextSearchMaxRetrieved {
		t.Fatalf("budget exceeded: %d", tool.retrieved[root+"/sess1"])
	}
}

func TestContextSearchConcurrentGetByID(t *testing.T) {
	root := t.TempDir()
	tdir := filepath.Join(root, "sess1.tools")
	if err := os.MkdirAll(tdir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tdir, "0001-read-aabbccdd.txt"), []byte(strings.Repeat("x", 5000)), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := newContextSearchTool(root, "sess1")
	const n = 8
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, err := tool.Exec(context.Background(), json.RawMessage(`{"id":"0001-read-aabbccdd.txt","window":1000}`))
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
}

func TestLoadConfigCapsMaxInlineBytes(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "mow.yaml")
	yaml := "extensions:\n  contextsink:\n    enabled: true\n    max_inline_bytes: 999999999\n"
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := mow.New(mow.Options{
		LoadUserConfig: true,
		Workspace:      dir,
		ConfigPaths:    []string{cfgPath},
		NoSession:      true,
		Chat: func(context.Context, []mow.Message, []mow.ToolSpec) (mow.Message, error) {
			return mow.Message{Role: "assistant", Content: "ok"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	cfg := loadConfig(eng)
	if cfg.MaxInlineBytes != maxConfigInlineBytes {
		t.Fatalf("max_inline_bytes=%d want cap %d", cfg.MaxInlineBytes, maxConfigInlineBytes)
	}
}

func TestStubPreviewRedactsSecrets(t *testing.T) {
	body := "api_key=super-secret-value\n" + strings.Repeat("x", defaultMaxInlineBytes)
	stub := formatStub("0001-bash-aabbccdd.txt", "bash", body)
	if strings.Contains(stub, "super-secret-value") {
		t.Fatalf("stub leaked secret: %q", stub)
	}
	if !strings.Contains(stub, "[redacted]") {
		t.Fatalf("stub missing redaction marker: %q", stub)
	}
}

func TestPathWithinRoot(t *testing.T) {
	root := filepath.Clean("/tmp/sessions/proj")
	if !pathWithinRoot(root, filepath.Join(root, "sess1.tools", "0001-bash-aabbccdd.txt")) {
		t.Fatal("expected path under root")
	}
	if pathWithinRoot(root, "/etc/passwd") {
		t.Fatal("expected escape blocked")
	}
	// Prefix confusion: sibling with shared prefix must not match.
	if pathWithinRoot(root, root+"-evil/x") {
		t.Fatal("prefix sibling must be blocked")
	}
	// Relative paths are rejected (cwd-dependent confusion).
	if pathWithinRoot("sessions/proj", "sessions/proj/x") {
		t.Fatal("relative root must be rejected")
	}
}

func TestRejectSymlinkIntermediateDir(t *testing.T) {
	root := t.TempDir()
	evil := t.TempDir()
	if err := os.WriteFile(filepath.Join(evil, "0001-bash-aabbccdd.txt"), []byte("leaked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// sess1.tools → evil
	if err := os.Symlink(evil, filepath.Join(root, "sess1.tools")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	path := filepath.Join(root, "sess1.tools", "0001-bash-aabbccdd.txt")
	if err := rejectSymlinkComponents(root, path); err == nil {
		t.Fatal("expected intermediate dir symlink rejected")
	}
	tool := newContextSearchTool(root, "sess1")
	_, err := tool.Exec(context.Background(), json.RawMessage(`{"id":"0001-bash-aabbccdd.txt"}`))
	if err == nil || !strings.Contains(err.Error(), "expired or missing") {
		t.Fatalf("want missing for tools dir symlink, got %v", err)
	}
}

func TestRuneWindow(t *testing.T) {
	// "héllo世界" = h é l l o 世 界 → 7 runes; off=1 win=3 → é l l
	body, start, total := runeWindow("héllo世界", 1, 3)
	if start != 1 || total != 7 || body != "éll" {
		t.Fatalf("body=%q start=%d total=%d", body, start, total)
	}
	if b, s, tot := runeWindow("abc", 10, 2); b != "" || s != 3 || tot != 3 {
		t.Fatalf("off past end: body=%q start=%d total=%d", b, s, tot)
	}
	if b, _, _ := runeWindow("abcdef", 2, 2); b != "cd" {
		t.Fatalf("mid window: %q", b)
	}
}

func TestContextSearchBudgetEvictionPreservesActiveSession(t *testing.T) {
	tool := newContextSearchTool(t.TempDir(), "active")
	activeKey := filepath.Join(t.TempDir(), "active")
	for i := 0; i < contextSearchMaxBudgetSessions+5; i++ {
		tool.charge(filepath.Join("/other", fmt.Sprintf("sess-%d", i)), 1)
	}
	tool.charge(activeKey, 42)
	if tool.retrieved[activeKey] != 42 {
		t.Fatalf("active session budget lost: %+v", tool.retrieved)
	}
}
