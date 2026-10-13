// Command codemesh serves a workspace of repositories: a map of each one's
// smells, a review of its changes and pull requests, and a diagnosis of its
// health.
//
//	codemesh [-addr localhost:7777] [-poll 1s] [path|url ...]
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
	"path/filepath"
	"syscall"
	"time"

	"github.com/joaomdsg/codemesh/internal/analysis"
	"github.com/joaomdsg/codemesh/internal/app"
	"github.com/joaomdsg/codemesh/internal/code"
	"github.com/joaomdsg/codemesh/internal/gitx"
	"github.com/joaomdsg/codemesh/internal/lang"
	"github.com/joaomdsg/codemesh/internal/prognosis"
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
	addr string
	poll time.Duration
	args []string
}

func parseFlags() options {
	var o options
	flag.StringVar(&o.addr, "addr", "localhost:7777", "address to serve on")
	flag.DurationVar(&o.poll, "poll", time.Second, "how often to check working trees for changes")
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "usage: codemesh [flags] [path|url ...]")
		flag.PrintDefaults()
	}
	flag.Parse()
	o.args = flag.Args()
	return o
}

func run() error {
	o := parseFlags()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	a, err := app.New(ctx, app.Config{Poll: o.poll, Log: log})
	if err != nil {
		return err
	}
	for _, arg := range o.args {
		if err := a.AddRepo(arg); err != nil {
			return fmt.Errorf("add %s: %w", arg, err)
		}
	}

	ln, err := net.Listen("tcp", o.addr)
	if err != nil {
		return err
	}
	origin, err := originOf(o.addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: ui.New(a, origin), ReadHeaderTimeout: 10 * time.Second}
	log.Info("serving", "url", origin)
	return serve(ctx, srv, ln)
}

// serve serves on ln. The end of ctx or a server error shuts the server down.
func serve(ctx context.Context, srv *http.Server, ln net.Listener) error {
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
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

// listPrognoses prints a tree's prognoses, one per line, most urgent first. It
// reads the tree as it is, and registers nothing in the workspace.
func listPrognoses() error {
	dir := "."
	if len(os.Args) > 2 {
		dir = os.Args[2]
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	// Outside git the engine still loads the tree; it only has no churn.
	repo, root := &gitx.Repo{Dir: abs}, "."
	if r, err := gitx.Open(abs); err == nil {
		repo = r
		if root, err = filepath.Rel(r.Dir, abs); err != nil {
			return err
		}
	}
	eng := analysis.New(analysis.Config{
		Repo: func(string) (*gitx.Repo, error) { return repo, nil },
		Load: func(_ context.Context, dir string) (*code.Snapshot, error) { return lang.Load(dir) },
	})
	res := eng.Get(context.Background(), analysis.Key{Repo: "dir", Rev: analysis.Worktree, Root: root})
	defer res.Release()
	if res.Err != nil {
		return res.Err
	}
	for _, g := range prognosis.Find(res.Snap, res.Findings) {
		fmt.Println(g)
	}
	return nil
}
