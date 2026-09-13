package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/remnawave/limiter/internal/api"
	"github.com/remnawave/limiter/internal/prepaid"
	"github.com/sirupsen/logrus"
)

func main() {
	logger := logrus.New()
	logger.SetOutput(os.Stdout)

	cfg, err := prepaid.LoadConfig("")
	if err != nil {
		logger.Fatalf("Ошибка конфигурации prepaid limiter: %v", err)
	}

	level, err := logrus.ParseLevel(cfg.LogLevel)
	if err != nil {
		logger.Fatalf("Некорректный LOG_LEVEL: %v", err)
	}
	logger.SetLevel(level)
	if cfg.LogFormat == "json" {
		logger.SetFormatter(&logrus.JSONFormatter{})
	} else {
		logger.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
	}

	store, err := prepaid.NewStore(cfg.RedisURL)
	if err != nil {
		logger.Fatalf("Ошибка Redis: %v", err)
	}
	defer store.Close()

	bootstrapCtx, bootstrapCancel := context.WithCancel(context.Background())
	defer bootstrapCancel()
	if err := store.Ping(bootstrapCtx); err != nil {
		logger.Fatalf("Redis недоступен: %v", err)
	}

	panel := api.NewClient(cfg.RemnawaveAPIURL, cfg.RemnawaveAPIToken)
	panel.SetLogger(logger)
	if cfg.RemnawaveCookies != "" {
		panel.SetCookies(api.ParseCookies(cfg.RemnawaveCookies))
	}
	if cfg.RemnawaveHeaders != "" {
		panel.SetHeaders(api.ParseHeaders(cfg.RemnawaveHeaders))
	}

	service := prepaid.NewService(
		panel,
		store,
		cfg.LimitedSquadUUID,
		cfg.UnlimitedSquadUUID,
		logger,
	)
	server := prepaid.NewHTTPServer(cfg.WebhookAddr, cfg.WebhookSecret, service, logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go service.RunReconciler(ctx, cfg.ReconcileInterval)

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Run(ctx)
	}()

	logger.WithFields(logrus.Fields{
		"panel":              cfg.RemnawaveAPIURL,
		"limited_squad":      cfg.LimitedSquadUUID,
		"unlimited_squad":    cfg.UnlimitedSquadUUID,
		"reconcile_interval": cfg.ReconcileInterval.String(),
	}).Info("Prepaid traffic limiter запущен")

	select {
	case <-ctx.Done():
		logger.Info("Получен сигнал остановки")
	case err := <-errCh:
		if err != nil {
			logger.Fatalf("HTTP server завершился с ошибкой: %v", err)
		}
	}
}
