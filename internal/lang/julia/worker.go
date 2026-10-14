package julia

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// idle is how long the worker waits for the next root before it exits.
var idle = 5 * time.Minute

// worker is one julia process reading roots in serve mode. Starting julia
// costs about a second, which a warm-up of many roots pays once this way.
type worker struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	stderr *lockedBuffer
	timer  *time.Timer
}

// lockedBuffer is julia's stderr, which exec copies in from its own goroutine.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *lockedBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

var bulk worker

// Close stops the warm-up worker, if it runs.
func (Frontend) Close() {
	bulk.mu.Lock()
	defer bulk.mu.Unlock()
	bulk.stop()
}

// run reads abs in the worker. ok is false when another load holds the
// worker, so a pool slot never waits on it.
func (w *worker) run(ctx context.Context, abs string) (in jl, ok bool, err error) {
	if !w.mu.TryLock() {
		return jl{}, false, nil
	}
	defer w.mu.Unlock()
	in, err = w.read(ctx, abs)
	return in, true, err
}

func (w *worker) read(ctx context.Context, abs string) (jl, error) {
	if w.timer != nil {
		w.timer.Stop()
	}
	if w.cmd == nil {
		if err := w.start(); err != nil {
			return jl{}, err
		}
	}
	// A cancelled load kills the process, which ends the read below.
	cmd := w.cmd
	unwatch := context.AfterFunc(ctx, func() { _ = cmd.Process.Kill() })
	defer func() {
		if !unwatch() {
			w.stop()
		}
	}()
	w.stderr.Reset()

	var resp struct {
		jl
		Error *string
	}
	_, err := io.WriteString(w.in, abs+"\n")
	var line []byte
	if err == nil {
		line, err = w.out.ReadBytes('\n')
	}
	if err == nil {
		err = json.Unmarshal(line, &resp)
	}
	if err != nil {
		w.stop()
		msg := strings.TrimSpace(w.stderr.String())
		if ctx.Err() != nil {
			return jl{}, ctx.Err()
		}
		return jl{}, fmt.Errorf("julia: %w: %s", err, msg)
	}
	// A timer that fired while this read held mu finds a newer one set.
	var t *time.Timer
	t = time.AfterFunc(idle, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.timer == t {
			w.stop()
		}
	})
	w.timer = t
	if resp.Error != nil {
		return jl{}, errors.New("julia: " + *resp.Error)
	}
	return resp.jl, nil
}

func (w *worker) start() error {
	cmd := exec.Command("julia", "--startup-file=no", "--history-file=no", "-e", juliaScript, "--", "--serve")
	w.stderr = &lockedBuffer{}
	cmd.Stderr = w.stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("julia: %w", err)
	}
	w.cmd, w.in, w.out = cmd, in, bufio.NewReader(out)
	return nil
}

// stop ends the process; the next run starts another. The caller holds mu.
func (w *worker) stop() {
	if w.cmd == nil {
		return
	}
	if w.timer != nil {
		w.timer.Stop()
	}
	_ = w.in.Close()
	_ = w.cmd.Process.Kill()
	_ = w.cmd.Wait()
	w.cmd = nil
}
