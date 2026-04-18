// Command port-manager runs the dashboard described in
// .tenet/spec/2026-04-17-port-manager.md. The wiring order is:
//
//	config → store → scanner → inspector → process → server → ListenAndServe
//
// Failures during wiring print to stderr and exit non-zero — there is
// no graceful "start the server anyway" mode. The HTTP server uses
// the timeouts mandated by Iron Law #12 (set in server.NewHTTPServer).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/JeiKeiLim/my-home-index-server/internal/auth"
	"github.com/JeiKeiLim/my-home-index-server/internal/config"
	"github.com/JeiKeiLim/my-home-index-server/internal/inspector"
	"github.com/JeiKeiLim/my-home-index-server/internal/process"
	"github.com/JeiKeiLim/my-home-index-server/internal/scanner"
	"github.com/JeiKeiLim/my-home-index-server/internal/server"
	"github.com/JeiKeiLim/my-home-index-server/internal/store"
)

// version is overridable via -ldflags "-X main.version=…" in release
// builds (job-10). The default lets local `go run` print something
// recognisable.
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "port-manager: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("port-manager", flag.ContinueOnError)
	opts := config.Options{StdOut: os.Stdout}
	config.BindFlags(fs, &opts)
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}
	if *showVersion {
		fmt.Println("port-manager", version)
		return nil
	}

	cfg, err := config.Load(opts)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	st, err := openStoreForCfg(cfg)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}

	sc, err := scanner.Auto(cfg)
	if err != nil {
		return fmt.Errorf("init scanner: %w", err)
	}

	insp := inspector.NewGopsutil()

	pm, err := process.New(cfg, process.WithStore(st))
	if err != nil {
		return fmt.Errorf("init process manager: %w", err)
	}

	srv := server.New(cfg, auth.New(cfg), sc, insp, pm, st)

	addr := fmt.Sprintf("0.0.0.0:%d", cfg.Port)
	httpSrv := srv.NewHTTPServer(addr)

	fmt.Printf("port-manager %s · listening on %s · public host %s · range %d-%d\n",
		version, addr, cfg.PublicHost, cfg.PortMin, cfg.PortMax)

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		fmt.Fprintf(os.Stderr, "port-manager: received %s, shutting down\n", sig)
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("listen: %w", err)
		}
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("port-manager: shutdown: %v", err)
	}
	return nil
}

// openStoreForCfg opens the store at cfg.StateDir's parent (Store.Open
// expects HOME-style root and appends .port-manager itself). If the
// caller has set a custom STATE_DIR, we honour the absolute path by
// using its parent as the root.
func openStoreForCfg(cfg *config.Config) (*store.Store, error) {
	root := cfg.StateDir
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		return store.Open(home)
	}
	// store.Open joins root + ".port-manager"; if cfg.StateDir is
	// already that exact directory, walk one level up so the structure
	// stays correct.
	parent := root
	if base := basenameOf(root); base == ".port-manager" {
		parent = parentOf(root)
	}
	return store.Open(parent)
}

func basenameOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}

func parentOf(p string) string {
	for i := len(p) - 1; i > 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[:i]
		}
	}
	return "."
}
