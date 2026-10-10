// Command codemesh serves a map of a Go module's smells, a review queue for
// its working-tree changes and a diagnosis of its health.
//
//	codemesh [-addr localhost:7777] [-base REF] [-poll 1s] [-agent claude] [dir]
//	codemesh prognoses [dir]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joaomdsg/codemesh/internal/fix"
	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/live"
	"github.com/joaomdsg/codemesh/internal/prognosis"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/ui"
)

func main() {
	run := run
	if len(os.Args) > 1 && os.Args[1] == "prognoses" {
		run = listPrognoses
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "codemesh:", err)
		os.Exit(1)
	}
}

type options struct {
	addr, base, agent, dir string
	poll                   time.Duration
}

func parseFlags() options {
	var o options
	flag.StringVar(&o.addr, "addr", "localhost:7777", "address to serve on")
	flag.StringVar(&o.base, "base", "", "ref to review against (default: origin/HEAD, main or master)")
	flag.DurationVar(&o.poll, "poll", time.Second, "how often to check the tree for changes")
	flag.StringVar(&o.agent, "agent", "claude", "command that treats a prognosis, called like claude -p")
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "usage: codemesh [flags] [dir]")
		flag.PrintDefaults()
	}
	flag.Parse()
	o.dir = "."
	if flag.NArg() > 0 {
		o.dir = flag.Arg(0)
	}
	return o
}

func run() error {
	o := parseFlags()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	state, err := review.OpenState(live.StatePath(o.dir))
	if err != nil {
		return fmt.Errorf("review state: %w", err)
	}
	sweep(o.dir, log)
	src := live.New(o.dir, o.base, log)
	defer src.Close()

	ln, err := net.Listen("tcp", o.addr)
	if err != nil {
		return err
	}
	origin, err := originOf(o.addr)
	if err != nil {
		return err
	}
	// Registered before closeRuns, so it unregisters after: a second signal
	// while the runs close waits for them instead of killing codemesh and
	// leaving Claude and the checks running.
	ctx, stop := stopOn(log)
	defer stop()
	runs := fix.NewRuns(o.dir)
	runs.Agent = o.agent
	if self, err := os.Executable(); err == nil {
		runs.Self = self
	}
	defer closeRuns(runs)
	srv := &http.Server{Handler: ui.New(src, state, runs, origin), ReadHeaderTimeout: 10 * time.Second}
	log.Info("serving", "url", origin)
	return serve(ctx, srv, ln, func(ctx context.Context) {
		if a := src.Refresh(); a.Err != nil {
			log.Error("first analysis failed", "err", a.Err)
		}
		go src.Watch(ctx, o.poll)
	})
}

// closeRuns and exit are swapped by tests.
var (
	closeRuns = (*fix.Runs).Close
	exit      = os.Exit
)

// stopOn ends the context at the first signal. A second one says codemesh
// is still closing its runs, which ends what Claude and the checks run; a
// third quits at once and leaves them running.
func stopOn(log *slog.Logger) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		for n := 1; ; n++ {
			select {
			case <-done:
				return
			case <-sig:
			}
			switch n {
			case 1:
				cancel()
			case 2:
				log.Warn("closing the runs; signal again to quit now and leave Claude and the checks running")
			default:
				exit(1)
			}
		}
	}()
	return ctx, func() {
		signal.Stop(sig)
		close(done)
		cancel()
	}
}

// serve serves on ln, then calls watch. The end of ctx or a server error
// shuts the server down.
func serve(ctx context.Context, srv *http.Server, ln net.Listener, watch func(context.Context)) error {
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	watch(ctx)
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

// originOf builds the browser origin from the -addr flag rather than the
// resolved address: a browser on localhost sends "localhost", not
// "127.0.0.1", as its Origin.
func originOf(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("-addr %q: %w", addr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

// listPrognoses prints a tree's prognoses, one per line, most urgent first:
// the verdict an agent fixing one checks its work against.
func listPrognoses() error {
	dir := "."
	if len(os.Args) > 2 {
		dir = os.Args[2]
	}
	snap, fs, err := live.Load(dir)
	if err != nil {
		return err
	}
	for _, g := range prognosis.Find(snap, fs) {
		fmt.Println(g)
	}
	return nil
}

// sweep removes worktrees left by codemesh servers that died without
// cleaning up.
func sweep(dir string, log *slog.Logger) {
	repo, err := gitx.Open(dir)
	if err != nil {
		return
	}
	if err := repo.Sweep(); err != nil {
		log.Warn("removing worktrees of dead servers", "err", err)
	}
}
