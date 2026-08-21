package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/subosito/mow/internal/policy"
)

func newTestPolicy(ws string, allowTools ...string) *policy.Policy {
	p := &policy.Policy{
		Workspace:    ws,
		MaxReadBytes: 1024 * 1024,
	}
	for _, t := range allowTools {
		switch t {
		case "write", "edit":
			p.AllowWrite = true
		case "bash":
			p.AllowShell = true
		}
	}
	return p
}

func TestToolMetadataAndRegistry(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	p := newTestPolicy(tmpDir, "read", "write", "edit", "glob", "grep", "bash")
	p.Hashline = true
	p.ExtraRoots = []string{t.TempDir()}

	allTools := Registry(p, []string{"read", "write", "edit", "glob", "grep", "bash"})
	// recall moved to packs/contextsink (covered by its own tests).
	// media tools moved to packs/media (covered by packs/media tests).

	for _, tool := range allTools {
		if tool.Name() == "" {
			t.Errorf("empty tool name for %T", tool)
		}
		if tool.Description() == "" {
			t.Errorf("empty description for tool %s", tool.Name())
		}
		if len(tool.Parameters()) == 0 {
			t.Errorf("empty parameters for tool %s", tool.Name())
		}
		if u, ok := tool.(interface{ Untrusted() bool }); ok {
			_ = u.Untrusted()
		}
		if ro, ok := tool.(interface{ ReadOnly() bool }); ok {
			_ = ro.ReadOnly()
		}
	}
}

