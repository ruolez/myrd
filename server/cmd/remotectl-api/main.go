// remotectl-api: REST + WebSocket signaling control plane.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ruolez/remotectl/server/internal/app"
	"github.com/ruolez/remotectl/server/internal/config"
	"github.com/ruolez/remotectl/server/internal/db"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Migrations run before the listener opens.
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	a, err := app.New(ctx, cfg, pool, log, app.Options{})
	if err != nil {
		return err
	}
	defer a.Close()
	go a.Run(ctx)

	api := &http.Server{Addr: cfg.ListenAddr, Handler: a.Router(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	mux := http.NewServeMux()
	mux.Handle("/metrics", a.MetricsHandler())
	metricsSrv := &http.Server{Addr: cfg.MetricsAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	errc := make(chan error, 2)
	go func() { errc <- api.ListenAndServe() }()
	go func() { errc <- metricsSrv.ListenAndServe() }()
	log.Info("listening", "addr", cfg.ListenAddr, "metrics", cfg.MetricsAddr, "public_url", cfg.PublicURL, "version", app.Version)

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	log.Info("shutting down")
	a.Close() // closes every WebSocket so Shutdown does not wait on them
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = metricsSrv.Shutdown(sctx)
	return api.Shutdown(sctx)
}
