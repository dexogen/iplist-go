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

	"github.com/dexogen/iplist-go/internal/app"
)

func main() {
	cfg := app.ConfigFromEnv()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel()}))

	data, err := app.LoadAllData(cfg)
	if err != nil {
		logger.Error("load data", "error", err)
		os.Exit(1)
	}
	dnsStore := app.NewDNSRuntimeStore(cfg, logger)
	if err := dnsStore.Load(data); err != nil {
		logger.Error("load dns runtime", "error", err)
		os.Exit(1)
	}
	data.Runtime = dnsStore
	exportCache := app.NewExportCache(cfg, data, logger)
	data.ExportCache = exportCache
	if err := exportCache.RefreshAll(); err != nil {
		logger.Error("build export cache", "error", err)
		os.Exit(1)
	}

	handler := app.NewServer(cfg, data, logger)
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	app.NewDNSUpdater(cfg, data, dnsStore, logger).Start(ctx)

	go func() {
		logger.Info("iplist-go started", "addr", cfg.HTTPAddr, "sets", len(data.Sets))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown", "error", err)
	}
}