func TestReadToolExtra(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	p := newTestPolicy(tmpDir)

	// Create test files
	fileContent := "line 1: hello\nline 2: world\n"
	filePath := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(filePath, []byte(fileContent), 0600); err != nil {
		t.Fatal(err)
	}

	rt := &readTool{p: p}

	t.Run("basic read", func(t *testing.T) {
		t.Parallel()
		res, err := rt.Exec(context.Background(), json.RawMessage(`{"path":"test.txt"}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res != fileContent {
			t.Errorf("got %q, want %q", res, fileContent)
		}
	})

	t.Run("path traversal protection", func(t *testing.T) {
		t.Parallel()
		_, err := rt.Exec(context.Background(), json.RawMessage(`{"path":"../../etc/passwd"}`))
		if err == nil {
			t.Fatal("expected path traversal error")
		}
	})

	t.Run("size limit truncation", func(t *testing.T) {
		t.Parallel()
		smallP := newTestPolicy(tmpDir)
		smallP.MaxReadBytes = 5
		rtSmall := &readTool{p: smallP}

		res, err := rtSmall.Exec(context.Background(), json.RawMessage(`{"path":"test.txt"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(res, "…(truncated") {
			t.Errorf("expected truncated output, got %q", res)
		}
	})

	t.Run("hashline mode truncation", func(t *testing.T) {
		t.Parallel()
		smallP := newTestPolicy(tmpDir)
		smallP.MaxReadBytes = 5
		smallP.Hashline = true
		rtSmall := &readTool{p: smallP}

		res, err := rtSmall.Exec(context.Background(), json.RawMessage(`{"path":"test.txt"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(res, "…(truncated") {
			t.Errorf("expected truncated output, got %q", res)
		}
	})

	t.Run("hashline mode", func(t *testing.T) {
		t.Parallel()
		hashP := newTestPolicy(tmpDir)
		hashP.Hashline = true
		rtHash := &readTool{p: hashP}

		res, err := rtHash.Exec(context.Background(), json.RawMessage(`{"path":"test.txt"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(res, ":") || !strings.Contains(res, "|") {
			t.Errorf("expected hashline format, got %q", res)
		}
	})

	t.Run("non-existent file nearby hint", func(t *testing.T) {
		t.Parallel()
		_, err := rt.Exec(context.Background(), json.RawMessage(`{"path":"tst.txt"}`))
		if err == nil || !strings.Contains(err.Error(), "no such file") {
			t.Fatalf("expected no such file error with hint, got %v", err)
		}
	})

	t.Run("invalid json args", func(t *testing.T) {
		t.Parallel()
		_, err := rt.Exec(context.Background(), json.RawMessage(`invalid json`))
		if err == nil {
			t.Fatal("expected json unmarshal error")
		}
	})
}

func TestWriteToolExtra(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	t.Run("write denied by policy", func(t *testing.T) {
		t.Parallel()
		p := newTestPolicy(tmpDir) // write not allowed
		wt := &writeTool{p: p}
		_, err := wt.Exec(context.Background(), json.RawMessage(`{"path":"a.txt","content":"data"}`))
		if err == nil || !strings.Contains(err.Error(), "write disabled") {
			t.Fatalf("expected tool write disabled error, got %v", err)
		}
	})

	t.Run("write nested directory creation and replace diff", func(t *testing.T) {
		t.Parallel()
		p := newTestPolicy(tmpDir, "write")
		wt := &writeTool{p: p}

		args, _ := json.Marshal(map[string]string{
			"path":    "nested/sub/file.txt",
			"content": "nested content",
		})
		res, err := wt.Exec(context.Background(), args)
		if err != nil {
			t.Fatalf("write failed: %v", err)
		}
		if !strings.Contains(res, "nested content") {
			t.Errorf("expected diff in output, got %q", res)
		}

		// Overwrite existing file to test formatReplaceDiff
		args2, _ := json.Marshal(map[string]string{
			"path":    "nested/sub/file.txt",
			"content": "updated nested content",
		})
		res2, err := wt.Exec(context.Background(), args2)
		if err != nil {
			t.Fatalf("overwrite failed: %v", err)
		}
		if !strings.Contains(res2, "updated nested content") {
			t.Errorf("expected replace diff, got %q", res2)
		}
	})

	t.Run("write path jail escape attempt", func(t *testing.T) {
		t.Parallel()
		p := newTestPolicy(tmpDir, "write")
		wt := &writeTool{p: p}

		args, _ := json.Marshal(map[string]string{
			"path":    "../outside.txt",
			"content": "bad",
		})
		_, err := wt.Exec(context.Background(), args)
		if err == nil {
			t.Fatal("expected path jail escape error")
		}
	})
}

func TestEditToolExtra(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	p := newTestPolicy(tmpDir, "edit")

	filePath := filepath.Join(tmpDir, "edit_me.txt")
	initialContent := "foo = 1\nbar = 2\n"
	_ = os.WriteFile(filePath, []byte(initialContent), 0600)

	et := &editTool{p: p}

	t.Run("edit classic replacement", func(t *testing.T) {
		t.Parallel()
		args, _ := json.Marshal(map[string]string{
			"path":       "edit_me.txt",
			"old_string": "foo = 1",
			"new_string": "foo = 100",
		})
		res, err := et.Exec(context.Background(), args)
		if err != nil {
			t.Fatalf("edit failed: %v", err)
		}
		if !strings.Contains(res, "foo = 100") {
			t.Errorf("expected diff with new string, got %q", res)
		}
	})

	t.Run("edit old_string not found", func(t *testing.T) {
		t.Parallel()
		args, _ := json.Marshal(map[string]string{
			"path":       "edit_me.txt",
			"old_string": "nonexistent_string",
			"new_string": "replacement",
		})
		_, err := et.Exec(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), "old_string not found") {
			t.Fatalf("expected old_string not found error, got %v", err)
		}
	})

	t.Run("edit empty old_string and line_hash", func(t *testing.T) {
		t.Parallel()
		args, _ := json.Marshal(map[string]string{
			"path":       "edit_me.txt",
			"new_string": "replacement",
		})
		_, err := et.Exec(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), "old_string or line_hash required") {
			t.Fatalf("expected parameter required error, got %v", err)
		}
	})

	t.Run("edit hashline mode error cases", func(t *testing.T) {
		t.Parallel()
		hashP := newTestPolicy(tmpDir, "edit")
		hashP.Hashline = true
		etHash := &editTool{p: hashP}

		fileHashPath := filepath.Join(tmpDir, "hash.txt")
		_ = os.WriteFile(fileHashPath, []byte("target line to replace\n"), 0600)

		// Short hash error
		argsShort, _ := json.Marshal(map[string]string{
			"path":       "hash.txt",
			"line_hash":  "abc",
			"new_string": "replaced line",
		})
		if _, err := etHash.Exec(context.Background(), argsShort); err == nil {
			t.Fatal("expected short hash error")
		}

		// Hash not found error
		argsNotFound, _ := json.Marshal(map[string]string{
			"path":       "hash.txt",
			"line_hash":  "12345678",
			"new_string": "replaced line",
		})
		if _, err := etHash.Exec(context.Background(), argsNotFound); err == nil {
			t.Fatal("expected hash not found error")
		}
	})
}

func TestGlobToolExtra(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	p := newTestPolicy(tmpDir)

	_ = os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("package main"), 0600)
	_ = os.WriteFile(filepath.Join(tmpDir, "b.go"), []byte("package main"), 0600)

	gt := &globTool{p: p}

	t.Run("match pattern", func(t *testing.T) {
		t.Parallel()
		res, err := gt.Exec(context.Background(), json.RawMessage(`{"pattern":"*.go"}`))
		if err != nil {
			t.Fatalf("glob failed: %v", err)
		}
		if !strings.Contains(res, "a.go") || !strings.Contains(res, "b.go") {
			t.Errorf("unexpected matches: %q", res)
		}
	})

	t.Run("no matches", func(t *testing.T) {
		t.Parallel()
		res, err := gt.Exec(context.Background(), json.RawMessage(`{"pattern":"*.txt"}`))
		if err != nil {
			t.Fatalf("glob failed: %v", err)
		}
		if res != "(no matches)" {
			t.Errorf("got %q, want (no matches)", res)
		}
	})
}

func TestGrepToolExtra(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	p := newTestPolicy(tmpDir)

	_ = os.WriteFile(filepath.Join(tmpDir, "code.go"), []byte("func targetFunc() {}\n"), 0600)
	// Write binary file with null byte
	_ = os.WriteFile(filepath.Join(tmpDir, "binary.bin"), []byte{0, 1, 2, 3, 't', 'a', 'r', 'g', 'e', 't'}, 0600)

	gt := &grepTool{p: p}

	t.Run("grep matching string", func(t *testing.T) {
		t.Parallel()
		res, err := gt.Exec(context.Background(), json.RawMessage(`{"pattern":"targetFunc"}`))
		if err != nil {
			t.Fatalf("grep failed: %v", err)
		}
		if !strings.Contains(res, "code.go (1)") || !strings.Contains(res, "  1:func targetFunc() {}") {
			t.Errorf("unexpected grep result: %q", res)
		}
		if strings.Contains(res, "binary.bin") {
			t.Error("grep should skip binary files")
		}
	})

	t.Run("empty pattern error", func(t *testing.T) {
		t.Parallel()
		_, err := gt.Exec(context.Background(), json.RawMessage(`{"pattern":""}`))
		if err == nil || !strings.Contains(err.Error(), "pattern required") {
			t.Fatalf("expected pattern required error, got %v", err)
		}
	})
}

func TestBashToolExtra(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	p := newTestPolicy(tmpDir, "bash")
	bt := &bashTool{p: p}

	t.Run("bash execution success", func(t *testing.T) {
		t.Parallel()
		res, err := bt.Exec(context.Background(), json.RawMessage(`{"command":"echo hello_bash"}`))
		if err != nil {
			t.Fatalf("bash failed: %v", err)
		}
		if !strings.Contains(res, "hello_bash") {
			t.Errorf("got %q, want hello_bash", res)
		}
	})

	t.Run("bash command required", func(t *testing.T) {
		t.Parallel()
		_, err := bt.Exec(context.Background(), json.RawMessage(`{"command":""}`))
		if err == nil || !strings.Contains(err.Error(), "command required") {
			t.Fatalf("expected command required error, got %v", err)
		}
	})

	t.Run("bash tool denied", func(t *testing.T) {
		t.Parallel()
		deniedP := newTestPolicy(tmpDir) // bash disabled
		btDenied := &bashTool{p: deniedP}
		_, err := btDenied.Exec(context.Background(), json.RawMessage(`{"command":"echo test"}`))
		if err == nil || !strings.Contains(err.Error(), "shell disabled") {
			t.Fatalf("expected tool bash disabled error, got %v", err)
		}
	})

	t.Run("bash timeout handling", func(t *testing.T) {
		t.Parallel()
		pTimeout := newTestPolicy(tmpDir, "bash")
		pTimeout.BashTimeoutSec = 1
		btTimeout := &bashTool{p: pTimeout}

		start := time.Now()
		res, err := btTimeout.Exec(context.Background(), json.RawMessage(`{"command":"sleep 5"}`))
		if err != nil {
			t.Fatalf("unexpected exec error: %v", err)
		}
		if !strings.Contains(res, "timed out") {
			t.Errorf("expected timeout message in output, got %q", res)
		}
		if time.Since(start) > 4*time.Second {
			t.Errorf("command took %v, want ~1s timeout", time.Since(start))
		}
	})

	t.Run("cappedBuffer truncation", func(t *testing.T) {
		t.Parallel()
		var buf cappedBuffer
		largeData := make([]byte, maxBashOutputBytes+100)
		for i := range largeData {
			largeData[i] = 'a'
		}
		n, err := buf.Write(largeData)
		if err != nil || n != len(largeData) {
			t.Fatalf("Write failed: n=%d err=%v", n, err)
		}
		if !buf.Truncated() {
			t.Fatal("expected cappedBuffer to be truncated")
		}
		// String() now carries an elision notice alongside the retained
		// head+tail, so assert the retained payload stays at the cap.
		if got := len(buf.head.String()) + len(buf.tail()); got != maxBashOutputBytes {
			t.Errorf("got retained length %d, want %d", got, maxBashOutputBytes)
		}
	})
}

func TestJailfileAndHelpersExtra(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	p := newTestPolicy(tmpDir, "write")

	t.Run("OpenJailedPathFor", func(t *testing.T) {
		t.Parallel()
		filePath := filepath.Join(tmpDir, "open_jailed.txt")
		f, path, err := OpenJailedPathFor(p, filePath, os.O_CREATE|os.O_WRONLY, 0600, true)
		if err != nil {
			t.Fatalf("OpenJailedPathFor failed: %v", err)
		}
		f.Close()
		if path != filePath {
			t.Errorf("got path %q, want %q", path, filePath)
		}
	})

	t.Run("openJailed nil policy", func(t *testing.T) {
		t.Parallel()
		_, _, err := openJailed(nil, "foo", os.O_RDONLY, 0)
		if err == nil || !strings.Contains(err.Error(), "workspace not set") {
			t.Fatalf("expected workspace not set error, got %v", err)
		}
	})

	t.Run("VerifyFDInJail nil file and helper", func(t *testing.T) {
		t.Parallel()
		err := VerifyFDInJail(p, nil)
		if err == nil || !strings.Contains(err.Error(), "nil file") {
			t.Fatalf("expected nil file error, got %v", err)
		}
		if err := VerifyFDInJail(p, nil); err == nil {
			t.Fatal("expected error from VerifyFDInJail")
		}
	})

	t.Run("workspaceRel formatting", func(t *testing.T) {
		t.Parallel()
		ws := "/home/user/project"
		if rel := workspaceRel(ws, "/home/user/project/src/main.go"); rel != "src/main.go" {
			t.Errorf("workspaceRel got %q, want src/main.go", rel)
		}
		if rel := workspaceRel("", "/abs/path"); rel != "/abs/path" {
			t.Errorf("workspaceRel empty ws got %q, want /abs/path", rel)
		}
	})
}
