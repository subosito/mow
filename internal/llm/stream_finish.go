package llm

import (
	"context"
	"io"
	"sync"
	"time"
)

const streamFinishGrace = 250 * time.Millisecond

// A protocol completion frame can arrive before HTTP EOF. Closing immediately
// can reset a successful request while the gateway is still finalizing it.
// Drain a small tail, bounded in both time and bytes. Cleanup failure must not
// turn a complete model response into an error or delay a user cancellation.
func finishStream(ctx context.Context, body io.ReadCloser, complete bool) {
	var once sync.Once
	closeBody := func() { once.Do(func() { _ = body.Close() }) }
	defer closeBody()
	if !complete || ctx.Err() != nil {
		return
	}
	timer := time.AfterFunc(streamFinishGrace, closeBody)
	defer timer.Stop()
	stop := context.AfterFunc(ctx, closeBody)
	defer stop()
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 64<<10))
}
