// Command ppsc is a local property-purchase search engine: it periodically
// scrapes configurable real-estate sites (defaults target São Paulo) and
// presents the results in a web UI where you browse listings and manage the
// sites, filters and schedule.
package main

import (
	"context"
	"embed"
	"flag"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ppsc/internal/logging"
	"ppsc/internal/photos"
	"ppsc/internal/scheduler"
	"ppsc/internal/scraper"
	"ppsc/internal/server"
	"ppsc/internal/store"
)

//go:embed all:web
var webFS embed.FS

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "address for the web UI to listen on")
	dbPath := flag.String("db", "ppsc.db", "path to the SQLite database file")
	logPath := flag.String("log", "ppsc.log", "path to the log file (empty = console only)")
	photoDir := flag.String("photos", "photos", "directory for downloaded listing photos")
	debug := flag.Bool("debug", false, "verbose logging (logs every page fetched and parsed)")
	flag.Parse()

	logCloser, err := logging.Setup(*logPath, *debug)
	if err != nil {
		slog.Error("open log file", "err", err)
		os.Exit(1)
	}
	defer logCloser.Close()

	st, err := store.Open(*dbPath)
	if err != nil {
		slog.Error("open db", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := st.SeedDefaults(ctx); err != nil {
		slog.Error("seed defaults", "err", err)
	}

	// A 'running' scrape state left behind means the previous process died
	// mid-pass. Flag it interrupted so the UI can offer resume or start-over.
	if interrupted, _ := st.MarkInterruptedIfRunning(ctx); interrupted {
		slog.Warn("previous scrape was interrupted; resume or start over from the web UI")
	}

	fetcher := scraper.NewFetcher()
	chromePath := scraper.FindChrome()
	if chromePath == "" {
		slog.Warn("no Chrome/Chromium found; sites set to 'render with browser' will fail until one is installed")
	} else {
		slog.Info("headless browser available", "chrome", chromePath)
	}
	browser := scraper.NewBrowserFetcher(chromePath)
	photoMgr := photos.NewManager(*photoDir)
	sched := scheduler.New(st, fetcher, browser, photoMgr)
	go sched.Loop(ctx)

	web, err := fs.Sub(webFS, "web")
	if err != nil {
		slog.Error("web fs", "err", err)
		os.Exit(1)
	}
	srv := server.New(st, sched, fetcher, browser, web, *photoDir)

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("listening", "url", "http://"+*addr, "db", *dbPath, "log", *logPath)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("serve", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
}
