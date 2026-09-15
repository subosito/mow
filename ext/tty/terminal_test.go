//go:build unix

package tty

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestChatProcess(t *testing.T) {
	if os.Getenv("MOW_CHAT_TEST_PROCESS") != "1" {
		return
	}
	os.Exit(runCmd([]string{"--no-session"}))
}

type terminalSession struct {
	t        *testing.T
	cmd      *exec.Cmd
	pty      *os.File
	mu       sync.Mutex
	output   strings.Builder
	done     chan error
	readDone chan struct{}
	prompts  chan string
}

func startTerminal(t *testing.T, term string) *terminalSession {
	t.Helper()
	s := &terminalSession{t: t, done: make(chan error, 1), readDone: make(chan struct{}), prompts: make(chan string, 16)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/models") {
			fmt.Fprint(w, `{"data":[{"id":"gpt-5-mini"}]}`)
			return
		}
		var body struct {
			Messages []struct{ Role, Content string }
			Stream   bool
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Errorf("request: %v", err)
			w.WriteHeader(400)
			return
		}
		prompt := ""
		for _, msg := range body.Messages {
			if msg.Role == "user" {
				prompt = msg.Content
			}
		}
		s.prompts <- prompt
		if prompt == "slow" {
			<-req.Context().Done()
			return
		}
		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer-marker\"}}]}\n\ndata: [DONE]\n\n")
		} else {
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"answer-marker"}}]}`)
		}
	}))
	t.Cleanup(server.Close)
	s.cmd = exec.Command(os.Args[0], "-test.run=^TestChatProcess$")
	s.cmd.Env = append(os.Environ(), "MOW_CHAT_TEST_PROCESS=1", "MOW_HOME="+t.TempDir(),
		"MOW_API_KEY=test-key", "MOW_MODEL=gpt-5-mini", "MOW_WIRE=openai-chat-completions",
		"MOW_BASE_URL="+server.URL+"/v1", "TERM="+term, "NO_COLOR=1", "INPUTRC="+os.DevNull)
	var err error
	s.pty, err = pty.StartWithSize(s.cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	go func() { s.done <- s.cmd.Wait() }()
	go func() {
		defer close(s.readDone)
		buf := make([]byte, 4096)
		for {
			n, err := s.pty.Read(buf)
			if n > 0 {
				s.mu.Lock()
				s.output.Write(buf[:n])
				s.mu.Unlock()
				if strings.Contains(string(buf[:n]), "\x1b[6n") {
					_, _ = io.WriteString(s.pty, "\x1b[1;1R")
				}
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = s.cmd.Process.Kill()
		_ = s.pty.Close()
		<-s.readDone
	})
	return s
}

func (s *terminalSession) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.output.String()
}

func (s *terminalSession) write(text string) {
	s.t.Helper()
	if _, err := io.WriteString(s.pty, text); err != nil {
		s.t.Fatal(err)
	}
}

func (s *terminalSession) waitText(text string, count int) {
	s.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Count(s.text(), text) >= count {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.t.Fatalf("missing %q (%d):\n%q", text, count, s.text())
}

func (s *terminalSession) readyAfter(text string) {
	s.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		out := s.text()
		if marker := strings.LastIndex(out, text); marker >= 0 && strings.LastIndex(out, "you> ") > marker {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.t.Fatalf("no input prompt after %q: %q", text, s.text())
}

func (s *terminalSession) prompt(want string) {
	s.t.Helper()
	select {
	case got := <-s.prompts:
		if got != want {
			s.t.Fatalf("prompt = %q, want %q; output %q", got, want, s.text())
		}
	case <-time.After(8 * time.Second):
		s.t.Fatalf("no prompt %q; terminal: %q", want, s.text())
	}
}

func (s *terminalSession) waitExit(code int) {
	s.t.Helper()
	select {
	case err := <-s.done:
		got := 0
		if err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				got = e.ExitCode()
			} else {
				s.t.Fatal(err)
			}
		}
		if got != code {
			s.t.Fatalf("exit %d, want %d: %q", got, code, s.text())
		}
	case <-time.After(8 * time.Second):
		s.t.Fatalf("process did not exit: %q", s.text())
	}
}

func TestTerminalEditingPasteAndStream(t *testing.T) {
	s := startTerminal(t, "xterm-256color")
	s.waitText("you> ", 1)
	s.write("\r")
	s.waitText("you> ", 2)
	s.write("\x1b[200~line one\nline two\x1b[201~")
	time.Sleep(100 * time.Millisecond)
	select {
	case p := <-s.prompts:
		t.Fatalf("paste submitted itself: %q", p)
	default:
	}
	s.write("\r")
	s.prompt("line one\nline two")
	s.waitText("answer-marker", 1)
	s.readyAfter("answer-marker")
	s.write("\x1b[A\r")
	s.prompt("line one\nline two")
	s.waitText("answer-marker", 2)
	s.readyAfter("answer-marker")
	s.write("first\nsecond\r")
	s.prompt("first\nsecond")
	s.waitText("answer-marker", 3)
	s.readyAfter("answer-marker")
	s.write("hellx\x7fo\r")
	s.prompt("hello")
	s.waitText("answer-marker", 4)
	s.readyAfter("answer-marker")
	s.write("/he\t\r")
	s.waitText("commands:", 1)
	s.readyAfter("commands:")
	s.write("discard\x03")
	time.Sleep(100 * time.Millisecond)
	s.write("kept\r")
	s.prompt("kept")
	s.waitText("answer-marker", 5)
	s.readyAfter("answer-marker")
	s.write("slow\r")
	s.prompt("slow")
	s.write("\x03")
	s.waitText("cancelled", 1)
	s.readyAfter("cancelled")
	s.write("after cancel\r")
	s.prompt("after cancel")
	s.waitText("answer-marker", 6)
	s.readyAfter("answer-marker")
	s.write("\x04")
	s.waitExit(0)
	if count := strings.Count(s.text(), "answer-marker"); count != 6 {
		t.Fatalf("answer printed %d times, want 6: %q", count, s.text())
	}
	if strings.Contains(s.text(), "\x1b[?1049") {
		t.Fatal("tty entered an alternate screen")
	}
}

func TestTerminalSIGTERMAtPrompt(t *testing.T) {
	s := startTerminal(t, "xterm")
	s.waitText("you> ", 1)
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	s.waitExit(143)
}

func TestTerminalSIGTERMDuringTurn(t *testing.T) {
	s := startTerminal(t, "xterm")
	s.waitText("you> ", 1)
	s.write("slow\r")
	s.prompt("slow")
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	s.waitExit(143)
}

func TestDumbTerminalUsesPlainInputAndOutput(t *testing.T) {
	s := startTerminal(t, "dumb")
	s.write("hello\n\n/quit\n")
	s.prompt("hello")
	s.waitExit(0)
	if out := s.text(); strings.Contains(out, "\x1b") || strings.Contains(out, "you> ") {
		t.Fatalf("terminal UI in plain mode: %q", out)
	}
}
