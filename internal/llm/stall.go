package llm

import (
	"errors"
	"io"
	"time"
)

// ErrStreamStalled means the server stopped sending a streamed response partway through.
var ErrStreamStalled = errors.New("openai: the response stream stalled")

// idleReader calls stall when no data has been read for idle; every read that returns data
// restarts the clock, so a slow stream that keeps arriving is never cut off.
type idleReader struct {
	r     io.Reader
	idle  time.Duration
	timer *time.Timer
}

func newIdleReader(r io.Reader, idle time.Duration, stall func()) *idleReader {
	return &idleReader{r: r, idle: idle, timer: time.AfterFunc(idle, stall)}
}

func (r *idleReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.timer.Reset(r.idle)
	}
	return n, err
}

func (r *idleReader) stop() {
	r.timer.Stop()
}
