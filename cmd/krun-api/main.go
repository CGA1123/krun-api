package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/CGA1123/krun-api/internal/api"
	"github.com/CGA1123/krun-api/krun"
	"github.com/CGA1123/krun-api/machine"
)

// brewPrefix runs `brew --prefix` and returns the trimmed output.
// Returns "" if brew is not found or exits non-zero.
func brewPrefix() string {
	out, err := exec.Command("brew", "--prefix").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ensureBrewLibPath sets DYLD_LIBRARY_PATH to the Homebrew lib directory if
// it is not already set. This ensures that libkrun and its transitive dylib
// dependencies (e.g. virglrenderer) are found when dlopen is called in child
// processes (e.g. krun-vmm) that inherit this process's environment.
func ensureBrewLibPath() {
	if os.Getenv("DYLD_LIBRARY_PATH") != "" {
		return
	}
	prefix := brewPrefix()
	if prefix == "" {
		return
	}
	libPath := filepath.Join(prefix, "lib")
	if err := os.Setenv("DYLD_LIBRARY_PATH", libPath); err != nil {
		fmt.Fprintf(os.Stderr, "krun-api: failed to set DYLD_LIBRARY_PATH: %v\n", err)
	}
}

func main() {
	ensureBrewLibPath()

	var (
		listen    = flag.String("listen", ":8080", "HTTP listen address")
		vmmPath   = flag.String("vmm-path", "./krun-vmm", "path to krun-vmm binary")
		socketDir = flag.String("socket-dir", "/tmp/krun-api", "directory for VM sockets and console logs")
		logLevel  = flag.String("log-level", "info", "log level (debug, info, warn, error)")
	)
	flag.Parse()

	var level slog.Level
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		fmt.Fprintf(os.Stderr, "invalid log level: %v\n", err)
		os.Exit(1)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	// Map slog level to libkrun log level.
	krunLogLevel := krun.LogLevelOff
	switch level {
	case slog.LevelDebug:
		krunLogLevel = krun.LogLevelDebug
	case slog.LevelInfo:
		krunLogLevel = krun.LogLevelInfo
	case slog.LevelWarn:
		krunLogLevel = krun.LogLevelWarn
	case slog.LevelError:
		krunLogLevel = krun.LogLevelError
	}

	// Create machine manager.
	mgr, err := machine.NewManager(*vmmPath, *socketDir, krunLogLevel)
	if err != nil {
		slog.Error("failed to create manager", "error", err)
		os.Exit(1)
	}

	// Wire API routes.
	handler := api.NewHandler(mgr)
	router := api.NewRouter(handler)

	srv := &http.Server{
		Addr:    *listen,
		Handler: api.WithMiddleware(router),
	}

	// Graceful shutdown on SIGTERM/SIGINT.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	go func() {
		slog.Info("krun-api listening", "addr", *listen)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server error", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down...")

	// Stop all running VMs.
	mgr.StopAll()

	// Shutdown HTTP server.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
}
