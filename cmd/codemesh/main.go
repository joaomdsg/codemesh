// Command codemesh serves a map of a Go module's smells and a review queue
// for its working-tree changes.
//
//	codemesh [-addr localhost:7777] [-base REF] [dir]
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

	"github.com/joaomdsg/codemesh/internal/live"
	"github.com/joaomdsg/codemesh/internal/review"
	"github.com/joaomdsg/codemesh/internal/ui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "codemesh:", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", "localhost:7777", "address to serve on")
	base := flag.String("base", "", "ref to review against (default: origin/HEAD, main or master)")
	poll := flag.Duration("poll", time.Second, "how often to check the tree for changes")
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "usage: codemesh [flags] [dir]")
		flag.PrintDefaults()
	}
	flag.Parse()
	dir := "."
	if flag.NArg() > 0 {
		dir = flag.Arg(0)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	state, err := review.OpenState(live.StatePath(dir))
	if err != nil {
		return fmt.Errorf("review state: %w", err)
	}
	src := live.New(dir, *base, log)
	defer src.Close()

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	origin, err := originOf(*addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: ui.New(src, state, origin), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	log.Info("serving", "url", origin)

	if a := src.Refresh(); a.Err != nil {
		log.Error("first analysis failed", "err", a.Err)
	}
	go src.Watch(ctx, *poll)

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
