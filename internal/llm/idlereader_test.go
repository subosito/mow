package llm

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// blockingReader serves prefix, then blocks forever (simulating a wedged gateway).
type blockingReader struct {
	prefix []byte
	off    int
	block  chan struct{}
}

func (b *blockingReader) Read(p []byte) (int, error) {
	if b.off < len(b.prefix) {
		n := copy(p, b.prefix[b.off:])
		b.off += n
		return n, nil
	}
	<-b.block
	return 0, io.EOF
}

func TestIdleReaderReadsThenTimesOut(t *testing.T) {
	br := &blockingReader{prefix: []byte("hello world"), block: make(chan struct{})}
	defer close(br.block)
	ir := &idleReader{r: br, idle: 50 * time.Millisecond, ctx: context.Background()}

	buf := make([]byte, 4)
	var got strings.Builder
	var err error
	for {
		var n int
		n, err = ir.Read(buf)
		got.Write(buf[:n])
		if err != nil {
			break
		}
	}
	if got.String() != "hello world" {
		t.Fatalf("lost bytes across small reads: %q", got.String())
	}
	if err == nil || !strings.Contains(err.Error(), "idle timeout") {
		t.Fatalf("want idle timeout error, got %v", err)
	}
	// After abandoning, the reader must stay terminal (never reuse the pump).
	if _, err := ir.Read(buf); err == nil {
		t.Fatal("want sticky error after abandon")
	}
}

func TestIdleReaderNilContext(t *testing.T) {
	ir := &idleReader{r: strings.NewReader("abc"), idle: time.Second}
	buf := make([]byte, 8)
	n, err := ir.Read(buf)
	if err != nil || string(buf[:n]) != "abc" {
		t.Fatalf("nil ctx read failed: n=%d err=%v", n, err)
	}
}

func TestIdleReaderCancel(t *testing.T) {
	br := &blockingReader{block: make(chan struct{})}
	defer close(br.block)
	ctx, cancel := context.WithCancel(context.Background())
	ir := &idleReader{r: br, idle: 10 * time.Second, ctx: ctx}
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, err := ir.Read(make([]byte, 16)); err != context.Canceled {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}
